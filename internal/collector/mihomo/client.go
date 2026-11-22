// Package mihomo reads Mihomo connection, proxy, and provider metadata over
// read-only local WebSocket and HTTP GET endpoints.
package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/protocol"
)

const (
	defaultConnectionsPath      = "/connections"
	defaultProxiesPath          = "/proxies"
	defaultProvidersProxiesPath = "/providers/proxies"
	defaultVersionPath          = "/version"
	defaultDomainTopN           = 50
	defaultFallbackPollInterval = time.Second
	defaultWSIdleTimeout        = 15 * time.Second
	defaultDialTimeout          = 10 * time.Second
	defaultReconnectBackoff     = time.Second
	maxReconnectBackoff         = 60 * time.Second
	maxResponseBytes            = 8 << 20
)

// ConnCounters stores cumulative upload and download bytes for one connection id.
type ConnCounters struct {
	Upload   uint64
	Download uint64
}

// Options controls optional metadata extraction and stream timing.
type Options struct {
	CollectDomains       bool
	WatchDomains         []string
	DomainTopN           int
	FallbackPollInterval time.Duration
	WSIdleTimeout        time.Duration
	DialTimeout          time.Duration
	ReconnectBackoff     time.Duration
	InitialCounters      map[string]ConnCounters
	compiledWatch        *compiledWatchDomains
}

// DimensionDelta captures orthogonal traffic counters (group, process, domain).
type DimensionDelta struct {
	Type    string `json:"type"`
	Key     string `json:"key"`
	RxDelta uint64 `json:"rx_delta"`
	TxDelta uint64 `json:"tx_delta"`
}

// Snapshot is the complete delta produced by one CollectSnapshot call.
type Snapshot struct {
	Proxy      *protocol.ProxyDelta
	Devices    []protocol.DeviceDelta
	Dimensions []DimensionDelta
	Gaps       []protocol.Gap
}

type connectionsResponse struct {
	Connections []connectionRecord `json:"connections"`
}

type connectionRecord struct {
	ID       string             `json:"id"`
	Upload   uint64             `json:"upload"`
	Download uint64             `json:"download"`
	Chains   []string           `json:"chains"`
	Metadata connectionMetadata `json:"metadata"`
}

type connectionMetadata struct {
	SourceIP    string `json:"sourceIP"`
	Process     string `json:"process"`
	ProcessPath string `json:"processPath"`
	Host        string `json:"host"`
	SniffHost   string `json:"sniffHost"`
}

type deviceKey struct {
	ip       string
	outbound string
}

type domainAgg struct {
	delta   DimensionDelta
	watched bool
}

type parsedFrame struct {
	byOutbound map[string]*protocol.OutboundDelta
	byDevice   map[deviceKey]*protocol.DeviceDelta
	byGroup    map[string]*DimensionDelta
	byProcess  map[string]*DimensionDelta
	byDomain   map[string]*domainAgg
	gaps       []protocol.Gap
}

// ParseConnections converts Mihomo /connections JSON into proxy outbound deltas.
// Connection ids stay local to the agent and are never exported as metric labels.
func ParseConnections(
	prev map[string]ConnCounters,
	raw []byte,
) (protocol.ProxyDelta, []protocol.DeviceDelta, map[string]ConnCounters, []protocol.Gap, error) {
	parsed, next, err := parseConnectionsFrame(prev, raw, Options{}, time.Now().UTC())
	if err != nil {
		return protocol.ProxyDelta{}, nil, nil, nil, err
	}
	snap := parsed.toSnapshot(0)
	var proxy protocol.ProxyDelta
	if snap.Proxy != nil {
		proxy = *snap.Proxy
	} else {
		proxy = protocol.ProxyDelta{ByOutbound: []protocol.OutboundDelta{}}
	}
	return proxy, snap.Devices, next, snap.Gaps, nil
}

// ParseConnectionsWithOptions converts Mihomo /connections JSON into a Snapshot
// including group, process, and opt-in domain dimensions.
func ParseConnectionsWithOptions(
	prev map[string]ConnCounters,
	raw []byte,
	opts Options,
) (Snapshot, map[string]ConnCounters, error) {
	if opts.compiledWatch == nil && len(opts.WatchDomains) > 0 {
		opts.compiledWatch = compileWatchDomains(opts.WatchDomains)
	}
	parsed, next, err := parseConnectionsFrame(prev, raw, opts, time.Now().UTC())
	if err != nil {
		return Snapshot{}, nil, err
	}
	return parsed.toSnapshot(opts.DomainTopN), next, nil
}

func parseConnectionsFrame(
	prev map[string]ConnCounters,
	raw []byte,
	opts Options,
	now time.Time,
) (parsedFrame, map[string]ConnCounters, error) {
	var response connectionsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return parsedFrame{}, nil, fmt.Errorf("decode mihomo connections: %w", err)
	}

	next := make(map[string]ConnCounters, len(response.Connections))
	if prev == nil {
		// Startup frame only establishes baseline; emit zero deltas and zero gaps.
		for _, record := range response.Connections {
			id := strings.TrimSpace(record.ID)
			if id == "" {
				continue
			}
			if _, dup := next[id]; dup {
				continue
			}
			next[id] = ConnCounters{Upload: record.Upload, Download: record.Download}
		}
		return parsedFrame{}, next, nil
	}

	out := newParsedFrame()
	hasResetGap := false
	for _, record := range response.Connections {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			continue
		}
		if _, dup := next[id]; dup {
			continue
		}
		next[id] = ConnCounters{Upload: record.Upload, Download: record.Download}

		var uploadDelta, downloadDelta uint64
		if previous, seen := prev[id]; !seen {
			// New connection IDs start from 0 so short-lived connections are never dropped.
			uploadDelta = record.Upload
			downloadDelta = record.Download
		} else {
			var uploadReset, downloadReset bool
			uploadDelta, uploadReset = accounting.DeltaUint64(previous.Upload, record.Upload)
			downloadDelta, downloadReset = accounting.DeltaUint64(previous.Download, record.Download)
			if uploadReset || downloadReset {
				if !hasResetGap {
					out.gaps = append(out.gaps, protocol.Gap{
						Reason: protocol.GapCollectorReset,
						From:   now.Add(-time.Second),
						To:     now,
					})
					hasResetGap = true
				}
				continue
			}
		}

		if uploadDelta == 0 && downloadDelta == 0 {
			continue
		}

		outbound, group := extractChain(record.Chains)
		entry := out.byOutbound[outbound]
		if entry == nil {
			entry = &protocol.OutboundDelta{Name: outbound, Group: group}
			out.byOutbound[outbound] = entry
		} else if entry.Group == "" && group != "" {
			entry.Group = group
		}
		if outbound == "DIRECT" {
			entry.DirectTx += uploadDelta
			entry.DirectRx += downloadDelta
		} else {
			entry.ProxyTx += uploadDelta
			entry.ProxyRx += downloadDelta
		}

		if sourceIP, ok := lanSourceIP(record.Metadata.SourceIP); ok {
			key := deviceKey{ip: sourceIP, outbound: outbound}
			device := out.byDevice[key]
			if device == nil {
				device = &protocol.DeviceDelta{
					ObsIP:    sourceIP,
					Source:   protocol.SourceMihomo,
					Outbound: outbound,
				}
				out.byDevice[key] = device
			}
			device.TxDelta += uploadDelta
			device.RxDelta += downloadDelta
		}

		if group != "" {
			addDimension(out.byGroup, "group", group, downloadDelta, uploadDelta)
		}
		if proc := extractProcess(record.Metadata); proc != "" {
			addDimension(out.byProcess, "process", proc, downloadDelta, uploadDelta)
		}
		if dom, watched := extractDomain(record.Metadata, opts); dom != "" {
			agg := out.byDomain[dom]
			if agg == nil {
				agg = &domainAgg{
					delta: DimensionDelta{Type: "domain", Key: dom},
				}
				out.byDomain[dom] = agg
			}
			agg.delta.RxDelta += downloadDelta
			agg.delta.TxDelta += uploadDelta
			if watched {
				agg.watched = true
			}
		}
	}

	// Normal connection closures (IDs in prev missing from next) clean state silently
	// without emitting GapCollectorReset.
	return out, next, nil
}

func newParsedFrame() parsedFrame {
	return parsedFrame{
		byOutbound: make(map[string]*protocol.OutboundDelta),
		byDevice:   make(map[deviceKey]*protocol.DeviceDelta),
		byGroup:    make(map[string]*DimensionDelta),
		byProcess:  make(map[string]*DimensionDelta),
		byDomain:   make(map[string]*domainAgg),
	}
}

func addDimension(m map[string]*DimensionDelta, dimType, key string, rx, tx uint64) {
	d := m[key]
	if d == nil {
		d = &DimensionDelta{Type: dimType, Key: key}
		m[key] = d
	}
	d.RxDelta += rx
	d.TxDelta += tx
}

func (p *parsedFrame) mergeFrom(other parsedFrame) {
	for k, entry := range other.byOutbound {
		cur := p.byOutbound[k]
		if cur == nil {
			copied := *entry
			p.byOutbound[k] = &copied
			continue
		}
		if cur.Group == "" && entry.Group != "" {
			cur.Group = entry.Group
		}
		cur.DirectRx += entry.DirectRx
		cur.DirectTx += entry.DirectTx
		cur.ProxyRx += entry.ProxyRx
		cur.ProxyTx += entry.ProxyTx
		cur.Unadjusted = cur.Unadjusted || entry.Unadjusted
	}
	for k, dev := range other.byDevice {
		cur := p.byDevice[k]
		if cur == nil {
			copied := *dev
			p.byDevice[k] = &copied
			continue
		}
		cur.RxDelta += dev.RxDelta
		cur.TxDelta += dev.TxDelta
	}
	for k, dim := range other.byGroup {
		addDimension(p.byGroup, "group", k, dim.RxDelta, dim.TxDelta)
	}
	for k, dim := range other.byProcess {
		addDimension(p.byProcess, "process", k, dim.RxDelta, dim.TxDelta)
	}
	for k, dom := range other.byDomain {
		cur := p.byDomain[k]
		if cur == nil {
			copied := *dom
			p.byDomain[k] = &copied
			continue
		}
		cur.delta.RxDelta += dom.delta.RxDelta
		cur.delta.TxDelta += dom.delta.TxDelta
		if dom.watched {
			cur.watched = true
		}
	}
	p.gaps = append(p.gaps, other.gaps...)
}

func (p parsedFrame) isEmpty() bool {
	return len(p.byOutbound) == 0 &&
		len(p.byDevice) == 0 &&
		len(p.byGroup) == 0 &&
		len(p.byProcess) == 0 &&
		len(p.byDomain) == 0 &&
		len(p.gaps) == 0
}

func (p parsedFrame) toSnapshot(domainTopN int) Snapshot {
	var proxy *protocol.ProxyDelta
	if len(p.byOutbound) > 0 {
		outbounds := make([]protocol.OutboundDelta, 0, len(p.byOutbound))
		for _, entry := range p.byOutbound {
			outbounds = append(outbounds, *entry)
		}
		sort.Slice(outbounds, func(i, j int) bool {
			return outbounds[i].Name < outbounds[j].Name
		})
		proxy = &protocol.ProxyDelta{ByOutbound: outbounds}
	}

	var devices []protocol.DeviceDelta
	if len(p.byDevice) > 0 {
		devices = make([]protocol.DeviceDelta, 0, len(p.byDevice))
		for _, device := range p.byDevice {
			devices = append(devices, *device)
		}
		sort.Slice(devices, func(i, j int) bool {
			if devices[i].ObsIP != devices[j].ObsIP {
				return devices[i].ObsIP < devices[j].ObsIP
			}
			return devices[i].Outbound < devices[j].Outbound
		})
	}

	var dims []DimensionDelta
	for _, d := range p.byGroup {
		dims = append(dims, *d)
	}
	for _, d := range p.byProcess {
		dims = append(dims, *d)
	}
	dims = append(dims, finalizeDomains(p.byDomain, domainTopN)...)
	if len(dims) > 0 {
		sort.Slice(dims, func(i, j int) bool {
			if dims[i].Type != dims[j].Type {
				return dims[i].Type < dims[j].Type
			}
			totI := dims[i].RxDelta + dims[i].TxDelta
			totJ := dims[j].RxDelta + dims[j].TxDelta
			if totI != totJ {
				return totI > totJ
			}
			return dims[i].Key < dims[j].Key
		})
	}

	return Snapshot{
		Proxy:      proxy,
		Devices:    devices,
		Dimensions: dims,
		Gaps:       p.gaps,
	}
}

func lanSourceIP(raw string) (string, bool) {
	parsed := net.ParseIP(strings.TrimSpace(raw))
	if parsed == nil {
		return "", false
	}
	if parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast() {
		return parsed.String(), true
	}
	return "", false
}

// extractChain extracts the exit node (chains[0]) and top-level strategy group (chains[len-1]).
// Mihomo reports chains in leaf-to-root order: ["exit-node", ..., "top-group"].
func extractChain(chains []string) (outbound, group string) {
	if len(chains) == 0 {
		return "DIRECT", ""
	}
	first := strings.TrimSpace(chains[0])
	if first == "" || strings.EqualFold(first, "DIRECT") {
		outbound = "DIRECT"
	} else {
		outbound = first
	}
	if len(chains) > 1 {
		last := strings.TrimSpace(chains[len(chains)-1])
		if last != "" {
			group = last
		}
	}
	return outbound, group
}

// Client performs read-only HTTP GET and WebSocket requests against a Mihomo controller.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

func NewClient(baseURL, secret string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), secret: secret, http: httpClient}
}

// HTTPStatusError represents a non-2xx HTTP status returned by Mihomo.
type HTTPStatusError struct {
	Label      string
	StatusCode int
	Status     string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("mihomo %s returned %s", e.Label, e.Status)
}

func (c *Client) FetchConnections(ctx context.Context) ([]byte, error) {
	return c.getPath(ctx, defaultConnectionsPath, "connections")
}

func (c *Client) restURL(path string) (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("unsupported mihomo scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}

func (c *Client) getPath(ctx context.Context, path, label string) ([]byte, error) {
	targetURL, err := c.restURL(path)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		targetURL,
		nil,
	)
	if err != nil {
		return nil, err
	}
	if c.secret != "" {
		request.Header.Set("Authorization", "Bearer "+c.secret)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &HTTPStatusError{
			Label:      label,
			StatusCode: response.StatusCode,
			Status:     response.Status,
		}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) wsConnectionsURL() (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported mihomo scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + defaultConnectionsPath
	return u.String(), nil
}
