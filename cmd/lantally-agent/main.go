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
	"path"
	"strings"
	"time"

	ifacecollector "github.com/misakayyds/lantally/internal/collector/iface"
	mihomocollector "github.com/misakayyds/lantally/internal/collector/mihomo"
	"github.com/misakayyds/lantally/internal/protocol"
)

const (
	defaultQueueCapacity = 128
	defaultInterval      = 15 * time.Second
)

type Config struct {
	ServerURL  string
	SiteID     string
	NodeID     string
	TokenFile  string
	Interval   time.Duration
	Collectors struct {
		Iface  bool
		Mihomo bool
	}
	Mihomo struct {
		URL        string
		SecretFile string
	}
}

type configFile struct {
	ServerURL  string `json:"server_url"`
	SiteID     string `json:"site_id"`
	NodeID     string `json:"node_id"`
	TokenFile  string `json:"token_file"`
	Interval   string `json:"interval"`
	Collectors struct {
		Iface  bool `json:"iface"`
		Mihomo bool `json:"mihomo"`
	} `json:"collectors"`
	Mihomo struct {
		URL        string `json:"url"`
		SecretFile string `json:"secret_file"`
	} `json:"mihomo"`
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
	cfg.Mihomo.URL = strings.TrimSpace(raw.Mihomo.URL)
	cfg.Mihomo.SecretFile = strings.TrimSpace(raw.Mihomo.SecretFile)
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

type mihomoAdapter struct {
	inner *mihomocollector.Collector
}

func (a mihomoAdapter) Capability() protocol.Capability {
	return a.inner.Capability()
}

func (a mihomoAdapter) Collect(ctx context.Context, at time.Time) (snapshot, error) {
	proxy, gaps, err := a.inner.Collect(ctx, at)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{proxy: proxy, gaps: gaps}, nil
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
		batch.Gaps = append(batch.Gaps, snap.gaps...)
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

func postBatch(ctx context.Context, client *http.Client, serverURL, token string, batch protocol.Batch) error {
	payload, err := protocol.Encode(batch)
	if err != nil {
		return err
	}
	endpoint, err := url.Parse(serverURL)
	if err != nil {
		return err
	}
	endpoint.Path = path.Join(strings.TrimSuffix(endpoint.Path, "/"), "/v1/ingest")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ingest returned %s", response.Status)
	}
	return nil
}

func run(ctx context.Context, cfg Config, token string) error {
	available := map[string]collector{
		"iface": ifaceAdapter{inner: ifacecollector.NewCollector()},
	}
	if cfg.Collectors.Mihomo {
		secret, err := loadSecret(cfg.Mihomo.SecretFile)
		if err != nil {
			return err
		}
		available["mihomo"] = mihomoAdapter{
			inner: mihomocollector.NewCollector(mihomocollector.NewClient(cfg.Mihomo.URL, secret, nil)),
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
			if err := postBatch(ctx, client, cfg.ServerURL, token, queue.Peek()); err != nil {
				delay := retry.Next()
				log.Printf("ingest failed; retrying in %s: %v", delay, err)
				retryTimer = time.NewTimer(delay)
				retryC = retryTimer.C
				return
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

func main() {
	configPath := flag.String("config", "/etc/lantally-agent.json", "path to agent JSON config")
	flag.Parse()
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	token, err := loadToken(cfg.TokenFile)
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), cfg, token); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
