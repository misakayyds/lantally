package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/misakayyds/lantally/internal/collector/direct"
	"github.com/misakayyds/lantally/internal/protocol"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

type intMockMihomo struct {
	srv         *httptest.Server
	secret      string
	mu          sync.Mutex
	rejectWS    int
	connsJSON   string
	wsFrames    []string
	wsConns     []*websocket.Conn
	nonGetCount int32
}

func newIntMockMihomo(t *testing.T, secret string) *intMockMihomo {
	t.Helper()
	m := &intMockMihomo{
		secret:    secret,
		connsJSON: `{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]}]}`,
	}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			atomic.AddInt32(&m.nonGetCount, 1)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if m.secret != "" && r.Header.Get("Authorization") != "Bearer "+m.secret && r.URL.Query().Get("token") != m.secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"meta":true,"version":"v1.18.0-int"}`))
		case "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"HK-IEPL-01":{"name":"HK-IEPL-01","type":"Shadowsocks"},"Proxy":{"name":"Proxy","type":"Selector","all":["HK-IEPL-01"]}}}`))
		case "/providers/proxies":
			_, _ = w.Write([]byte(`{"providers":{"IntProvider":{"name":"IntProvider","vehicleType":"HTTP","subscriptionInfo":{"Upload":400,"Download":1600,"Total":100000,"Expire":1893456000},"proxies":[{"name":"HK-IEPL-01"}]}}}`))
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

func (m *intMockMihomo) pushWS(ctx context.Context, payload string) {
	m.mu.Lock()
	m.connsJSON = payload
	conns := append([]*websocket.Conn(nil), m.wsConns...)
	m.mu.Unlock()
	for _, c := range conns {
		_ = c.Write(ctx, websocket.MessageText, []byte(payload))
	}
}

func waitServerUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("condition not satisfied within %v", timeout)
}

func TestDirectCollectorEndToEndPipelineRestartAndAgentCoexistence(t *testing.T) {
	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	dbPath := filepath.Join(t.TempDir(), "lantally.db")
	store1, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store1.Close() })
	if err := store1.CreateAdminCredential("int-admin-pass"); err != nil {
		t.Fatal(err)
	}
	mgr1 := direct.NewManager(store1, direct.Config{DefaultInterval: 250 * time.Millisecond})
	t.Cleanup(func() { mgr1.Close() })
	ctx1, cancel1 := context.WithCancel(context.Background())
	t.Cleanup(cancel1)
	if err := mgr1.Start(ctx1); err != nil {
		t.Fatal(err)
	}
	handler1 := Routes(store1, Config{Direct: mgr1})

	loginRec := doAuthedJSON(handler1, nil, http.MethodPost, "/v1/login", `{"password":"int-admin-pass"}`)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login = %d", loginRec.Code)
	}
	cookie := loginRec.Result().Cookies()[0]

	const sec = "int-dc-secret-77"
	mh := newIntMockMihomo(t, sec)
	mh.wsFrames = []string{
		`{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]}]}`,
		`{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},{"id":"c1","upload":300,"download":700,"chains":["HK-IEPL-01","AI"],"metadata":{"sourceIP":"10.0.0.21","process":"codex","host":"hidden.synthetic.test"}}]}`,
	}

	// Enroll a remote agent as well.
	enrollRec := doAuthedJSON(handler1, cookie, http.MethodPost, "/v1/enroll", `{"site_id":"site-int","node_id":"remote-agent-1"}`)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", enrollRec.Code, enrollRec.Body.String())
	}
	var enrollResp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(enrollRec.Body.Bytes(), &enrollResp); err != nil {
		t.Fatal(err)
	}

	// Create direct collector (initially collect_domains=false).
	createRec := doAuthedJSON(handler1, cookie, http.MethodPost, "/v1/direct-collectors", `{
		"name":"IntDC","site_id":"site-int","node_id":"dc-int-1",
		"url":"`+mh.srv.URL+`","secret":"`+sec+`","enabled":true,"collect_domains":false,"poll_interval_ms":250
	}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create direct collector = %d %s", createRec.Code, createRec.Body.String())
	}

	// Wait for traffic, device, preview, and provider sync.
	waitServerUntil(t, 5*time.Second, func() bool {
		ov := doAuthedJSON(handler1, cookie, http.MethodGet, "/v1/overview", "")
		return strings.Contains(ov.Body.String(), `"proxy_raw":1000`)
	})

	// Check group/process dimensions & privacy default (no domains).
	grp := doAuthedJSON(handler1, cookie, http.MethodGet, "/v1/traffic?group=group", "")
	if !strings.Contains(grp.Body.String(), `"AI":1000`) {
		t.Fatalf("expected group AI=1000, got %s", grp.Body.String())
	}
	proc := doAuthedJSON(handler1, cookie, http.MethodGet, "/v1/traffic?group=process", "")
	if !strings.Contains(proc.Body.String(), `"codex":1000`) {
		t.Fatalf("expected process codex=1000, got %s", proc.Body.String())
	}
	dom0 := doAuthedJSON(handler1, cookie, http.MethodGet, "/v1/traffic?group=domain", "")
	if strings.Contains(dom0.Body.String(), "synthetic.test") {
		t.Fatalf("domain leaked while collect_domains=false: %s", dom0.Body.String())
	}

	// Device 10.0.0.21 accounted.
	devs := doAuthedJSON(handler1, cookie, http.MethodGet, "/v1/devices", "")
	if !strings.Contains(devs.Body.String(), "10.0.0.21") {
		t.Fatalf("expected device 10.0.0.21 in /v1/devices: %s", devs.Body.String())
	}

	// Proxy catalog & provider reconciliation.
	prev := doAuthedJSON(handler1, cookie, http.MethodPost, "/v1/proxy/rules/preview", `{}`)
	if !strings.Contains(prev.Body.String(), "HK-IEPL-01") {
		t.Fatalf("expected HK-IEPL-01 in preview: %s", prev.Body.String())
	}
	provs := doAuthedJSON(handler1, cookie, http.MethodGet, "/v1/proxy/providers", "")
	if !strings.Contains(provs.Body.String(), "IntProvider") {
		t.Fatalf("expected IntProvider in /v1/proxy/providers: %s", provs.Body.String())
	}

	// Toggle collect_domains=true via PUT /v1/nodes/{id}/config -> live refresh.
	nodeCfg := doAuthedJSON(handler1, cookie, http.MethodPut, "/v1/nodes/dc-int-1/config", `{"collect_domains":true}`)
	if nodeCfg.Code != http.StatusOK {
		t.Fatalf("PUT node config = %d %s", nodeCfg.Code, nodeCfg.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	mh.pushWS(context.Background(), `{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},{"id":"c2","upload":150,"download":350,"chains":["HK-IEPL-01","AI"],"metadata":{"host":"api.sub.synthetic.test"}}]}`)
	waitServerUntil(t, 5*time.Second, func() bool {
		dom1 := doAuthedJSON(handler1, cookie, http.MethodGet, "/v1/traffic?group=domain", "")
		return strings.Contains(dom1.Body.String(), `"synthetic.test":500`)
	})

	// Send a remote agent batch and check /v1/nodes shows both modes.
	rawBatch, err := protocol.Encode(protocol.Batch{
		ProtocolVersion: 1,
		SiteID:          "site-int",
		NodeID:          "remote-agent-1",
		BootID:          "boot-remote-1",
		Sequence:        1,
		SampledAt:       time.Now().UTC(),
		IntervalMS:      1000,
		Capabilities:    []protocol.Capability{protocol.CapIface},
		Interfaces:      []protocol.IfaceDelta{{Name: "eth0", RxDelta: 500, TxDelta: 200}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	_, _ = gz.Write(rawBatch)
	_ = gz.Close()
	// protocol.Encode already gzips if needed? Verify: check rawBatch[0]==0x1f.
	var ingestBody []byte
	if len(rawBatch) >= 2 && rawBatch[0] == 0x1f && rawBatch[1] == 0x8b {
		ingestBody = rawBatch
	} else {
		ingestBody = gzBuf.Bytes()
	}
	inReq := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(ingestBody))
	inReq.Header.Set("Content-Type", "application/json")
	inReq.Header.Set("Content-Encoding", "gzip")
	inReq.Header.Set("Authorization", "Bearer "+enrollResp.Token)
	inRec := httptest.NewRecorder()
	handler1.ServeHTTP(inRec, inReq)
	if inRec.Code != http.StatusOK {
		t.Fatalf("remote ingest = %d %s", inRec.Code, inRec.Body.String())
	}

	nodesRec := doAuthedJSON(handler1, cookie, http.MethodGet, "/v1/nodes", "")
	if nodesRec.Code != http.StatusOK ||
		!strings.Contains(nodesRec.Body.String(), `"id":"dc-int-1"`) ||
		!strings.Contains(nodesRec.Body.String(), `"mode":"direct"`) ||
		!strings.Contains(nodesRec.Body.String(), `"id":"remote-agent-1"`) ||
		!strings.Contains(nodesRec.Body.String(), `"mode":"agent"`) {
		t.Fatalf("unexpected /v1/nodes response: %s", nodesRec.Body.String())
	}

	// Shut down server/manager 1 and verify restart resumes persisted collector automatically.
	cancel1()
	mgr1.Close()
	_ = store1.Close()

	mh.mu.Lock()
	mh.connsJSON = `{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},{"id":"c3","upload":200,"download":300,"chains":["HK-IEPL-01","AI"]}]}`
	mh.mu.Unlock()

	store2, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	mgr2 := direct.NewManager(store2, direct.Config{DefaultInterval: 250 * time.Millisecond})
	if err := mgr2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr2.Close()
	handler2 := Routes(store2, Config{Direct: mgr2})

	login2 := doAuthedJSON(handler2, nil, http.MethodPost, "/v1/login", `{"password":"int-admin-pass"}`)
	cookie2 := login2.Result().Cookies()[0]
	// Note: restart establishes fresh baseline on [b, c3], then push c4 to prove resumed live collection.
	time.Sleep(150 * time.Millisecond)
	mh.pushWS(context.Background(), `{"connections":[{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},{"id":"c3","upload":200,"download":300,"chains":["HK-IEPL-01","AI"]},{"id":"c4","upload":100,"download":200,"chains":["HK-IEPL-01","AI"]}]}`)

	waitServerUntil(t, 5*time.Second, func() bool {
		ov := doAuthedJSON(handler2, cookie2, http.MethodGet, "/v1/overview", "")
		return strings.Contains(ov.Body.String(), `"proxy_raw":1800`) // 1000 + 500 + 300
	})

	if atomic.LoadInt32(&mh.nonGetCount) != 0 {
		t.Fatalf("expected 0 non-GET requests, got %d", mh.nonGetCount)
	}
	if strings.Contains(logBuf.String(), sec) {
		t.Fatalf("secret leaked in server logs: %s", logBuf.String())
	}
}
