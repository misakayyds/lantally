package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/collector/sim"
	"github.com/misakayyds/lantally/internal/enroll"
	"github.com/misakayyds/lantally/internal/protocol"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

func TestEnsureFirstRunAdminUsesBootstrapEnv(t *testing.T) {
	t.Setenv("LANTALLY_BOOTSTRAP_ADMIN_PASSWORD", "bootstrap-test-password")
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := EnsureFirstRunAdmin(store, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthenticateAdmin("bootstrap-test-password"); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollRequiresLogin(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := Routes(store, Config{})

	req := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"site_id":"site-a","node_id":"node-a"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("enroll without login = %d, want 401", rec.Code)
	}
}

func TestEnrollIssuesTokenOnceAndListsNode(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})

	loginReq := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"test-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]

	enrollReq := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"site_id":"site-a","node_id":"proxy-20"}`))
	enrollReq.Header.Set("Content-Type", "application/json")
	enrollReq.AddCookie(cookie)
	enrollRec := httptest.NewRecorder()
	handler.ServeHTTP(enrollRec, enrollReq)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", enrollRec.Code, enrollRec.Body.String())
	}

	var issued struct {
		Token  string `json:"token"`
		NodeID string `json:"node_id"`
		SiteID string `json:"site_id"`
	}
	if err := json.NewDecoder(bytes.NewReader(enrollRec.Body.Bytes())).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if _, ok := enroll.CredentialID(issued.Token); !ok || issued.NodeID != "proxy-20" || issued.SiteID != "site-a" {
		t.Fatalf("issued = %+v", issued)
	}

	nodesReq := httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
	nodesReq.AddCookie(cookie)
	nodesRec := httptest.NewRecorder()
	handler.ServeHTTP(nodesRec, nodesReq)
	if nodesRec.Code != http.StatusOK {
		t.Fatalf("nodes = %d %s", nodesRec.Code, nodesRec.Body.String())
	}
	if !strings.Contains(nodesRec.Body.String(), `"proxy-20"`) {
		t.Fatalf("nodes missing enrolled id: %s", nodesRec.Body.String())
	}
}

func TestOverviewShowsLedgerBytesAfterIngest(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})

	loginReq := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"test-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]

	enrollReq := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"site_id":"sim-site","node_id":"sim-node"}`))
	enrollReq.Header.Set("Content-Type", "application/json")
	enrollReq.AddCookie(cookie)
	enrollRec := httptest.NewRecorder()
	handler.ServeHTTP(enrollRec, enrollReq)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", enrollRec.Code, enrollRec.Body.String())
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(bytes.NewReader(enrollRec.Body.Bytes())).Decode(&issued); err != nil {
		t.Fatal(err)
	}

	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}
	ingestReq := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw))
	ingestReq.Header.Set("Authorization", "Bearer "+issued.Token)
	ingestReq.Header.Set("Content-Encoding", "gzip")
	ingestReq.Header.Set("Content-Type", "application/json")
	ingestRec := httptest.NewRecorder()
	handler.ServeHTTP(ingestRec, ingestReq)
	if ingestRec.Code != http.StatusOK {
		t.Fatalf("ingest = %d %s", ingestRec.Code, ingestRec.Body.String())
	}

	overviewReq := httptest.NewRequest(http.MethodGet, "/v1/overview", nil)
	overviewReq.AddCookie(cookie)
	overviewRec := httptest.NewRecorder()
	handler.ServeHTTP(overviewRec, overviewReq)
	if overviewRec.Code != http.StatusOK {
		t.Fatalf("overview = %d %s", overviewRec.Code, overviewRec.Body.String())
	}
	body := overviewRec.Body.String()
	if !strings.Contains(body, `"total":1536`) {
		t.Fatalf("overview missing iface total: %s", body)
	}
	if !strings.Contains(body, `"bucket_seconds":1800`) || !strings.Contains(body, `"sim-node"`) {
		t.Fatalf("overview missing 72h traffic series: %s", body)
	}

	devicesReq := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
	devicesReq.AddCookie(cookie)
	devicesRec := httptest.NewRecorder()
	handler.ServeHTTP(devicesRec, devicesReq)
	if devicesRec.Code != http.StatusOK {
		t.Fatalf("devices = %d %s", devicesRec.Code, devicesRec.Body.String())
	}
	var devicesBody struct {
		Devices []sqlitestore.DeviceLedger `json:"devices"`
	}
	if err := json.NewDecoder(bytes.NewReader(devicesRec.Body.Bytes())).Decode(&devicesBody); err != nil {
		t.Fatal(err)
	}
	if len(devicesBody.Devices) != 1 {
		t.Fatalf("devices = %+v", devicesBody.Devices)
	}
}

func TestTrafficEndpointHonorsRangeAndGroup(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})

	unauth := httptest.NewRequest(http.MethodGet, "/v1/traffic", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauth)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("traffic without login = %d", unauthRec.Code)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"test-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	cookie := loginRec.Result().Cookies()[0]

	enrollReq := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"site_id":"sim-site","node_id":"sim-node"}`))
	enrollReq.Header.Set("Content-Type", "application/json")
	enrollReq.AddCookie(cookie)
	enrollRec := httptest.NewRecorder()
	handler.ServeHTTP(enrollRec, enrollReq)
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(bytes.NewReader(enrollRec.Body.Bytes())).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}
	ingestReq := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw))
	ingestReq.Header.Set("Authorization", "Bearer "+issued.Token)
	ingestReq.Header.Set("Content-Encoding", "gzip")
	ingestReq.Header.Set("Content-Type", "application/json")
	if rec := httptest.NewRecorder(); true {
		handler.ServeHTTP(rec, ingestReq)
		if rec.Code != http.StatusOK {
			t.Fatalf("ingest = %d %s", rec.Code, rec.Body.String())
		}
	}

	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)
	path := "/v1/traffic?from=" + from.Format(time.RFC3339) + "&to=" + to.Format(time.RFC3339) + "&group=node&class=total"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("traffic = %d %s", rec.Code, rec.Body.String())
	}
	var series sqlitestore.TrafficSeries
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&series); err != nil {
		t.Fatal(err)
	}
	if series.BucketSeconds != 1800 {
		t.Fatalf("bucket = %d, want 1800 for 24h", series.BucketSeconds)
	}
	if series.Totals["sim-node"] != 1536 {
		t.Fatalf("totals = %+v, want sim-node 1536", series.Totals)
	}

	bad := httptest.NewRequest(http.MethodGet, "/v1/traffic?group=unknown", nil)
	bad.AddCookie(cookie)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("unknown group = %d, want 400", badRec.Code)
	}
}

func loginCookie(t *testing.T, handler http.Handler, password string) *http.Cookie {
	t.Helper()
	loginReq := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"`+password+`"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK || len(loginRec.Result().Cookies()) == 0 {
		t.Fatalf("login = %d %s", loginRec.Code, loginRec.Body.String())
	}
	return loginRec.Result().Cookies()[0]
}

func TestProxyListsOutboundsAndSavesMultiplier(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := store.SetMultiplier(ctx, "ss-test", 1.5); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 1, time.Now().UTC(), []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "ss-test", Rx: 100},
		{Class: accounting.ClassProxyRaw, Outbound: "unknown", Rx: 40},
		{Class: accounting.ClassProxyUnadjusted, Outbound: "unknown", Rx: 40},
		{Class: accounting.ClassProxyAdjusted, Outbound: "ss-test", Rx: 150},
	}); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	req := httptest.NewRequest(http.MethodGet, "/v1/proxy", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"ss-test"`) || !strings.Contains(body, `"configured":true`) {
		t.Fatalf("proxy missing configured outbound: %s", body)
	}
	if !strings.Contains(body, `"unknown"`) || !strings.Contains(body, `"configured":false`) {
		t.Fatalf("proxy missing unconfigured outbound: %s", body)
	}

	save := httptest.NewRequest(http.MethodPut, "/v1/proxy/multipliers", strings.NewReader(`{"name":"ss-hk","factor":2}`))
	save.Header.Set("Content-Type", "application/json")
	save.AddCookie(cookie)
	saveRec := httptest.NewRecorder()
	handler.ServeHTTP(saveRec, save)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("save multiplier = %d %s", saveRec.Code, saveRec.Body.String())
	}
	got, err := store.ListMultipliers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["ss-hk"] != 2 {
		t.Fatalf("saved multipliers = %+v", got)
	}
}

func TestRenameAndGetDevice(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	enrollReq := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"site_id":"sim-site","node_id":"sim-node"}`))
	enrollReq.Header.Set("Content-Type", "application/json")
	enrollReq.AddCookie(cookie)
	enrollRec := httptest.NewRecorder()
	handler.ServeHTTP(enrollRec, enrollReq)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", enrollRec.Code, enrollRec.Body.String())
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(bytes.NewReader(enrollRec.Body.Bytes())).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}
	ingestReq := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw))
	ingestReq.Header.Set("Authorization", "Bearer "+issued.Token)
	ingestReq.Header.Set("Content-Encoding", "gzip")
	ingestReq.Header.Set("Content-Type", "application/json")
	ingestRec := httptest.NewRecorder()
	handler.ServeHTTP(ingestRec, ingestReq)
	if ingestRec.Code != http.StatusOK {
		t.Fatalf("ingest = %d %s", ingestRec.Code, ingestRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	var devicesBody struct {
		Devices []sqlitestore.DeviceLedger `json:"devices"`
	}
	if err := json.NewDecoder(bytes.NewReader(listRec.Body.Bytes())).Decode(&devicesBody); err != nil {
		t.Fatal(err)
	}
	if len(devicesBody.Devices) != 1 {
		t.Fatalf("devices = %+v", devicesBody.Devices)
	}
	deviceID := devicesBody.Devices[0].ID

	rename := httptest.NewRequest(http.MethodPut, "/v1/devices/"+deviceID, strings.NewReader(`{"name":"书房电脑"}`))
	rename.Header.Set("Content-Type", "application/json")
	rename.AddCookie(cookie)
	renameRec := httptest.NewRecorder()
	handler.ServeHTTP(renameRec, rename)
	if renameRec.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", renameRec.Code, renameRec.Body.String())
	}

	detail := httptest.NewRequest(http.MethodGet, "/v1/devices/"+deviceID, nil)
	detail.AddCookie(cookie)
	detailRec := httptest.NewRecorder()
	handler.ServeHTTP(detailRec, detail)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("device detail = %d %s", detailRec.Code, detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "书房电脑") {
		t.Fatalf("device detail missing name: %s", detailRec.Body.String())
	}
}

func TestIngestAppliesStoredMultiplier(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMultiplier(t.Context(), "ss-test", 1.5); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	enrollReq := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"site_id":"home","node_id":"proxy-20"}`))
	enrollReq.Header.Set("Content-Type", "application/json")
	enrollReq.AddCookie(cookie)
	enrollRec := httptest.NewRecorder()
	handler.ServeHTTP(enrollRec, enrollReq)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", enrollRec.Code, enrollRec.Body.String())
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(bytes.NewReader(enrollRec.Body.Bytes())).Decode(&issued); err != nil {
		t.Fatal(err)
	}

	raw, err := protocol.Encode(protocol.Batch{
		ProtocolVersion: 1,
		SiteID:          "home",
		NodeID:          "proxy-20",
		BootID:          "boot-a",
		Sequence:        1,
		SampledAt:       time.Now().UTC(),
		IntervalMS:      15000,
		Proxy: &protocol.ProxyDelta{
			ByOutbound: []protocol.OutboundDelta{
				{Name: "ss-test", ProxyRx: 100, ProxyTx: 0},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ingestReq := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw))
	ingestReq.Header.Set("Authorization", "Bearer "+issued.Token)
	ingestReq.Header.Set("Content-Encoding", "gzip")
	ingestReq.Header.Set("Content-Type", "application/json")
	ingestRec := httptest.NewRecorder()
	handler.ServeHTTP(ingestRec, ingestReq)
	if ingestRec.Code != http.StatusOK {
		t.Fatalf("ingest = %d %s", ingestRec.Code, ingestRec.Body.String())
	}

	proxyReq := httptest.NewRequest(http.MethodGet, "/v1/proxy", nil)
	proxyReq.AddCookie(cookie)
	proxyRec := httptest.NewRecorder()
	handler.ServeHTTP(proxyRec, proxyReq)
	if proxyRec.Code != http.StatusOK {
		t.Fatalf("proxy = %d %s", proxyRec.Code, proxyRec.Body.String())
	}
	if !strings.Contains(proxyRec.Body.String(), `"adjusted":150`) && !strings.Contains(proxyRec.Body.String(), `"adjusted": 150`) {
		t.Fatalf("ingest did not apply stored multiplier: %s", proxyRec.Body.String())
	}
}

func TestAlertsSilenceAcknowledgeAndStayQuiet(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateNode(t.Context(), "proxy-20", "home", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.TouchNode(t.Context(), "proxy-20", "boot-a", 15*time.Minute, now.Add(-46*time.Minute)); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	list := httptest.NewRequest(http.MethodGet, "/v1/alerts", nil)
	list.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK {
		t.Fatalf("alerts = %d %s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), `"silence"`) {
		t.Fatalf("expected silence alert: %s", listRec.Body.String())
	}
	var body struct {
		Alerts []sqlitestore.Alert `json:"alerts"`
	}
	if err := json.NewDecoder(bytes.NewReader(listRec.Body.Bytes())).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Alerts) != 1 {
		t.Fatalf("alerts = %+v", body.Alerts)
	}

	ack := httptest.NewRequest(http.MethodPost, "/v1/alerts/"+itoa(body.Alerts[0].ID)+"/ack", strings.NewReader("{}"))
	ack.Header.Set("Content-Type", "application/json")
	ack.AddCookie(cookie)
	ackRec := httptest.NewRecorder()
	handler.ServeHTTP(ackRec, ack)
	if ackRec.Code != http.StatusOK {
		t.Fatalf("ack = %d %s", ackRec.Code, ackRec.Body.String())
	}

	again := httptest.NewRequest(http.MethodGet, "/v1/alerts", nil)
	again.AddCookie(cookie)
	againRec := httptest.NewRecorder()
	handler.ServeHTTP(againRec, again)
	if strings.Contains(againRec.Body.String(), `"silence"`) {
		t.Fatalf("acked alert still listed: %s", againRec.Body.String())
	}
}

func TestProxyBillingComputesRatio(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.ApplyLedgerOnce(t.Context(), "home", "proxy-20", "boot-a", 1, now, []accounting.Increment{
		{Class: accounting.ClassProxyAdjusted, Outbound: "ss-test", Rx: 120, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupDaily(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	save := httptest.NewRequest(http.MethodPut, "/v1/proxy/billing", strings.NewReader(`{"reset_day":1,"provider_bytes":100}`))
	save.Header.Set("Content-Type", "application/json")
	save.AddCookie(cookie)
	saveRec := httptest.NewRecorder()
	handler.ServeHTTP(saveRec, save)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("save billing = %d %s", saveRec.Code, saveRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/proxy", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"local_bytes":120`) || !strings.Contains(rec.Body.String(), `"provider_bytes":100`) {
		t.Fatalf("proxy missing billing: %s", rec.Body.String())
	}

	alerts := httptest.NewRequest(http.MethodGet, "/v1/alerts", nil)
	alerts.AddCookie(cookie)
	alertsRec := httptest.NewRecorder()
	handler.ServeHTTP(alertsRec, alerts)
	if !strings.Contains(alertsRec.Body.String(), `"drift"`) {
		t.Fatalf("expected drift alert: %s", alertsRec.Body.String())
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
