// Package mihomo reads Mihomo connection counters over local HTTP GET only.
package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/protocol"
)

const defaultConnectionsPath = "/connections"

// ConnCounters stores cumulative upload and download bytes for one connection id.
type ConnCounters struct {
	Upload   uint64
	Download uint64
}

type connectionsResponse struct {
	Connections []connectionRecord `json:"connections"`
}

type connectionRecord struct {
	ID       string   `json:"id"`
	Upload   uint64   `json:"upload"`
	Download uint64   `json:"download"`
	Chains   []string `json:"chains"`
}

// ParseConnections converts Mihomo /connections JSON into proxy outbound deltas.
// Connection ids stay local to the agent and are never exported as metric labels.
func ParseConnections(
	prev map[string]ConnCounters,
	raw []byte,
) (protocol.ProxyDelta, map[string]ConnCounters, []protocol.Gap, error) {
	var response connectionsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return protocol.ProxyDelta{}, nil, nil, fmt.Errorf("decode mihomo connections: %w", err)
	}

	next := make(map[string]ConnCounters, len(response.Connections))
	byOutbound := make(map[string]*protocol.OutboundDelta)
	now := time.Now().UTC()

	for _, record := range response.Connections {
		if strings.TrimSpace(record.ID) == "" {
			continue
		}
		next[record.ID] = ConnCounters{Upload: record.Upload, Download: record.Download}

		previous, seen := prev[record.ID]
		uploadDelta, uploadReset := accounting.DeltaUint64(previous.Upload, record.Upload)
		downloadDelta, downloadReset := accounting.DeltaUint64(previous.Download, record.Download)
		if !seen || uploadReset || downloadReset {
			continue
		}
		if uploadDelta == 0 && downloadDelta == 0 {
			continue
		}
		outbound := outboundName(record.Chains)
		entry := byOutbound[outbound]
		if entry == nil {
			entry = &protocol.OutboundDelta{Name: outbound}
			byOutbound[outbound] = entry
		}
		if outbound == "DIRECT" {
			entry.DirectTx += uploadDelta
			entry.DirectRx += downloadDelta
		} else {
			entry.ProxyTx += uploadDelta
			entry.ProxyRx += downloadDelta
		}
	}

	var gaps []protocol.Gap
	for id := range prev {
		if _, stillActive := next[id]; stillActive {
			continue
		}
		gaps = append(gaps, protocol.Gap{
			Reason: protocol.GapCollectorReset,
			From:   now.Add(-time.Second),
			To:     now,
		})
	}

	result := protocol.ProxyDelta{ByOutbound: make([]protocol.OutboundDelta, 0, len(byOutbound))}
	for _, entry := range byOutbound {
		result.ByOutbound = append(result.ByOutbound, *entry)
	}
	return result, next, gaps, nil
}

func outboundName(chains []string) string {
	if len(chains) == 0 {
		return "DIRECT"
	}
	last := strings.TrimSpace(chains[len(chains)-1])
	if strings.EqualFold(last, "DIRECT") || last == "" {
		return "DIRECT"
	}
	return last
}

// Client performs read-only GET requests against a local Mihomo controller.
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

func (c *Client) FetchConnections(ctx context.Context) ([]byte, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+defaultConnectionsPath,
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
		return nil, fmt.Errorf("mihomo connections returned %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Collector tracks Mihomo connection deltas between polls.
type Collector struct {
	client *Client
	prev   map[string]ConnCounters
}

func NewCollector(client *Client) *Collector {
	return &Collector{client: client, prev: make(map[string]ConnCounters)}
}

func (c *Collector) Capability() protocol.Capability {
	return protocol.CapMihomo
}

func (c *Collector) Collect(
	ctx context.Context,
	at time.Time,
) (*protocol.ProxyDelta, []protocol.Gap, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	raw, err := c.client.FetchConnections(ctx)
	if err != nil {
		return nil, nil, err
	}
	delta, next, gaps, err := ParseConnections(c.prev, raw)
	if err != nil {
		return nil, nil, err
	}
	for i := range gaps {
		gaps[i].From = at.Add(-time.Second)
		gaps[i].To = at
	}
	c.prev = next
	if len(delta.ByOutbound) == 0 {
		return nil, gaps, nil
	}
	return &delta, gaps, nil
}
