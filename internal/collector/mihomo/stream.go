package mihomo

import (
	"context"
	mathrand "math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/misakayyds/lantally/internal/protocol"
)

// Collector tracks Mihomo connection deltas via WebSocket streaming (with HTTP fallback)
// or synchronous HTTP polling when Start has not been called.
type Collector struct {
	client *Client

	mu             sync.Mutex
	opts           Options
	prev           map[string]ConnCounters
	pending        parsedFrame
	started        bool
	firstFrameDone bool
	firstFrameCh   chan struct{}
	wasConnected   bool
	lastFrameAt    time.Time
	disconnectedAt time.Time
	streamErr      error
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

func NewCollector(client *Client) *Collector {
	return NewCollectorWithOptions(client, Options{})
}

func NewCollectorWithOptions(client *Client, opts Options) *Collector {
	opts.compiledWatch = compileWatchDomains(opts.WatchDomains)
	var prev map[string]ConnCounters
	if opts.InitialCounters != nil {
		prev = make(map[string]ConnCounters, len(opts.InitialCounters))
		for k, v := range opts.InitialCounters {
			prev[k] = v
		}
		opts.InitialCounters = nil
	}
	return &Collector{
		client:       client,
		opts:         opts,
		prev:         prev,
		pending:      newParsedFrame(),
		firstFrameCh: make(chan struct{}),
	}
}

func (c *Collector) Capability() protocol.Capability {
	return protocol.CapMihomo
}

// SetOptions updates metadata extraction options safely for future frames.
func (c *Collector) SetOptions(opts Options) {
	opts.compiledWatch = compileWatchDomains(opts.WatchDomains)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opts = opts
}

// SetDomainOptions updates only domain-collection preferences safely in memory
// without overwriting stream timing or DomainTopN settings.
func (c *Collector) SetDomainOptions(collectDomains bool, watchDomains []string) {
	var copied []string
	if len(watchDomains) > 0 {
		copied = make([]string, len(watchDomains))
		copy(copied, watchDomains)
	}
	compiled := compileWatchDomains(copied)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opts.CollectDomains = collectDomains
	c.opts.WatchDomains = copied
	c.opts.compiledWatch = compiled
}

// Start launches the background WebSocket /connections stream (falling back to
// high-frequency HTTP polling when the server rejects WebSocket upgrades).
func (c *Collector) Start(ctx context.Context) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.started = true
	c.cancel = cancel
	c.wg.Add(1)
	c.mu.Unlock()

	go func() {
		defer c.wg.Done()
		c.streamLoop(runCtx)
	}()
}

// Close stops the background stream started by Start and waits for exit.
func (c *Collector) Close() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
}

// Status reports whether the collector has connected and its latest stream error.
func (c *Collector) Status() (connected bool, lastErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wasConnected, c.streamErr
}

// LastFrameAt reports the timestamp of the most recently ingested connections frame.
func (c *Collector) LastFrameAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastFrameAt
}

// SnapshotCounters returns a copy of the tracked connection counters baseline.
func (c *Collector) SnapshotCounters() map[string]ConnCounters {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prev == nil {
		return nil
	}
	out := make(map[string]ConnCounters, len(c.prev))
	for k, v := range c.prev {
		out[k] = v
	}
	return out
}

// PollOnce performs a single read-only HTTP GET /connections fetch and ingests it.
func (c *Collector) PollOnce(ctx context.Context) error {
	raw, err := c.client.FetchConnections(ctx)
	if err != nil {
		return err
	}
	return c.ingestFrame(raw, time.Now().UTC())
}

// Collect returns proxy, device, and gap deltas since the previous Collect call.
func (c *Collector) Collect(
	ctx context.Context,
	at time.Time,
) (*protocol.ProxyDelta, []protocol.DeviceDelta, []protocol.Gap, error) {
	snap, err := c.CollectSnapshot(ctx, at)
	if err != nil {
		return nil, nil, nil, err
	}
	return snap.Proxy, snap.Devices, snap.Gaps, nil
}

// CollectSnapshot returns the full Snapshot (including group/process/domain dimensions)
// accumulated since the previous CollectSnapshot call.
func (c *Collector) CollectSnapshot(ctx context.Context, at time.Time) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	c.mu.Lock()
	started := c.started
	readyCh := c.firstFrameCh
	ready := c.firstFrameDone
	c.mu.Unlock()

	if !started {
		raw, err := c.client.FetchConnections(ctx)
		if err != nil {
			return Snapshot{}, err
		}
		if err := c.ingestFrame(raw, at); err != nil {
			return Snapshot{}, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.drainPendingLocked(), nil
	}

	if !ready {
		waitTimer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-readyCh:
		case <-waitTimer.C:
		case <-ctx.Done():
			waitTimer.Stop()
			return Snapshot{}, ctx.Err()
		}
		waitTimer.Stop()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending.isEmpty() && c.streamErr != nil {
		return Snapshot{}, c.streamErr
	}
	return c.drainPendingLocked(), nil
}

func (c *Collector) drainPendingLocked() Snapshot {
	snap := c.pending.toSnapshot(c.opts.DomainTopN)
	c.pending = newParsedFrame()
	return snap
}

func (c *Collector) ingestFrame(raw []byte, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	parsed, next, err := parseConnectionsFrame(c.prev, raw, c.opts, at)
	if err != nil {
		return err
	}
	c.prev = next
	if c.wasConnected && !c.disconnectedAt.IsZero() {
		c.pending.gaps = append(c.pending.gaps, protocol.Gap{
			Reason: protocol.GapCollectorReset,
			From:   c.disconnectedAt,
			To:     at,
		})
	}
	c.disconnectedAt = time.Time{}
	c.wasConnected = true
	c.lastFrameAt = at
	c.streamErr = nil
	c.pending.mergeFrom(parsed)
	c.signalFirstFrameLocked()
	return nil
}

func (c *Collector) markDisconnected(err error, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamErr = err
	if c.wasConnected && c.disconnectedAt.IsZero() {
		c.disconnectedAt = at
	}
	c.signalFirstFrameLocked()
}

func (c *Collector) recordStreamError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamErr = err
	c.signalFirstFrameLocked()
}

func (c *Collector) signalFirstFrameLocked() {
	if !c.firstFrameDone {
		c.firstFrameDone = true
		close(c.firstFrameCh)
	}
}

func (c *Collector) dialTimeout() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.opts.DialTimeout > 0 {
		return c.opts.DialTimeout
	}
	return defaultDialTimeout
}

func (c *Collector) wsIdleTimeout() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.opts.WSIdleTimeout > 0 {
		return c.opts.WSIdleTimeout
	}
	return defaultWSIdleTimeout
}

func (c *Collector) fallbackPollInterval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.opts.FallbackPollInterval > 0 {
		return c.opts.FallbackPollInterval
	}
	return defaultFallbackPollInterval
}

func (c *Collector) reconnectBaseBackoff() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.opts.ReconnectBackoff > 0 {
		return c.opts.ReconnectBackoff
	}
	return defaultReconnectBackoff
}

func (c *Collector) streamLoop(ctx context.Context) {
	wsURL, err := c.client.wsConnectionsURL()
	if err != nil {
		c.markDisconnected(err, time.Now().UTC())
		return
	}

	baseBackoff := c.reconnectBaseBackoff()
	curBackoff := baseBackoff

	for ctx.Err() == nil {
		var dialClient http.Client
		if c.client.http != nil {
			dialClient = *c.client.http
		}
		// Never pass a non-zero http.Client.Timeout to coder/websocket.Dial;
		// enforce only handshake timeout via dialCtx.
		dialClient.Timeout = 0

		var headers http.Header
		if c.client.secret != "" {
			headers = http.Header{"Authorization": []string{"Bearer " + c.client.secret}}
		}

		dialCtx, dialCancel := context.WithTimeout(ctx, c.dialTimeout())
		conn, resp, dialErr := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
			HTTPClient: &dialClient,
			HTTPHeader: headers,
		})
		dialCancel()

		if dialErr != nil {
			if isUnsupportedWSResponse(resp) {
				c.runHTTPPollFallback(ctx)
				return
			}
			c.markDisconnected(dialErr, time.Now().UTC())
			if !waitBackoff(ctx, curBackoff) {
				return
			}
			curBackoff = nextBackoff(curBackoff, maxReconnectBackoff)
			continue
		}

		conn.SetReadLimit(maxResponseBytes)

		c.serveWSConn(ctx, conn, baseBackoff, &curBackoff)

		if ctx.Err() != nil {
			return
		}
		if !waitBackoff(ctx, curBackoff) {
			return
		}
		curBackoff = nextBackoff(curBackoff, maxReconnectBackoff)
	}
}

func (c *Collector) serveWSConn(
	ctx context.Context,
	conn *websocket.Conn,
	baseBackoff time.Duration,
	curBackoff *time.Duration,
) {
	defer conn.CloseNow()
	for ctx.Err() == nil {
		readCtx, readCancel := context.WithTimeout(ctx, c.wsIdleTimeout())
		_, raw, readErr := conn.Read(readCtx)
		readCancel()
		if readErr != nil {
			if ctx.Err() != nil {
				return
			}
			c.markDisconnected(readErr, time.Now().UTC())
			return
		}
		*curBackoff = baseBackoff
		if ingestErr := c.ingestFrame(raw, time.Now().UTC()); ingestErr != nil {
			c.recordStreamError(ingestErr)
		}
	}
}

func (c *Collector) runHTTPPollFallback(ctx context.Context) {
	pollOnce := func() {
		raw, err := c.client.FetchConnections(ctx)
		if err != nil {
			if ctx.Err() == nil {
				c.markDisconnected(err, time.Now().UTC())
			}
			return
		}
		if err := c.ingestFrame(raw, time.Now().UTC()); err != nil {
			c.recordStreamError(err)
		}
	}

	pollOnce()
	ticker := time.NewTicker(c.fallbackPollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollOnce()
		}
	}
}

func isUnsupportedWSResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusOK,
		http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func waitBackoff(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	jittered := time.Duration(float64(d) * (0.5 + mathrand.Float64()*0.5))
	timer := time.NewTimer(jittered)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	if cur <= 0 {
		return time.Second
	}
	cur *= 2
	if cur > max {
		return max
	}
	return cur
}
