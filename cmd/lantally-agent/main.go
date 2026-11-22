package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	ifacecollector "github.com/misakayyds/lantally/internal/collector/iface"
	mihomocollector "github.com/misakayyds/lantally/internal/collector/mihomo"
	nlbwmoncollector "github.com/misakayyds/lantally/internal/collector/nlbwmon"
	"github.com/misakayyds/lantally/internal/protocol"
	"github.com/misakayyds/lantally/internal/version"
)

const (
	defaultQueueCapacity         = 128
	defaultInterval              = 15 * time.Second
	defaultMihomoMetaSyncInterval = 5 * time.Minute
)

type Config struct {
	ServerURL  string
	SiteID     string
	NodeID     string
	TokenFile  string
	Interval   time.Duration
	Collectors struct {
		Iface   bool
		Mihomo  bool
		Nlbwmon bool
	}
	Mihomo struct {
		URL        string
		SecretFile string
	}
	Nlbwmon struct {
		Command string
	}
	Iface struct {
		ExcludePrefixes []string
		Include         []string
	}
}

type configFile struct {
	ServerURL  string `json:"server_url"`
	SiteID     string `json:"site_id"`
	NodeID     string `json:"node_id"`
	TokenFile  string `json:"token_file"`
	Interval   string `json:"interval"`
	Collectors struct {
		Iface   bool `json:"iface"`
		Mihomo  bool `json:"mihomo"`
		Nlbwmon bool `json:"nlbwmon"`
	} `json:"collectors"`
	Mihomo struct {
		URL        string `json:"url"`
		SecretFile string `json:"secret_file"`
	} `json:"mihomo"`
	Nlbwmon struct {
		Command string `json:"command"`
	} `json:"nlbwmon"`
	Iface struct {
		ExcludePrefixes []string `json:"exclude_prefixes"`
		Include         []string `json:"include"`
	} `json:"iface"`
}

func LoadConfig(filename string) (Config, error) {
	var raw configFile
	data, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Config{}, err
	}
	interval := defaultInterval
	if raw.Interval != "" {
		interval, err = time.ParseDuration(raw.Interval)
		if err != nil {
			return Config{}, fmt.Errorf("parse interval: %w", err)
		}
	}
	cfg := Config{
		ServerURL: raw.ServerURL,
		SiteID:    raw.SiteID,
		NodeID:    raw.NodeID,
		TokenFile: raw.TokenFile,
		Interval:  interval,
	}
	cfg.Collectors.Iface = raw.Collectors.Iface
	cfg.Collectors.Mihomo = raw.Collectors.Mihomo
	cfg.Collectors.Nlbwmon = raw.Collectors.Nlbwmon
	cfg.Mihomo.URL = strings.TrimSpace(raw.Mihomo.URL)
	cfg.Mihomo.SecretFile = strings.TrimSpace(raw.Mihomo.SecretFile)
	cfg.Nlbwmon.Command = strings.TrimSpace(raw.Nlbwmon.Command)
	if raw.Iface.ExcludePrefixes != nil {
		cfg.Iface.ExcludePrefixes = append([]string{}, raw.Iface.ExcludePrefixes...)
	}
	if len(raw.Iface.Include) > 0 {
		cfg.Iface.Include = append([]string(nil), raw.Iface.Include...)
	}
	if cfg.ServerURL == "" || cfg.SiteID == "" || cfg.NodeID == "" || cfg.TokenFile == "" {
		return Config{}, errors.New("server_url, site_id, node_id, and token_file are required")
	}
	if cfg.Collectors.Mihomo && cfg.Mihomo.URL == "" {
		return Config{}, errors.New("mihomo.url is required when collectors.mihomo is enabled")
	}
	if cfg.Collectors.Mihomo {
		if _, err := url.ParseRequestURI(cfg.Mihomo.URL); err != nil {
			return Config{}, fmt.Errorf("parse mihomo.url: %w", err)
		}
	}
	if cfg.Interval <= 0 {
		return Config{}, errors.New("interval must be positive")
	}
	if _, err := url.ParseRequestURI(cfg.ServerURL); err != nil {
		return Config{}, fmt.Errorf("parse server_url: %w", err)
	}
	return cfg, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("config contains trailing JSON")
		}
		return err
	}
	return nil
}

func loadToken(filename string) (string, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("token file must contain one full bearer token")
	}
	return token, nil
}

func loadSecret(filename string) (string, error) {
	if strings.TrimSpace(filename) == "" {
		return "", nil
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return "", errors.New("secret file must contain one line")
	}
	return secret, nil
}

type snapshot struct {
	interfaces []protocol.IfaceDelta
	devices    []protocol.DeviceDelta
	proxy      *protocol.ProxyDelta
	dimensions []protocol.DimensionDelta
	proxyNodes []string
	providers  []protocol.ProviderSnapshot
	gaps       []protocol.Gap
}

type collector interface {
	Capability() protocol.Capability
	Collect(context.Context, time.Time) (snapshot, error)
}

type namedCollector struct {
	name      string
	collector collector
}

type agent struct {
	config     Config
	bootID     string
	sequence   uint64
	collectors []namedCollector
	firstBatch bool
}

func newAgent(config Config, bootID string, available map[string]collector) *agent {
	a := &agent{config: config, bootID: bootID, firstBatch: true}
	for name, candidate := range available {
		switch name {
		case "iface":
			if !config.Collectors.Iface {
				continue
			}
		case "mihomo":
			if !config.Collectors.Mihomo {
				continue
			}
		case "nlbwmon":
			if !config.Collectors.Nlbwmon {
				continue
			}
		}
		a.collectors = append(a.collectors, namedCollector{name: name, collector: candidate})
	}
	return a
}

func newAgentSession(config Config, available map[string]collector) (*agent, error) {
	id, err := processSessionID()
	if err != nil {
		return nil, err
	}
	return newAgent(config, id, available), nil
}

type ifaceAdapter struct {
	inner *ifacecollector.Collector
}

func (a ifaceAdapter) Capability() protocol.Capability {
	return a.inner.Capability()
}

func (a ifaceAdapter) Collect(ctx context.Context, at time.Time) (snapshot, error) {
	deltas, gaps, err := a.inner.Collect(ctx, at)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{interfaces: deltas, gaps: gaps}, nil
}

type nlbwmonAdapter struct {
	inner *nlbwmoncollector.Collector
}

func (a nlbwmonAdapter) Capability() protocol.Capability {
	return a.inner.Capability()
}

func (a nlbwmonAdapter) Collect(ctx context.Context, at time.Time) (snapshot, error) {
	devices, gaps, err := a.inner.Collect(ctx, at)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{devices: devices, gaps: gaps}, nil
}

type mihomoAdapter struct {
	inner        *mihomocollector.Collector
	client       *mihomocollector.Client
	metaInterval time.Duration
	lastMetaSync time.Time
}

func (a *mihomoAdapter) Capability() protocol.Capability {
	return a.inner.Capability()
}

func (a *mihomoAdapter) effectiveMetaInterval() time.Duration {
	if a.metaInterval > 0 {
		return a.metaInterval
	}
	return defaultMihomoMetaSyncInterval
}

func (a *mihomoAdapter) Collect(ctx context.Context, at time.Time) (snapshot, error) {
	snap, err := a.inner.CollectSnapshot(ctx, at)
	if err != nil {
		return snapshot{}, err
	}
	var dims []protocol.DimensionDelta
	for _, d := range snap.Dimensions {
		if d.RxDelta == 0 && d.TxDelta == 0 {
			continue
		}
		dims = append(dims, protocol.DimensionDelta{
			Type:    protocol.DimensionType(d.Type),
			Key:     d.Key,
			RxDelta: d.RxDelta,
			TxDelta: d.TxDelta,
		})
	}

	var proxyNodes []string
	var protoProviders []protocol.ProviderSnapshot
	if a.client != nil && (a.lastMetaSync.IsZero() || at.Sub(a.lastMetaSync) >= a.effectiveMetaInterval()) {
		nodeSet := map[string]struct{}{}

		proxies, pErr := a.client.FetchProxies(ctx)
		if pErr != nil {
			log.Printf("mihomo /proxies sync skipped: %v", pErr)
		} else {
			for _, p := range proxies {
				if p.IsGroup {
					continue
				}
				if name := strings.TrimSpace(p.Name); name != "" && !isBuiltinProxyName(name) {
					nodeSet[name] = struct{}{}
				}
			}
		}

		providers, provErr := a.client.FetchProviders(ctx)
		if provErr != nil {
			log.Printf("mihomo /providers/proxies sync skipped: %v", provErr)
		} else {
			for _, p := range providers {
				snapProv := protocol.ProviderSnapshot{
					Name:        p.Name,
					VehicleType: p.VehicleType,
				}
				if p.SubscriptionInfo != nil {
					snapProv.Upload = p.SubscriptionInfo.Upload
					snapProv.Download = p.SubscriptionInfo.Download
					snapProv.Total = p.SubscriptionInfo.Total
					snapProv.Expire = p.SubscriptionInfo.Expire
				}
				if len(p.Proxies) > 0 {
					snapProv.Proxies = make([]string, 0, len(p.Proxies))
					for _, member := range p.Proxies {
						trimmed := strings.TrimSpace(member)
						if trimmed == "" {
							continue
						}
						snapProv.Proxies = append(snapProv.Proxies, trimmed)
						if !isBuiltinProxyName(trimmed) {
							nodeSet[trimmed] = struct{}{}
						}
					}
				}
				protoProviders = append(protoProviders, snapProv)
			}
		}

		if pErr == nil && (provErr == nil || isHTTP404(provErr)) {
			a.lastMetaSync = at
		}

		if len(nodeSet) > 0 {
			proxyNodes = make([]string, 0, len(nodeSet))
			for name := range nodeSet {
				proxyNodes = append(proxyNodes, name)
			}
			sort.Strings(proxyNodes)
		}
	}

	return snapshot{
		devices:    snap.Devices,
		proxy:      snap.Proxy,
		dimensions: dims,
		proxyNodes: proxyNodes,
		providers:  protoProviders,
		gaps:       snap.Gaps,
	}, nil
}

func isHTTP404(err error) bool {
	var httpErr *mihomocollector.HTTPStatusError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

func isBuiltinProxyName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL":
		return true
	default:
		return false
	}
}

func (a *agent) collect(ctx context.Context, at time.Time) protocol.Batch {
	a.sequence++
	batch := protocol.Batch{
		ProtocolVersion: 1,
		SiteID:          a.config.SiteID,
		NodeID:          a.config.NodeID,
		BootID:          a.bootID,
		Sequence:        a.sequence,
		SampledAt:       at.UTC(),
		IntervalMS:      int(a.config.Interval / time.Millisecond),
		Capabilities:    []protocol.Capability{},
		Interfaces:      []protocol.IfaceDelta{},
		Devices:         []protocol.DeviceDelta{},
		Metadata:        ifaceMetadata(a.config),
	}
	if a.firstBatch {
		batch.Gaps = append(batch.Gaps, protocol.Gap{
			Reason: protocol.GapReboot,
			From:   at,
			To:     at,
		})
		a.firstBatch = false
	}
	seenCapabilities := make(map[protocol.Capability]struct{})
	seenProxyNodes := make(map[string]struct{})
	for _, registered := range a.collectors {
		capability := registered.collector.Capability()
		if _, exists := seenCapabilities[capability]; !exists {
			batch.Capabilities = append(batch.Capabilities, capability)
			seenCapabilities[capability] = struct{}{}
		}
		snap, err := registered.collector.Collect(ctx, at)
		if err != nil {
			batch.Gaps = append(batch.Gaps, protocol.Gap{
				Reason: protocol.GapCollectorReset,
				From:   at.Add(-a.config.Interval),
				To:     at,
			})
			continue
		}
		batch.Interfaces = append(batch.Interfaces, snap.interfaces...)
		batch.Devices = append(batch.Devices, snap.devices...)
		if snap.proxy != nil && len(snap.proxy.ByOutbound) > 0 {
			if batch.Proxy == nil {
				copied := *snap.proxy
				batch.Proxy = &copied
			} else {
				batch.Proxy.ByOutbound = append(batch.Proxy.ByOutbound, snap.proxy.ByOutbound...)
			}
		}
		batch.Dimensions = append(batch.Dimensions, snap.dimensions...)
		for _, nodeName := range snap.proxyNodes {
			if _, ok := seenProxyNodes[nodeName]; !ok {
				seenProxyNodes[nodeName] = struct{}{}
				batch.ProxyNodes = append(batch.ProxyNodes, nodeName)
			}
		}
		batch.Providers = append(batch.Providers, snap.providers...)
		batch.Gaps = append(batch.Gaps, snap.gaps...)
	}
	for _, inc := range accounting.IfaceIncrements(batch.Interfaces) {
		if inc.Rx == 0 && inc.Tx == 0 {
			continue
		}
		batch.Dimensions = append(batch.Dimensions, protocol.DimensionDelta{
			Type:    protocol.DimIface,
			Key:     inc.Iface,
			RxDelta: inc.Rx,
			TxDelta: inc.Tx,
		})
	}
	return batch
}

type batchQueue struct {
	capacity int
	items    []protocol.Batch
	head     int
	size     int
}

func newBatchQueue(capacity int) *batchQueue {
	if capacity <= 0 {
		panic("batch queue capacity must be positive")
	}
	return &batchQueue{capacity: capacity, items: make([]protocol.Batch, capacity)}
}

func (q *batchQueue) Push(batch protocol.Batch) {
	var dropped protocol.Batch
	didDrop := false
	if q.size == q.capacity {
		dropped = q.items[q.head]
		didDrop = true
		q.items[q.head] = protocol.Batch{}
		q.head = (q.head + 1) % q.capacity
		q.size--
	}
	tail := (q.head + q.size) % q.capacity
	q.items[tail] = batch
	q.size++
	if didDrop {
		carryDroppedLossGaps(&q.items[q.head], dropped, batch.SampledAt)
	}
}

func carryDroppedLossGaps(survivor *protocol.Batch, dropped protocol.Batch, dropTo time.Time) {
	bufferFrom := dropped.SampledAt
	for _, gap := range dropped.Gaps {
		switch gap.Reason {
		case protocol.GapReboot:
			carryRebootGap(survivor, gap)
		case protocol.GapBufferDrop:
			if bufferFrom.IsZero() || gap.From.Before(bufferFrom) {
				bufferFrom = gap.From
			}
		}
	}
	if !bufferFrom.IsZero() {
		carryBufferDrop(survivor, bufferFrom, dropTo)
	}
}

func carryRebootGap(batch *protocol.Batch, reboot protocol.Gap) {
	for _, gap := range batch.Gaps {
		if gap.Reason == protocol.GapReboot {
			return
		}
	}
	batch.Gaps = append(batch.Gaps, reboot)
}

func carryBufferDrop(batch *protocol.Batch, from, to time.Time) {
	for i := range batch.Gaps {
		if batch.Gaps[i].Reason != protocol.GapBufferDrop {
			continue
		}
		if batch.Gaps[i].From.IsZero() || from.Before(batch.Gaps[i].From) {
			batch.Gaps[i].From = from
		}
		if to.After(batch.Gaps[i].To) {
			batch.Gaps[i].To = to
		}
		return
	}
	batch.Gaps = append(batch.Gaps, protocol.Gap{
		Reason: protocol.GapBufferDrop,
		From:   from,
		To:     to,
	})
}

func (q *batchQueue) Peek() protocol.Batch {
	if q.size == 0 {
		panic("peek empty batch queue")
	}
	return q.items[q.head]
}

func (q *batchQueue) Pop() {
	if q.size == 0 {
		panic("pop empty batch queue")
	}
	q.items[q.head] = protocol.Batch{}
	q.head = (q.head + 1) % q.capacity
	q.size--
}

func (q *batchQueue) Len() int {
	return q.size
}

type backoff struct {
	initial time.Duration
	maximum time.Duration
	current time.Duration
	random  func() float64
}

func newBackoff(initial, maximum time.Duration, random func() float64) *backoff {
	return &backoff{initial: initial, maximum: maximum, random: random}
}

func (b *backoff) Next() time.Duration {
	if b.current == 0 {
		b.current = b.initial
	} else if b.current < b.maximum {
		b.current *= 2
		if b.current > b.maximum {
			b.current = b.maximum
		}
	}
	jittered := time.Duration(float64(b.current) * (0.5 + b.random()))
	if jittered > b.maximum {
		return b.maximum
	}
	return jittered
}

func (b *backoff) Reset() {
	b.current = 0
}

type ingestHTTPError struct {
	statusCode int
	status     string
	body       string
}

func (e *ingestHTTPError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("ingest returned %s: %s", e.status, e.body)
	}
	return fmt.Sprintf("ingest returned %s", e.status)
}

func isPermanentIngestRejection(err error) bool {
	var httpErr *ingestHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	switch httpErr.statusCode {
	case http.StatusBadRequest,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func postBatch(ctx context.Context, client *http.Client, serverURL, token string, batch protocol.Batch) error {
	_, _, err := postBatchWithResponse(ctx, client, serverURL, token, batch)
	return err
}

func postBatchWithResponse(
	ctx context.Context,
	client *http.Client,
	serverURL, token string,
	batch protocol.Batch,
) (protocol.IngestResponse, bool, error) {
	payload, err := protocol.Encode(batch)
	if err != nil {
		return protocol.IngestResponse{}, false, err
	}
	endpoint, err := url.Parse(serverURL)
	if err != nil {
		return protocol.IngestResponse{}, false, err
	}
	endpoint.Path = path.Join(strings.TrimSuffix(endpoint.Path, "/"), "/v1/ingest")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return protocol.IngestResponse{}, false, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response, err := client.Do(request)
	if err != nil {
		return protocol.IngestResponse{}, false, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return protocol.IngestResponse{}, false, &ingestHTTPError{
			statusCode: response.StatusCode,
			status:     response.Status,
			body:       strings.TrimSpace(string(body)),
		}
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return protocol.IngestResponse{}, false, nil
	}
	var resp protocol.IngestResponse
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		log.Printf("decode ingest response warning: %v", err)
		return protocol.IngestResponse{}, false, nil
	}
	return resp, true, nil
}

func run(ctx context.Context, cfg Config, token string) error {
	available := map[string]collector{
		"iface": ifaceAdapter{inner: ifacecollector.NewCollector()},
	}
	var mihomoCol *mihomocollector.Collector
	if cfg.Collectors.Mihomo {
		secret, err := loadSecret(cfg.Mihomo.SecretFile)
		if err != nil {
			return err
		}
		mihomoClient := mihomocollector.NewClient(cfg.Mihomo.URL, secret, nil)
		mihomoCol = mihomocollector.NewCollector(mihomoClient)
		mihomoCol.Start(ctx)
		defer mihomoCol.Close()
		available["mihomo"] = &mihomoAdapter{
			inner:        mihomoCol,
			client:       mihomoClient,
			metaInterval: defaultMihomoMetaSyncInterval,
		}
	}
	if cfg.Collectors.Nlbwmon {
		command := cfg.Nlbwmon.Command
		if command == "" {
			command = "/usr/sbin/nlbw"
		}
		available["nlbwmon"] = nlbwmonAdapter{
			inner: nlbwmoncollector.NewCollector(nlbwmoncollector.CommandDump(command, "-c", "json")),
		}
	}
	a, err := newAgentSession(cfg, available)
	if err != nil {
		return err
	}
	queue := newBatchQueue(defaultQueueCapacity)
	client := &http.Client{Timeout: 30 * time.Second}
	retry := newBackoff(time.Second, time.Minute, mathrand.Float64)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	var retryTimer *time.Timer
	var retryC <-chan time.Time
	collectAndQueue := func(at time.Time) {
		queue.Push(a.collect(ctx, at))
	}
	deliver := func() {
		for queue.Len() > 0 {
			head := queue.Peek()
			resp, hasResp, err := postBatchWithResponse(ctx, client, cfg.ServerURL, token, head)
			if err != nil {
				if isPermanentIngestRejection(err) {
					log.Printf("ingest permanently rejected batch seq=%d; dropping from queue: %v", head.Sequence, err)
					queue.Pop()
					retry.Reset()
					continue
				}
				delay := retry.Next()
				log.Printf("ingest failed; retrying in %s: %v", delay, err)
				retryTimer = time.NewTimer(delay)
				retryC = retryTimer.C
				return
			}
			if mihomoCol != nil && hasResp {
				collectDomains := resp.CollectDomains != nil && *resp.CollectDomains
				mihomoCol.SetDomainOptions(collectDomains, resp.WatchDomains)
			}
			queue.Pop()
			retry.Reset()
		}
		retryC = nil
	}

	collectAndQueue(time.Now())
	deliver()
	for {
		select {
		case <-ctx.Done():
			if retryTimer != nil {
				retryTimer.Stop()
			}
			return ctx.Err()
		case at := <-ticker.C:
			collectAndQueue(at)
			if retryC == nil {
				deliver()
			}
		case <-retryC:
			retryC = nil
			deliver()
		}
	}
}

func processSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

func ifaceMetadata(cfg Config) json.RawMessage {
	if cfg.Iface.ExcludePrefixes == nil && len(cfg.Iface.Include) == 0 {
		return nil
	}
	payload := struct {
		IfaceExcludePrefixes *[]string `json:"iface_exclude_prefixes,omitempty"`
		IfaceInclude         []string  `json:"iface_include,omitempty"`
	}{
		IfaceInclude: cfg.Iface.Include,
	}
	if cfg.Iface.ExcludePrefixes != nil {
		prefixes := cfg.Iface.ExcludePrefixes
		payload.IfaceExcludePrefixes = &prefixes
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return raw
}

func main() {
	configPath := flag.String("config", "/etc/lantally-agent.json", "path to agent JSON config")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return
	}
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	token, err := loadToken(cfg.TokenFile)
	if err != nil {
		log.Fatal(err)
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if err := run(signalCtx, cfg, token); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
