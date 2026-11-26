package direct

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/misakayyds/lantally/internal/alert"
	"github.com/misakayyds/lantally/internal/collector/mihomo"
	"github.com/misakayyds/lantally/internal/ingest"
	"github.com/misakayyds/lantally/internal/protocol"
	"github.com/misakayyds/lantally/internal/redact"
	"github.com/misakayyds/lantally/internal/store/metrics"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

const (
	defaultPollInterval     = 15 * time.Second
	defaultMetaSyncInterval = 5 * time.Minute
	defaultDomainTopN       = 50
	defaultProbeTimeout     = 5 * time.Second
)

type Config struct {
	Metrics          *metrics.Writer
	DefaultInterval  time.Duration
	MetaSyncInterval time.Duration
	DomainTopN       int
	ProbeTimeout     time.Duration
	HTTPClient       *http.Client
}

type savedCounterState struct {
	rawURL string
	nodeID string
	prev   map[string]mihomo.ConnCounters
}

// Manager supervises server-side direct Mihomo collectors and feeds their
// snapshots through the unified ingest pipeline.
type Manager struct {
	store  *sqlitestore.Store
	ingest *ingest.Handler
	cfg    Config

	syncMu        sync.Mutex
	mu            sync.Mutex
	started       bool
	closed        bool
	rootCtx       context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	runners       map[int64]*collectorRunner
	savedCounters map[int64]savedCounterState
}

func NewManager(store *sqlitestore.Store, cfg Config) *Manager {
	if cfg.DefaultInterval <= 0 {
		cfg.DefaultInterval = defaultPollInterval
	}
	if cfg.MetaSyncInterval <= 0 {
		cfg.MetaSyncInterval = defaultMetaSyncInterval
	}
	if cfg.DomainTopN <= 0 {
		cfg.DomainTopN = defaultDomainTopN
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = defaultProbeTimeout
	}
	h := ingest.NewHandler(store)
	if cfg.Metrics != nil {
		h.SetMetrics(cfg.Metrics)
	}
	return &Manager{
		store:         store,
		ingest:        h,
		cfg:           cfg,
		runners:       make(map[int64]*collectorRunner),
		savedCounters: make(map[int64]savedCounterState),
	}
}

// Probe performs a strictly read-only HTTP GET probe against rawURL and redacts
// any secret material from error output.
func Probe(ctx context.Context, rawURL, secret string, httpClient *http.Client) mihomo.ProbeResult {
	res := mihomo.NewClient(rawURL, secret, httpClient).ProbeHTTP(ctx)
	if res.Error != "" {
		res.Error = sanitizeErrorString(res.Error, secret, rawURL)
	}
	return res
}

func (m *Manager) ProbeConfig(ctx context.Context, rawURL, secret string) mihomo.ProbeResult {
	probeCtx, cancel := context.WithTimeout(ctx, m.cfg.ProbeTimeout)
	defer cancel()
	return Probe(probeCtx, rawURL, secret, m.cfg.HTTPClient)
}

func (m *Manager) ProbeSaved(ctx context.Context, id int64) (mihomo.ProbeResult, error) {
	dc, err := m.store.GetDirectCollectorRuntime(ctx, id)
	if err != nil {
		return mihomo.ProbeResult{}, err
	}
	return m.ProbeConfig(ctx, dc.RawURL, dc.SecretPlain), nil
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("direct collector manager is closed")
	}
	if m.started {
		m.mu.Unlock()
		return nil
	}
	rootCtx, cancel := context.WithCancel(ctx)
	m.rootCtx = rootCtx
	m.cancel = cancel
	m.started = true
	m.mu.Unlock()

	return m.Reload(rootCtx)
}

func (m *Manager) Reload(ctx context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	m.mu.Lock()
	if !m.started || m.closed {
		m.mu.Unlock()
		return nil
	}
	rootCtx := m.rootCtx
	m.mu.Unlock()

	enabled, err := m.store.ListEnabledDirectCollectorsRuntime(ctx)
	if err != nil {
		return err
	}
	desired := make(map[int64]sqlitestore.DirectCollector, len(enabled))
	for _, dc := range enabled {
		desired[dc.ID] = dc
	}

	var toStop []*collectorRunner
	var toStart []sqlitestore.DirectCollector

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	for id, r := range m.runners {
		dc, ok := desired[id]
		if !ok || !r.matchesConfig(dc) {
			delete(m.runners, id)
			toStop = append(toStop, r)
		}
	}
	m.mu.Unlock()

	for _, r := range toStop {
		r.stop()
		m.mu.Lock()
		if dc, ok := desired[r.id]; ok && dc.RawURL == r.rawURL && dc.NodeID == r.nodeID {
			if r.finalPrev != nil {
				m.savedCounters[r.id] = savedCounterState{
					rawURL: r.rawURL,
					nodeID: r.nodeID,
					prev:   r.finalPrev,
				}
			}
		} else if !ok {
			// Check whether collector still exists (disabled) or was deleted.
			if r.finalPrev != nil {
				m.savedCounters[r.id] = savedCounterState{
					rawURL: r.rawURL,
					nodeID: r.nodeID,
					prev:   r.finalPrev,
				}
			}
		} else {
			delete(m.savedCounters, r.id)
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	for _, dc := range enabled {
		if r, exists := m.runners[dc.ID]; exists {
			r.setCollectDomains(dc.CollectDomains)
			r.triggerRefresh()
			continue
		}
		toStart = append(toStart, dc)
	}
	m.mu.Unlock()

	for _, dc := range toStart {
		watchDomains, _ := m.store.WatchDomainsForNode(rootCtx, dc.NodeID)
		m.startRunner(rootCtx, dc, watchDomains)
	}
	return nil
}

func (m *Manager) SyncCollector(ctx context.Context, id int64) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	m.mu.Lock()
	if !m.started || m.closed {
		m.mu.Unlock()
		return nil
	}
	rootCtx := m.rootCtx
	existing := m.runners[id]
	m.mu.Unlock()

	dc, err := m.store.GetDirectCollectorRuntime(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		if existing != nil {
			m.mu.Lock()
			if cur := m.runners[id]; cur == existing {
				delete(m.runners, id)
			}
			m.mu.Unlock()
			existing.stop()
		}
		m.mu.Lock()
		delete(m.savedCounters, id)
		m.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}

	if !dc.Enabled {
		if existing != nil {
			m.mu.Lock()
			if cur := m.runners[id]; cur == existing {
				delete(m.runners, id)
			}
			m.mu.Unlock()
			existing.stop()
			m.mu.Lock()
			if existing.finalPrev != nil {
				m.savedCounters[id] = savedCounterState{
					rawURL: existing.rawURL,
					nodeID: existing.nodeID,
					prev:   existing.finalPrev,
				}
			}
			m.mu.Unlock()
		}
		return nil
	}

	if existing != nil && existing.matchesConfig(dc) {
		existing.setCollectDomains(dc.CollectDomains)
		existing.triggerRefresh()
		return nil
	}

	if existing != nil {
		m.mu.Lock()
		if cur := m.runners[id]; cur == existing {
			delete(m.runners, id)
		}
		m.mu.Unlock()
		existing.stop()
		m.mu.Lock()
		if existing.rawURL == dc.RawURL && existing.nodeID == dc.NodeID && existing.finalPrev != nil {
			m.savedCounters[id] = savedCounterState{
				rawURL: existing.rawURL,
				nodeID: existing.nodeID,
				prev:   existing.finalPrev,
			}
		} else {
			delete(m.savedCounters, id)
		}
		m.mu.Unlock()
	}

	watchDomains, _ := m.store.WatchDomainsForNode(rootCtx, dc.NodeID)
	m.startRunner(rootCtx, dc, watchDomains)
	return nil
}

func (m *Manager) RemoveCollector(id int64) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	m.mu.Lock()
	r := m.runners[id]
	if r != nil {
		delete(m.runners, id)
	}
	delete(m.savedCounters, id)
	m.mu.Unlock()

	if r != nil {
		r.stop()
	}
}

func (m *Manager) RefreshPreferences(_ context.Context) {
	m.mu.Lock()
	list := make([]*collectorRunner, 0, len(m.runners))
	for _, r := range m.runners {
		list = append(list, r)
	}
	m.mu.Unlock()
	for _, r := range list {
		r.triggerRefresh()
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	alreadyClosed := m.closed
	m.closed = true
	cancel := m.cancel
	m.cancel = nil
	if !alreadyClosed {
		m.runners = make(map[int64]*collectorRunner)
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

func (m *Manager) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Close()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) startRunner(rootCtx context.Context, dc sqlitestore.DirectCollector, watchDomains []string) {
	interval := m.cfg.DefaultInterval
	if dc.PollIntervalMS > 0 {
		interval = time.Duration(dc.PollIntervalMS) * time.Millisecond
	}

	m.mu.Lock()
	if m.closed || rootCtx.Err() != nil {
		m.mu.Unlock()
		return
	}
	var initialPrev map[string]mihomo.ConnCounters
	if saved, ok := m.savedCounters[dc.ID]; ok && saved.rawURL == dc.RawURL && saved.nodeID == dc.NodeID && saved.prev != nil {
		initialPrev = make(map[string]mihomo.ConnCounters, len(saved.prev))
		for k, v := range saved.prev {
			initialPrev[k] = v
		}
	}
	runCtx, cancel := context.WithCancel(rootCtx)
	client := mihomo.NewClient(dc.RawURL, dc.SecretPlain, m.cfg.HTTPClient)
	col := mihomo.NewCollectorWithOptions(client, mihomo.Options{
		CollectDomains:  dc.CollectDomains,
		WatchDomains:    watchDomains,
		DomainTopN:      m.cfg.DomainTopN,
		InitialCounters: initialPrev,
	})
	r := &collectorRunner{
		manager:            m,
		id:                 dc.ID,
		siteID:             dc.SiteID,
		nodeID:             dc.NodeID,
		rawURL:             dc.RawURL,
		secret:             dc.SecretPlain,
		pollInterval:       interval,
		metaInterval:       m.cfg.MetaSyncInterval,
		bootID:             newRandomUUID(),
		client:             client,
		col:                col,
		cancel:             cancel,
		done:               make(chan struct{}),
		refreshCh:          make(chan struct{}, 1),
		collectDomains:     dc.CollectDomains,
		lastCollectDomains: dc.CollectDomains,
		lastWatchDomains:   watchDomains,
	}
	m.runners[dc.ID] = r
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		r.run(runCtx)
	}()
}

type collectorRunner struct {
	manager      *Manager
	id           int64
	siteID       string
	nodeID       string
	rawURL       string
	secret       string
	pollInterval time.Duration
	metaInterval time.Duration
	bootID       string

	client *mihomo.Client
	col    *mihomo.Collector
	cancel context.CancelFunc
	done   chan struct{}

	refreshCh chan struct{}

	prefMu             sync.Mutex
	collectDomains     bool
	lastCollectDomains bool
	lastWatchDomains   []string

	seq          uint64
	lastMetaSync time.Time
	finalPrev    map[string]mihomo.ConnCounters
}

func (r *collectorRunner) matchesConfig(dc sqlitestore.DirectCollector) bool {
	interval := r.manager.cfg.DefaultInterval
	if dc.PollIntervalMS > 0 {
		interval = time.Duration(dc.PollIntervalMS) * time.Millisecond
	}
	return r.siteID == dc.SiteID &&
		r.nodeID == dc.NodeID &&
		r.rawURL == dc.RawURL &&
		r.secret == dc.SecretPlain &&
		r.pollInterval == interval
}

func (r *collectorRunner) setCollectDomains(v bool) {
	r.prefMu.Lock()
	r.collectDomains = v
	r.prefMu.Unlock()
}

func (r *collectorRunner) triggerRefresh() {
	select {
	case r.refreshCh <- struct{}{}:
	default:
	}
}

func (r *collectorRunner) stop() {
	r.cancel()
	<-r.done
}

func (r *collectorRunner) run(ctx context.Context) {
	defer close(r.done)
	r.col.Start(ctx)
	defer func() {
		r.col.Close()
		r.finalPrev = r.col.SnapshotCounters()
	}()

	warmupDelay := 250 * time.Millisecond
	if half := r.pollInterval / 2; half > 0 && half < warmupDelay {
		warmupDelay = half
	}
	warmup := time.NewTimer(warmupDelay)
	defer warmup.Stop()

	var postWarmup *time.Timer
	var postWarmupC <-chan time.Time
	if r.pollInterval > 2*time.Second {
		postWarmup = time.NewTimer(1500 * time.Millisecond)
		defer postWarmup.Stop()
		postWarmupC = postWarmup.C
	}

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-warmup.C:
			r.tick(ctx, false)
		case <-postWarmupC:
			postWarmupC = nil
			r.tick(ctx, true)
		case <-ticker.C:
			r.tick(ctx, false)
		case <-r.refreshCh:
			r.tick(ctx, false)
		}
	}
}

func (r *collectorRunner) tick(ctx context.Context, quickQuietCheck bool) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()

	// Refresh domain collection & watchlist preferences live.
	r.prefMu.Lock()
	fallbackCollect := r.collectDomains
	r.prefMu.Unlock()

	collectDomains, watchDomains, prefErr := r.manager.store.NodeDomainPreferences(ctx, r.nodeID)
	if prefErr != nil {
		collectDomains = fallbackCollect
		watchDomains, _ = r.manager.store.WatchDomainsForNode(ctx, r.nodeID)
	}
	r.prefMu.Lock()
	changed := collectDomains != r.lastCollectDomains || !stringSlicesEqual(watchDomains, r.lastWatchDomains)
	if changed {
		r.collectDomains = collectDomains
		r.lastCollectDomains = collectDomains
		r.lastWatchDomains = watchDomains
	}
	r.prefMu.Unlock()
	if changed {
		r.col.SetDomainOptions(collectDomains, watchDomains)
	}

	// If WS has been quiet for longer than quietThreshold (e.g. controller/mock does not push idle frames),
	// perform a single read-only GET /connections refresh without disturbing active streaming.
	connected, streamErr := r.col.Status()
	lastFrameAt := r.col.LastFrameAt()
	quietThreshold := r.pollInterval
	if quietThreshold > 5*time.Second {
		quietThreshold = 5 * time.Second
	}
	if quickQuietCheck && quietThreshold > time.Second {
		quietThreshold = time.Second
	}
	var pollErr error
	if connected && !lastFrameAt.IsZero() && now.Sub(lastFrameAt) >= quietThreshold {
		if err := r.col.PollOnce(ctx); err != nil && ctx.Err() == nil {
			pollErr = err
		}
	}

	snap, snapErr := r.col.CollectSnapshot(ctx, now)
	if ctx.Err() != nil {
		return
	}

	var (
		proxyNodes     []string
		protoProviders []protocol.ProviderSnapshot
		pErr, provErr  error
		metaAttempted  bool
	)
	if r.lastMetaSync.IsZero() || now.Sub(r.lastMetaSync) >= r.metaInterval {
		metaAttempted = true
		nodeSet := map[string]struct{}{}

		var proxies []mihomo.ProxyInfo
		proxies, pErr = r.client.FetchProxies(ctx)
		if pErr == nil {
			for _, p := range proxies {
				if p.IsGroup {
					continue
				}
				if name := strings.TrimSpace(p.Name); name != "" && !isBuiltinProxyName(name) {
					nodeSet[name] = struct{}{}
				}
			}
		}

		var providers []mihomo.ProviderSnapshot
		providers, provErr = r.client.FetchProviders(ctx)
		if provErr == nil {
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

		if pErr == nil && provErr == nil {
			r.lastMetaSync = now
		}
		if len(nodeSet) > 0 {
			proxyNodes = make([]string, 0, len(nodeSet))
			for name := range nodeSet {
				proxyNodes = append(proxyNodes, name)
			}
			sort.Strings(proxyNodes)
		}
	}

	connected, streamErr = r.col.Status()
	if snapErr != nil {
		r.recordError(ctx, snapErr)
		return
	}
	if pollErr != nil && snap.Proxy == nil && len(snap.Dimensions) == 0 {
		r.recordError(ctx, pollErr)
		return
	}
	if !connected && metaAttempted && (pErr != nil || provErr != nil) {
		errToReport := streamErr
		if errToReport == nil {
			errToReport = pErr
		}
		if errToReport == nil {
			errToReport = provErr
		}
		r.recordError(ctx, errToReport)
		return
	}

	dims := make([]protocol.DimensionDelta, 0, len(snap.Dimensions))
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

	if snap.Proxy != nil || len(snap.Devices) > 0 || len(dims) > 0 || len(proxyNodes) > 0 || len(protoProviders) > 0 || len(snap.Gaps) > 0 {
		r.seq++
		batch := protocol.Batch{
			ProtocolVersion: 1,
			SiteID:          r.siteID,
			NodeID:          r.nodeID,
			BootID:          r.bootID,
			Sequence:        r.seq,
			SampledAt:       now,
			IntervalMS:      int(r.pollInterval.Milliseconds()),
			Capabilities:    []protocol.Capability{protocol.CapMihomo},
			Interfaces:      []protocol.IfaceDelta{},
			Devices:         snap.Devices,
			Proxy:           snap.Proxy,
			Dimensions:      dims,
			ProxyNodes:      proxyNodes,
			Providers:       protoProviders,
			Gaps:            snap.Gaps,
		}
		if _, err := r.manager.ingest.IngestBatch(ctx, batch); err != nil {
			if ctx.Err() == nil {
				r.recordError(ctx, err)
			}
			return
		}
		for _, gap := range snap.Gaps {
			rec, fired := alert.EvalReset(false, string(gap.Reason))
			if !fired {
				continue
			}
			rec.SiteID = r.siteID
			rec.NodeID = r.nodeID
			rec.ObservedAt = now
			rec.Fingerprint = "reset|" + r.nodeID + "|" + string(gap.Reason) + "|" + gap.From.UTC().Format(time.RFC3339)
			_ = r.manager.store.RaiseAlert(ctx, rec)
		}
	}

	metaHealthy := metaAttempted && pErr == nil && provErr == nil
	if connected || metaHealthy {
		_ = r.manager.store.UpdateDirectCollectorStatus(ctx, r.id, now, "")
	} else if streamErr != nil {
		r.recordError(ctx, streamErr)
	}
}

func (r *collectorRunner) recordError(ctx context.Context, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	redacted := sanitizeErrorString(err.Error(), r.secret, r.rawURL)
	log.Printf("direct collector %d (%s): %s", r.id, r.nodeID, redacted)
	_ = r.manager.store.UpdateDirectCollectorStatus(ctx, r.id, time.Time{}, redacted)
}

func sanitizeErrorString(msg, secret, rawURL string) string {
	cleaned := redact.Secrets(msg, secret)
	if rawURL != "" && strings.Contains(cleaned, rawURL) {
		cleaned = strings.ReplaceAll(cleaned, rawURL, redact.URL(rawURL))
	}
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > 512 {
		cleaned = cleaned[:512]
	}
	return cleaned
}

func isBuiltinProxyName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL":
		return true
	default:
		return false
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newRandomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("dc-%d", time.Now().UTC().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
