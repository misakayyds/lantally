package direct

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

type syntheticMihomo struct {
	srv         *httptest.Server
	secret      string
	mu          sync.Mutex
	rejectWS    int
	connsJSON   string
	wsFrames    []string
	nonGetCount int32
	wsDialCount int32
	connGetHits int32
	wsConns     []*websocket.Conn
}

func newSyntheticMihomo(t *testing.T, secret string) *syntheticMihomo {
	t.Helper()
	m := &syntheticMihomo{
		secret:    secret,
		connsJSON: `{"connections":[{"id":"base","upload":10,"download":10,"chains":["DIRECT"]}]}`,
	}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			atomic.AddInt32(&m.nonGetCount, 1)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if m.secret != "" {
			if r.Header.Get("Authorization") != "Bearer "+m.secret && r.URL.Query().Get("token") != m.secret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"meta":true,"version":"v1.18.0-syn"}`))
		case "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"},"Proxy":{"name":"Proxy","type":"Selector","all":["HK-01"]}}}`))
		case "/providers/proxies":
			_, _ = w.Write([]byte(`{"providers":{"SynProvider":{"name":"SynProvider","vehicleType":"HTTP","subscriptionInfo":{"Upload":100,"Download":400,"Total":10000,"Expire":1893456000},"proxies":[{"name":"HK-01"}]}}}`))
		case "/connections":
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				m.mu.Lock()
				reject := m.rejectWS
				frames := append([]string(nil), m.wsFrames...)
				m.wsFrames = nil
				initial := m.connsJSON
				m.mu.Unlock()
				if reject != 0 {
					http.Error(w, "ws disabled", reject)
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				atomic.AddInt32(&m.wsDialCount, 1)
				m.mu.Lock()
				m.wsConns = append(m.wsConns, conn)
				m.mu.Unlock()

				if len(frames) == 0 {
					frames = []string{initial}
				}
				for i, f := range frames {
					if i > 0 {
						time.Sleep(40 * time.Millisecond)
					}
					if err := conn.Write(r.Context(), websocket.MessageText, []byte(f)); err != nil {
						_ = conn.CloseNow()
						return
					}
				}
				for {
					if _, _, err := conn.Read(r.Context()); err != nil {
						_ = conn.CloseNow()
						return
					}
				}
			}
			atomic.AddInt32(&m.connGetHits, 1)
			m.mu.Lock()
			payload := m.connsJSON
			m.mu.Unlock()
			_, _ = w.Write([]byte(payload))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		m.mu.Lock()
		for _, c := range m.wsConns {
			_ = c.CloseNow()
		}
		m.mu.Unlock()
		m.srv.Close()
	})
	return m
}

func (m *syntheticMihomo) setFrames(frames ...string) {
	m.mu.Lock()
	m.wsFrames = frames
	m.mu.Unlock()
}

func (m *syntheticMihomo) setConnsJSON(payload string) {
	m.mu.Lock()
	m.connsJSON = payload
	m.mu.Unlock()
}

func (m *syntheticMihomo) pushWS(ctx context.Context, payload string) {
	m.mu.Lock()
	m.connsJSON = payload
	conns := append([]*websocket.Conn(nil), m.wsConns...)
	m.mu.Unlock()
	for _, c := range conns {
		_ = c.Write(ctx, websocket.MessageText, []byte(payload))
	}
}

func openTempStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func strPtr(v string) *string { return &v }
func boolPtr(v bool) *bool    { return &v }
func intPtr(v int) *int       { return &v }

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func TestManagerWSIngestDomainToggleToggleResumeAndGoroutineCleanup(t *testing.T) {
	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	// Allow any transient background goroutines from previous tests to settle.
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}

	store := openTempStore(t)
	ctx := context.Background()

	const sec = "syn-dc-secret-42"
	const urlTok = "syn-url-tok-99"
	mh := newSyntheticMihomo(t, sec)
	time.Sleep(30 * time.Millisecond)
	baseGoroutines := runtime.NumGoroutine()
	mh.setFrames(
		`{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]}]}`,
		`{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},{"id":"c1","upload":300,"download":700,"chains":["HK-01","AI"],"metadata":{"sourceIP":"10.0.0.21","process":"codex","host":"off.example.com"}}]}`,
	)

	// ProbeConfig must not consume queued wsFrames.
	mgr := NewManager(store, Config{DefaultInterval: 250 * time.Millisecond})
	probe := mgr.ProbeConfig(ctx, mh.srv.URL+"?token="+urlTok, sec)
	if !probe.OK || probe.ProxyCount != 2 {
		t.Fatalf("ProbeConfig failed: %+v", probe)
	}
	if atomic.LoadInt32(&mh.wsDialCount) != 0 {
		t.Fatalf("ProbeConfig dialed WS unexpectedly")
	}

	// Bad secret probe redacts secrets.
	badProbe := mgr.ProbeConfig(ctx, mh.srv.URL+"?token="+urlTok, "wrong-secret-abc")
	if badProbe.OK || strings.Contains(badProbe.Error, urlTok) || strings.Contains(badProbe.Error, "wrong-secret-abc") {
		t.Fatalf("badProbe leaked secret or reported OK: %+v", badProbe)
	}

	dc, err := store.CreateDirectCollector(ctx, sqlitestore.DirectCollectorInput{
		Name:           strPtr("SynDC"),
		SiteID:         strPtr("site-syn"),
		NodeID:         strPtr("dc-syn-1"),
		URL:            strPtr(mh.srv.URL + "?token=" + urlTok),
		Secret:         strPtr(sec),
		Enabled:        boolPtr(true),
		CollectDomains: boolPtr(false),
		PollIntervalMS: intPtr(250),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 4*time.Second, func() bool {
		bd, err := store.LedgerBreakdown(ctx, time.Time{}, time.Time{}, "site-syn", "dc-syn-1")
		if err != nil {
			return false
		}
		return bd["proxy_raw"].Total == 1000
	})

	// Verify collect_domains=false kept domains empty.
	domSeries, err := store.TrafficSeries(ctx, sqlitestore.TrafficQuery{Group: "domain", SiteID: "site-syn", NodeID: "dc-syn-1"})
	if err != nil || len(domSeries.Totals) != 0 {
		t.Fatalf("expected 0 domains while collect_domains=false, got %+v err=%v", domSeries.Totals, err)
	}

	// Verify providers & proxy catalog synced.
	names, err := store.ListProxyNodeNames(ctx)
	if err != nil || len(names) == 0 || names[0] != "HK-01" {
		t.Fatalf("expected HK-01 in proxy catalog, got %+v err=%v", names, err)
	}
	provs, _, err := store.ListProviderStatuses(ctx, time.Now().UTC(), time.UTC)
	if err != nil || len(provs) != 1 || provs[0].Name != "SynProvider" {
		t.Fatalf("expected SynProvider synced, got %+v err=%v", provs, err)
	}

	// Live toggle collect_domains=true via SetNodeCollectDomains + RefreshPreferences.
	if err := store.SetNodeCollectDomains(ctx, "dc-syn-1", true); err != nil {
		t.Fatal(err)
	}
	mgr.RefreshPreferences(ctx)
	time.Sleep(100 * time.Millisecond)

	mh.pushWS(ctx, `{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},{"id":"c2","upload":200,"download":300,"chains":["HK-01","AI"],"metadata":{"host":"api.sub.example.org"}}]}`)
	waitFor(t, 4*time.Second, func() bool {
		domSeries, err := store.TrafficSeries(ctx, sqlitestore.TrafficQuery{Group: "domain", SiteID: "site-syn", NodeID: "dc-syn-1"})
		if err != nil {
			return false
		}
		return domSeries.Totals["example.org"] == 500
	})

	// Toggle off -> stops runner -> updates SetConns -> toggle on -> picks up c3 via preserved counters!
	if _, err := store.ToggleDirectCollector(ctx, dc.ID, boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SyncCollector(ctx, dc.ID); err != nil {
		t.Fatal(err)
	}

	mh.setConnsJSON(`{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},{"id":"c3","upload":150,"download":250,"chains":["HK-01","Proxy"]}]}`)
	if _, err := store.ToggleDirectCollector(ctx, dc.ID, boolPtr(true)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SyncCollector(ctx, dc.ID); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 4*time.Second, func() bool {
		bd, err := store.LedgerBreakdown(ctx, time.Time{}, time.Time{}, "site-syn", "dc-syn-1")
		if err != nil {
			return false
		}
		return bd["proxy_raw"].Total == 1900 // 1000 + 500 + 400
	})

	if atomic.LoadInt32(&mh.nonGetCount) != 0 {
		t.Fatalf("expected zero non-GET requests, got %d", mh.nonGetCount)
	}

	if err := mgr.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}

	// Verify zero goroutine leaks after shutdown.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > baseGoroutines {
		time.Sleep(25 * time.Millisecond)
	}
	if runtime.NumGoroutine() > baseGoroutines {
		buf := make([]byte, 64<<10)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutine leak: base=%d now=%d\n%s", baseGoroutines, runtime.NumGoroutine(), string(buf[:n]))
	}

	if strings.Contains(logBuf.String(), sec) || strings.Contains(logBuf.String(), urlTok) {
		t.Fatalf("secrets leaked in logs: %s", logBuf.String())
	}
	_ = json.Marshal
}

func TestManagerHTTPFallbackWhenWSRejected(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	mh := newSyntheticMihomo(t, "fb-sec")
	mh.rejectWS = http.StatusNotFound
	mh.setConnsJSON(`{"connections":[{"id":"b","upload":5,"download":5,"chains":["DIRECT"]}]}`)

	if _, err := store.CreateDirectCollector(ctx, sqlitestore.DirectCollectorInput{
		Name:           strPtr("FallbackDC"),
		SiteID:         strPtr("site-syn"),
		NodeID:         strPtr("dc-fb-1"),
		URL:            strPtr(mh.srv.URL),
		Secret:         strPtr("fb-sec"),
		Enabled:        boolPtr(true),
		PollIntervalMS: intPtr(250),
	}); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(store, Config{DefaultInterval: 250 * time.Millisecond})
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	waitFor(t, 4*time.Second, func() bool {
		return atomic.LoadInt32(&mh.connGetHits) >= 1
	})
	mh.setConnsJSON(`{"connections":[{"id":"b","upload":5,"download":5,"chains":["DIRECT"]},{"id":"fb1","upload":100,"download":400,"chains":["HK-01","Proxy"]}]}`)

	waitFor(t, 4*time.Second, func() bool {
		bd, err := store.LedgerBreakdown(ctx, time.Time{}, time.Time{}, "site-syn", "dc-fb-1")
		if err != nil {
			return false
		}
		return bd["proxy_raw"].Total == 500
	})
}

func TestManagerKeyMismatchRecoveryKeepsSiblingRunning(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "lantally.db")

	mh := newSyntheticMihomo(t, "")
	mh.setFrames(
		`{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]}]}`,
		`{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},{"id":"k1","upload":250,"download":350,"chains":["HK-01","Proxy"]}]}`,
	)

	t.Setenv("LANTALLY_SECRET_KEY", "key-alpha")
	s1, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	badDC, err := s1.CreateDirectCollector(ctx, sqlitestore.DirectCollectorInput{
		Name:           strPtr("BadKeyDC"),
		SiteID:         strPtr("site-syn"),
		NodeID:         strPtr("dc-bad-key"),
		URL:            strPtr("http://127.0.0.1:9090"),
		Secret:         strPtr("alpha-secret"),
		Enabled:        boolPtr(true),
		PollIntervalMS: intPtr(250),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.CreateDirectCollector(ctx, sqlitestore.DirectCollectorInput{
		Name:           strPtr("SiblingDC"),
		SiteID:         strPtr("site-syn"),
		NodeID:         strPtr("dc-sibling-ok"),
		URL:            strPtr(mh.srv.URL),
		Enabled:        boolPtr(true),
		PollIntervalMS: intPtr(250),
	}); err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	t.Setenv("LANTALLY_SECRET_KEY", "key-beta")
	s2, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	mgr := NewManager(s2, Config{DefaultInterval: 250 * time.Millisecond})
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("DirectManager.Start failed on key mismatch: %v", err)
	}
	defer mgr.Close()

	gotBad, err := s2.GetDirectCollector(ctx, badDC.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBad.LastError, "key mismatch") {
		t.Fatalf("expected key mismatch in last_error, got %q", gotBad.LastError)
	}

	waitFor(t, 4*time.Second, func() bool {
		bd, err := s2.LedgerBreakdown(ctx, time.Time{}, time.Time{}, "site-syn", "dc-sibling-ok")
		if err != nil {
			return false
		}
		return bd["proxy_raw"].Total == 600
	})
}

func TestManagerShutdownTimeoutFollowedByCloseWaitsForRunners(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	mh := newSyntheticMihomo(t, "")
	if _, err := store.CreateDirectCollector(ctx, sqlitestore.DirectCollectorInput{
		Name:           strPtr("ShutdownDC"),
		SiteID:         strPtr("site-syn"),
		NodeID:         strPtr("dc-sd-1"),
		URL:            strPtr(mh.srv.URL),
		Enabled:        boolPtr(true),
		PollIntervalMS: intPtr(250),
	}); err != nil {
		t.Fatal(err)
	}

	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
	time.Sleep(30 * time.Millisecond)
	baseGoroutines := runtime.NumGoroutine()

	mgr := NewManager(store, Config{DefaultInterval: 250 * time.Millisecond})
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return atomic.LoadInt32(&mh.wsDialCount) >= 1
	})

	// Trigger Shutdown with an already-canceled context so it times out immediately.
	timedOutCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := mgr.Shutdown(timedOutCtx); err == nil {
		t.Fatalf("expected Shutdown to return context error")
	}

	// Subsequent Close() must still wait for all runner goroutines to exit.
	mgr.Close()
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > baseGoroutines {
		time.Sleep(25 * time.Millisecond)
	}
	if runtime.NumGoroutine() > baseGoroutines {
		buf := make([]byte, 64<<10)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutine leak after Shutdown timeout + Close: base=%d now=%d\n%s", baseGoroutines, runtime.NumGoroutine(), string(buf[:n]))
	}
}

func TestManagerReloadSyncModifyDeleteRemoveAndProbeSaved(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	const sec = "dc-sec-99"
	const tok = "dc-tok-99"
	mh1 := newSyntheticMihomo(t, sec)
	mh1.setFrames(`{"connections":[{"id":"b1","upload":1,"download":1,"chains":["DIRECT"]}]}`)
	mh2 := newSyntheticMihomo(t, sec)
	mh2.setFrames(
		`{"connections":[{"id":"b2","upload":1,"download":1,"chains":["DIRECT"]}]}`,
		`{"connections":[{"id":"b2","upload":1,"download":1,"chains":["DIRECT"]},{"id":"c1","upload":100,"download":200,"chains":["HK-01","Proxy"]}]}`,
	)

	dc, err := store.CreateDirectCollector(ctx, sqlitestore.DirectCollectorInput{
		Name:           strPtr("LifecycleDC"),
		SiteID:         strPtr("site-syn"),
		NodeID:         strPtr("dc-lc-1"),
		URL:            strPtr(mh1.srv.URL + "?token=" + tok),
		Secret:         strPtr(sec),
		Enabled:        boolPtr(true),
		PollIntervalMS: intPtr(200),
	})
	if err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(store, Config{DefaultInterval: 200 * time.Millisecond})

	// ProbeSaved on valid and missing IDs.
	probeOK, err := mgr.ProbeSaved(ctx, dc.ID)
	if err != nil || !probeOK.OK {
		t.Fatalf("ProbeSaved valid = %+v err=%v", probeOK, err)
	}
	if _, err := mgr.ProbeSaved(ctx, 99999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ProbeSaved missing = %v, want sql.ErrNoRows", err)
	}

	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	waitFor(t, 3*time.Second, func() bool {
		return atomic.LoadInt32(&mh1.wsDialCount) >= 1
	})

	// Update URL + poll_interval_ms -> SyncCollector restarts runner against mh2.
	if _, err := store.UpdateDirectCollector(ctx, dc.ID, sqlitestore.DirectCollectorInput{
		URL:            strPtr(mh2.srv.URL + "?token=" + tok),
		PollIntervalMS: intPtr(250),
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SyncCollector(ctx, dc.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 4*time.Second, func() bool {
		bd, err := store.LedgerBreakdown(ctx, time.Time{}, time.Time{}, "site-syn", "dc-lc-1")
		return err == nil && bd["proxy_raw"].Total == 300
	})

	// Bad secret triggers recordError -> redacts secret and URL token in last_error.
	if _, err := store.UpdateDirectCollector(ctx, dc.ID, sqlitestore.DirectCollectorInput{
		Secret: strPtr("wrong-sec-abc"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 4*time.Second, func() bool {
		got, err := store.GetDirectCollector(ctx, dc.ID)
		return err == nil && got.LastError != ""
	})
	gotErrDC, err := store.GetDirectCollector(ctx, dc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotErrDC.LastError, "wrong-sec-abc") || strings.Contains(gotErrDC.LastError, tok) {
		t.Fatalf("last_error leaked credentials: %q", gotErrDC.LastError)
	}

	// Delete collector -> SyncCollector stops runner cleanly; RemoveCollector handles missing ID.
	if err := store.DeleteDirectCollector(ctx, dc.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SyncCollector(ctx, dc.ID); err != nil {
		t.Fatalf("SyncCollector after delete failed: %v", err)
	}
	mgr.RemoveCollector(dc.ID)
}
