package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

func setupAuthedServer(t *testing.T, cfg Config) (*sqlitestore.Store, http.Handler, *http.Cookie) {
	t.Helper()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-admin-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, cfg)

	loginReq := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"test-admin-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", loginRec.Code, loginRec.Body.String())
	}
	return store, handler, loginRec.Result().Cookies()[0]
}

func doAuthedJSON(handler http.Handler, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestDirectCollectorsRequireAuth(t *testing.T) {
	_, handler, _ := setupAuthedServer(t, Config{})
	endpoints := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/direct-collectors", ""},
		{http.MethodPost, "/v1/direct-collectors", `{}`},
		{http.MethodPost, "/v1/direct-collectors/test", `{}`},
		{http.MethodPut, "/v1/direct-collectors/1", `{}`},
		{http.MethodDelete, "/v1/direct-collectors/1", ""},
		{http.MethodPost, "/v1/direct-collectors/1/test", `{}`},
		{http.MethodPost, "/v1/direct-collectors/1/toggle", `{}`},
	}
	for _, ep := range endpoints {
		rec := doAuthedJSON(handler, nil, ep.method, ep.path, ep.body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s = %d, want 401", ep.method, ep.path, rec.Code)
		}
	}
}

func TestDirectCollectorsCRUDValidationAndProbeHTTP(t *testing.T) {
	var wsConnsHits int32
	mockMihomo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/connections" {
			atomic.AddInt32(&wsConnsHits, 1)
			http.Error(w, "unexpected /connections hit", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer syn-bearer-secret" && r.URL.Query().Get("token") != "syn-url-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"version":"v1.18.1-syn","meta":true}`))
		case "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"},"Proxy":{"name":"Proxy","type":"Selector","all":["HK-01"]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockMihomo.Close()

	store, handler, cookie := setupAuthedServer(t, Config{})

	// Empty GET returns non-null [] slice.
	list0 := doAuthedJSON(handler, cookie, http.MethodGet, "/v1/direct-collectors", "")
	if list0.Code != http.StatusOK || !strings.Contains(list0.Body.String(), `"collectors":[]`) {
		t.Fatalf("empty GET = %d %s", list0.Code, list0.Body.String())
	}

	// Bad payloads -> 400.
	badBodies := []string{
		`{`,
		`{"name":"","site_id":"site-a","node_id":"dc-1","url":"http://127.0.0.1:9090"}`,
		`{"name":"DC","site_id":"","node_id":"dc-1","url":"http://127.0.0.1:9090"}`,
		`{"name":"DC","site_id":"site-a","node_id":"","url":"http://127.0.0.1:9090"}`,
		`{"name":"DC","site_id":"site-a","node_id":"dc-1","url":"not-a-url"}`,
		`{"name":"DC","site_id":"site-a","node_id":"dc-1","url":"file:///etc/passwd"}`,
		`{"name":"DC","site_id":"site-a","node_id":"dc-1","url":"ftp://127.0.0.1:21"}`,
		`{"name":"DC","site_id":"site-a","node_id":"dc-1","url":"http://127.0.0.1:9090","poll_interval_ms":50}`,
		`{"name":"DC","site_id":"site-a","node_id":"dc-1","url":"http://127.0.0.1:9090","poll_interval_ms":999999}`,
	}
	for i, body := range badBodies {
		rec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bad body #%d = %d (%s), want 400", i, rec.Code, rec.Body.String())
		}
	}

	// Create with omitted site_id/node_id and secret + ?token=... URL.
	createRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors", `{
		"name":"EdgeRouter",
		"url":"`+mockMihomo.URL+`?token=syn-url-tok",
		"secret":"syn-bearer-secret",
		"enabled":false
	}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST create = %d %s", createRec.Code, createRec.Body.String())
	}
	if strings.Contains(createRec.Body.String(), "syn-bearer-secret") || strings.Contains(createRec.Body.String(), "syn-url-tok") {
		t.Fatalf("secret leaked in POST response: %s", createRec.Body.String())
	}
	var created struct {
		ID        int64  `json:"id"`
		SiteID    string `json:"site_id"`
		NodeID    string `json:"node_id"`
		HasSecret bool   `json:"has_secret"`
		Secret    string `json:"secret"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID <= 0 || created.SiteID != "default" || !strings.HasPrefix(created.NodeID, "dc-edgerouter-") || !created.HasSecret || created.Secret != "********" {
		t.Fatalf("unexpected created payload: %+v", created)
	}
	idStr := strconv.FormatInt(created.ID, 10)

	// Ad-hoc /test and saved /{id}/test work without hitting /connections.
	adhocBad := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/test", `{"url":"file:///etc/passwd"}`)
	if adhocBad.Code != http.StatusBadRequest {
		t.Fatalf("adhoc invalid URL = %d, want 400", adhocBad.Code)
	}
	adhocOK := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/test", `{"url":"`+mockMihomo.URL+`","secret":"syn-bearer-secret"}`)
	if adhocOK.Code != http.StatusOK || !strings.Contains(adhocOK.Body.String(), `"ok":true`) {
		t.Fatalf("adhoc test = %d %s", adhocOK.Code, adhocOK.Body.String())
	}

	byIDMissing := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/9999/test", `{}`)
	if byIDMissing.Code != http.StatusNotFound {
		t.Fatalf("missing id test = %d, want 404", byIDMissing.Code)
	}

	byIDOK := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/"+idStr+"/test", `{}`)
	if byIDOK.Code != http.StatusOK || !strings.Contains(byIDOK.Body.String(), `"ok":true`) || !strings.Contains(byIDOK.Body.String(), `"proxy_count":2`) {
		t.Fatalf("saved id test = %d %s", byIDOK.Code, byIDOK.Body.String())
	}
	if atomic.LoadInt32(&wsConnsHits) != 0 {
		t.Fatalf("/test must never hit /connections, got %d hits", wsConnsHits)
	}

	// Closed port /test returns 200 ok:false without leaking URL token or secret.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()
	closedTest := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/test", `{"url":"http://`+closedAddr+`?token=closed-tok-99","secret":"closed-sec-99"}`)
	if closedTest.Code != http.StatusOK || !strings.Contains(closedTest.Body.String(), `"ok":false`) {
		t.Fatalf("closed port test = %d %s", closedTest.Code, closedTest.Body.String())
	}
	if strings.Contains(closedTest.Body.String(), "closed-tok-99") || strings.Contains(closedTest.Body.String(), "closed-sec-99") {
		t.Fatalf("closed port test leaked secrets: %s", closedTest.Body.String())
	}

	// PUT omitting secret preserves it; clear_secret clears it.
	putPreserve := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/direct-collectors/"+idStr, `{"name":"EdgeRouter-2"}`)
	if putPreserve.Code != http.StatusOK || !strings.Contains(putPreserve.Body.String(), `"has_secret":true`) {
		t.Fatalf("PUT preserve secret = %d %s", putPreserve.Code, putPreserve.Body.String())
	}
	putClear := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/direct-collectors/"+idStr, `{"clear_secret":true}`)
	if putClear.Code != http.StatusOK || !strings.Contains(putClear.Body.String(), `"has_secret":false`) {
		t.Fatalf("PUT clear_secret = %d %s", putClear.Code, putClear.Body.String())
	}

	// Toggle flip & explicit enabled.
	tg1 := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/"+idStr+"/toggle", `{}`)
	if tg1.Code != http.StatusOK || !strings.Contains(tg1.Body.String(), `"enabled":true`) {
		t.Fatalf("toggle flip = %d %s", tg1.Code, tg1.Body.String())
	}
	tg2 := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/"+idStr+"/toggle", `{"enabled":false}`)
	if tg2.Code != http.StatusOK || !strings.Contains(tg2.Body.String(), `"enabled":false`) {
		t.Fatalf("toggle explicit false = %d %s", tg2.Code, tg2.Body.String())
	}

	// Delete -> 200, second delete -> 404.
	del1 := doAuthedJSON(handler, cookie, http.MethodDelete, "/v1/direct-collectors/"+idStr, "")
	if del1.Code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", del1.Code, del1.Body.String())
	}
	del2 := doAuthedJSON(handler, cookie, http.MethodDelete, "/v1/direct-collectors/"+idStr, "")
	if del2.Code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404", del2.Code)
	}

	// Closed DB -> 503.
	_ = store.Close()
	down := doAuthedJSON(handler, cookie, http.MethodGet, "/v1/direct-collectors", "")
	if down.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed DB GET = %d, want 503", down.Code)
	}
}

func TestDirectCollectorsNoSecretEditAndRedactedURLPreservation(t *testing.T) {
	mockMihomo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "syn-secret-99" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"version":"v1.18.1-syn","meta":true}`))
		case "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockMihomo.Close()

	_, handler, cookie := setupAuthedServer(t, Config{})

	// Create collector WITHOUT bearer secret, WITH ?token= in URL.
	createRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors", `{
		"name":"NoSecURLAuth",
		"url":"`+mockMihomo.URL+`?token=syn-secret-99",
		"enabled":false
	}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST create = %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(created.URL, "[redacted]") {
		t.Fatalf("expected redacted URL in response, got %q", created.URL)
	}
	idStr := strconv.FormatInt(created.ID, 10)

	// 1. PUT omitting url and secret on secret-less collector must succeed (200, not 503).
	putRename := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/direct-collectors/"+idStr, `{"name":"EditedNoSec"}`)
	if putRename.Code != http.StatusOK {
		t.Fatalf("PUT rename on secret-less collector = %d %s, want 200", putRename.Code, putRename.Body.String())
	}

	// 2. PUT sending unchanged redacted URL preserves underlying URL token.
	putRedacted := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/direct-collectors/"+idStr, `{"name":"EditedAgain","url":"`+created.URL+`"}`)
	if putRedacted.Code != http.StatusOK {
		t.Fatalf("PUT with unchanged redacted URL = %d %s, want 200", putRedacted.Code, putRedacted.Body.String())
	}

	// Verify saved test probe still authenticates with syn-secret-99!
	testRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/"+idStr+"/test", `{}`)
	if testRec.Code != http.StatusOK || !strings.Contains(testRec.Body.String(), `"ok":true`) {
		t.Fatalf("POST /{id}/test after redacted URL edit = %d %s", testRec.Code, testRec.Body.String())
	}

	// 3. PUT with a different URL containing literal [redacted] is rejected with 400.
	putTampered := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/direct-collectors/"+idStr, `{"url":"http://127.0.0.1:9999?token=[redacted]"}`)
	if putTampered.Code != http.StatusBadRequest {
		t.Fatalf("PUT with tampered [redacted] URL = %d %s, want 400", putTampered.Code, putTampered.Body.String())
	}
}

func TestDirectCollectorsMalformedURLDoesNotLeakSecrets(t *testing.T) {
	_, handler, cookie := setupAuthedServer(t, Config{})

	created := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors", `{
		"name":"SeedDC",
		"url":"http://127.0.0.1:9090",
		"enabled":false
	}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("seed POST = %d %s", created.Code, created.Body.String())
	}
	var seed struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &seed); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(seed.ID, 10)

	badURL := "wss://admin:LeakPass111@127.0.0.1:badport?token=LeakTok222&secret=LeakQSec333"
	secrets := []string{"LeakPass111", "LeakTok222", "LeakQSec333"}

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "POST create",
			method: http.MethodPost,
			path:   "/v1/direct-collectors",
			body:   `{"name":"Bad","url":"` + badURL + `"}`,
		},
		{
			name:   "PUT update",
			method: http.MethodPut,
			path:   "/v1/direct-collectors/" + idStr,
			body:   `{"url":"` + badURL + `"}`,
		},
		{
			name:   "POST adhoc test",
			method: http.MethodPost,
			path:   "/v1/direct-collectors/test",
			body:   `{"url":"` + badURL + `"}`,
		},
		{
			name:   "POST saved test",
			method: http.MethodPost,
			path:   "/v1/direct-collectors/" + idStr + "/test",
			body:   `{"url":"` + badURL + `"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doAuthedJSON(handler, cookie, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d (%s), want 400", tc.method, tc.path, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, sec := range secrets {
				if strings.Contains(body, sec) {
					t.Fatalf("SECRET LEAK in %s %s (400): found %q inside %q", tc.method, tc.path, sec, body)
				}
			}
		})
	}
}

func TestDirectCollectorsTestByIDClearSecretPreservesURLToken(t *testing.T) {
	var authHeaderCount int32
	var tokenHitCount int32
	var lastAuth atomic.Value
	lastAuth.Store("")

	mockMihomo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "syn-url-tok-clear" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&tokenHitCount, 1)
		auth := r.Header.Get("Authorization")
		if auth != "" {
			atomic.AddInt32(&authHeaderCount, 1)
		}
		lastAuth.Store(auth)
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"version":"v1.18.1-syn","meta":true}`))
		case "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockMihomo.Close()

	_, handler, cookie := setupAuthedServer(t, Config{})

	createRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors", `{
		"name":"ClearTestDC",
		"url":"`+mockMihomo.URL+`?token=syn-url-tok-clear",
		"secret":"syn-bearer-to-clear",
		"enabled":false
	}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST create = %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID        int64  `json:"id"`
		URL       string `json:"url"`
		HasSecret bool   `json:"has_secret"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.HasSecret || !strings.Contains(created.URL, "[redacted]") {
		t.Fatalf("unexpected created collector: %+v", created)
	}
	idStr := strconv.FormatInt(created.ID, 10)

	// Baseline probe without clear_secret sends stored Authorization header.
	baseRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/"+idStr+"/test", `{}`)
	if baseRec.Code != http.StatusOK || !strings.Contains(baseRec.Body.String(), `"ok":true`) {
		t.Fatalf("baseline /{id}/test = %d %s", baseRec.Code, baseRec.Body.String())
	}
	if got := lastAuth.Load().(string); got != "Bearer syn-bearer-to-clear" {
		t.Fatalf("baseline Authorization header = %q, want %q", got, "Bearer syn-bearer-to-clear")
	}

	// Probe with clear_secret:true uses stored unredacted URL token and sends NO Authorization header.
	atomic.StoreInt32(&authHeaderCount, 0)
	atomic.StoreInt32(&tokenHitCount, 0)
	clearRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/"+idStr+"/test", `{"clear_secret":true}`)
	if clearRec.Code != http.StatusOK || !strings.Contains(clearRec.Body.String(), `"ok":true`) {
		t.Fatalf("clear_secret /{id}/test = %d %s", clearRec.Code, clearRec.Body.String())
	}
	if atomic.LoadInt32(&tokenHitCount) == 0 {
		t.Fatalf("expected mockMihomo to receive stored URL token")
	}
	if atomic.LoadInt32(&authHeaderCount) != 0 {
		t.Fatalf("expected zero Authorization headers with clear_secret:true, got %d (last=%q)", atomic.LoadInt32(&authHeaderCount), lastAuth.Load().(string))
	}

	// Also works when passing the unchanged redacted URL alongside clear_secret:true.
	atomic.StoreInt32(&authHeaderCount, 0)
	atomic.StoreInt32(&tokenHitCount, 0)
	redactedClearRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/direct-collectors/"+idStr+"/test", `{"url":"`+created.URL+`","clear_secret":true}`)
	if redactedClearRec.Code != http.StatusOK || !strings.Contains(redactedClearRec.Body.String(), `"ok":true`) {
		t.Fatalf("redacted+clear_secret /{id}/test = %d %s", redactedClearRec.Code, redactedClearRec.Body.String())
	}
	if atomic.LoadInt32(&tokenHitCount) == 0 || atomic.LoadInt32(&authHeaderCount) != 0 {
		t.Fatalf("expected stored URL token and zero Authorization headers, tokenHits=%d authHits=%d", atomic.LoadInt32(&tokenHitCount), atomic.LoadInt32(&authHeaderCount))
	}

	// Non-mutating test probe must not clear the persisted secret before Save.
	listRec := doAuthedJSON(handler, cookie, http.MethodGet, "/v1/direct-collectors", "")
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"has_secret":true`) {
		t.Fatalf("expected stored secret to remain intact after test probe: %d %s", listRec.Code, listRec.Body.String())
	}
}
