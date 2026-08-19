package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestEnsureFirstRunAdminLeavesSetupToWizard(t *testing.T) {
	t.Setenv("LANTALLY_BOOTSTRAP_ADMIN_PASSWORD", "")
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := EnsureFirstRunAdmin(store, io.Discard); err != nil {
		t.Fatal(err)
	}
	has, err := store.HasAdminCredential()
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("wizard mode must not create a generated admin password")
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

func TestSetupWizardCreatesAdminOnce(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := Routes(store, Config{})

	statusReq := httptest.NewRequest(http.MethodGet, "/v1/setup", nil)
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK || !strings.Contains(statusRec.Body.String(), `"needed":true`) {
		t.Fatalf("setup status = %d %s", statusRec.Code, statusRec.Body.String())
	}

	create := httptest.NewRequest(http.MethodPost, "/v1/setup", strings.NewReader(`{"password":"first-admin-pass"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusOK {
		t.Fatalf("setup = %d %s", createRec.Code, createRec.Body.String())
	}
	if len(createRec.Result().Cookies()) == 0 {
		t.Fatal("setup should log the admin in")
	}
	if err := store.AuthenticateAdmin("first-admin-pass"); err != nil {
		t.Fatal(err)
	}

	again := httptest.NewRequest(http.MethodPost, "/v1/setup", strings.NewReader(`{"password":"other-pass"}`))
	again.Header.Set("Content-Type", "application/json")
	againRec := httptest.NewRecorder()
	handler.ServeHTTP(againRec, again)
	if againRec.Code != http.StatusConflict {
		t.Fatalf("second setup = %d, want 409", againRec.Code)
	}
}

func TestClaimCodeRedeemsOnceAndServesInstallScript(t *testing.T) {
	store, err := sqlitestore.Open("file:r5-claim-http?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	create := httptest.NewRequest(http.MethodPost, "/v1/claims", strings.NewReader(`{"site_id":"home","node_id":"laptop","local":true}`))
	create.Header.Set("Content-Type", "application/json")
	create.Host = "lantally.example.test:8080"
	create.AddCookie(cookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", createRec.Code, createRec.Body.String())
	}
	var issued struct {
		Code    string `json:"code"`
		Linux   string `json:"linux"`
		MacOS   string `json:"macos"`
		Windows string `json:"windows"`
		Local   bool   `json:"local"`
		Mihomo  string `json:"mihomo_hint"`
	}
	if err := json.NewDecoder(bytes.NewReader(createRec.Body.Bytes())).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if issued.Code == "" || !strings.Contains(issued.Linux, issued.Code) || !issued.Local {
		t.Fatalf("issued claim = %+v", issued)
	}
	if !strings.Contains(issued.Mihomo, "127.0.0.1:9090") {
		t.Fatalf("local mode should hint loopback mihomo: %+v", issued)
	}
	if !strings.Contains(issued.Windows, "LANTALLY_CLAIM='"+issued.Code+"'") {
		t.Fatalf("windows command should embed claim: %+v", issued)
	}

	script := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	scriptRec := httptest.NewRecorder()
	handler.ServeHTTP(scriptRec, script)
	if scriptRec.Code != http.StatusOK || !strings.Contains(scriptRec.Body.String(), "/v1/claim") {
		t.Fatalf("install.sh = %d %s", scriptRec.Code, scriptRec.Body.String())
	}
	ps1 := httptest.NewRequest(http.MethodGet, "/install.ps1", nil)
	ps1Rec := httptest.NewRecorder()
	handler.ServeHTTP(ps1Rec, ps1)
	if ps1Rec.Code != http.StatusOK || !strings.Contains(ps1Rec.Body.String(), "/v1/claim") {
		t.Fatalf("install.ps1 = %d %s", ps1Rec.Code, ps1Rec.Body.String())
	}

	redeem := httptest.NewRequest(http.MethodPost, "/v1/claim", strings.NewReader(`{"code":"`+issued.Code+`"}`))
	redeem.Header.Set("Content-Type", "application/json")
	redeemRec := httptest.NewRecorder()
	handler.ServeHTTP(redeemRec, redeem)
	if redeemRec.Code != http.StatusOK {
		t.Fatalf("redeem = %d %s", redeemRec.Code, redeemRec.Body.String())
	}
	var tokenBody struct {
		Token  string `json:"token"`
		SiteID string `json:"site_id"`
		NodeID string `json:"node_id"`
		Local  bool   `json:"local"`
	}
	if err := json.NewDecoder(bytes.NewReader(redeemRec.Body.Bytes())).Decode(&tokenBody); err != nil {
		t.Fatal(err)
	}
	if _, ok := enroll.CredentialID(tokenBody.Token); !ok || tokenBody.NodeID != "laptop" || !tokenBody.Local {
		t.Fatalf("redeemed = %+v", tokenBody)
	}

	again := httptest.NewRequest(http.MethodPost, "/v1/claim", strings.NewReader(`{"code":"`+issued.Code+`"}`))
	again.Header.Set("Content-Type", "application/json")
	againRec := httptest.NewRecorder()
	handler.ServeHTTP(againRec, again)
	if againRec.Code != http.StatusConflict {
		t.Fatalf("second redeem = %d, want 409", againRec.Code)
	}

	detail := httptest.NewRequest(http.MethodGet, "/v1/nodes/laptop", nil)
	detail.AddCookie(cookie)
	detailRec := httptest.NewRecorder()
	handler.ServeHTTP(detailRec, detail)
	if detailRec.Code != http.StatusOK || !strings.Contains(detailRec.Body.String(), `"id":"laptop"`) {
		t.Fatalf("node detail = %d %s", detailRec.Code, detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), `"online":false`) {
		t.Fatalf("new node should wait for first batch: %s", detailRec.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodPost, "/v1/nodes/laptop/revoke", nil)
	revoke.AddCookie(cookie)
	revokeRec := httptest.NewRecorder()
	handler.ServeHTTP(revokeRec, revoke)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", revokeRec.Code, revokeRec.Body.String())
	}
	gone := httptest.NewRequest(http.MethodGet, "/v1/nodes/laptop", nil)
	gone.AddCookie(cookie)
	goneRec := httptest.NewRecorder()
	handler.ServeHTTP(goneRec, gone)
	if goneRec.Code != http.StatusNotFound {
		t.Fatalf("revoked node detail = %d, want 404", goneRec.Code)
	}
}

func TestAgentBinaryServedFromAgentDir(t *testing.T) {
	store, err := sqlitestore.Open("file:r5-agent-bin?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dir := t.TempDir()
	binDir := filepath.Join(dir, "linux", "amd64")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "lantally-agent"), []byte("fake-agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{AgentDir: dir})

	req := httptest.NewRequest(http.MethodGet, "/agents/linux/amd64", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "fake-agent" {
		t.Fatalf("agent binary = %d %q", rec.Code, rec.Body.String())
	}

	missing := httptest.NewRequest(http.MethodGet, "/agents/linux/mips", nil)
	missingRec := httptest.NewRecorder()
	handler.ServeHTTP(missingRec, missing)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing agent = %d, want 404", missingRec.Code)
	}

	empty := Routes(store, Config{})
	emptyRec := httptest.NewRecorder()
	empty.ServeHTTP(emptyRec, httptest.NewRequest(http.MethodGet, "/agents/linux/amd64", nil))
	if emptyRec.Code != http.StatusNotFound {
		t.Fatalf("empty AgentDir = %d, want 404", emptyRec.Code)
	}
}

func TestLoginRateLimitedAfterFailures(t *testing.T) {
	store, err := sqlitestore.Open("file:r6-login-limit?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"wrong-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.10:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("fail %d = %d, want 401", i, rec.Code)
		}
	}
	locked := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"test-password"}`))
	locked.Header.Set("Content-Type", "application/json")
	locked.RemoteAddr = "192.0.2.10:1234"
	lockedRec := httptest.NewRecorder()
	handler.ServeHTTP(lockedRec, locked)
	if lockedRec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login = %d, want 429", lockedRec.Code)
	}
}

func TestSessionCookieSecureWhenHTTPS(t *testing.T) {
	store, err := sqlitestore.Open("file:r6-secure-cookie?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"test-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("missing session cookie")
	}
	if !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("cookie secure=%v httponly=%v, want both true", cookies[0].Secure, cookies[0].HttpOnly)
	}
}

func TestSettingsPasswordBackupAndRetention(t *testing.T) {
	dir := t.TempDir()
	store, err := sqlitestore.Open(filepath.Join(dir, "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	unauth := httptest.NewRequest(http.MethodGet, "/v1/settings", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauth)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("settings without login = %d", unauthRec.Code)
	}

	settings := httptest.NewRequest(http.MethodGet, "/v1/settings", nil)
	settings.AddCookie(cookie)
	settingsRec := httptest.NewRecorder()
	handler.ServeHTTP(settingsRec, settings)
	if settingsRec.Code != http.StatusOK {
		t.Fatalf("settings = %d %s", settingsRec.Code, settingsRec.Body.String())
	}
	body := settingsRec.Body.String()
	if !strings.Contains(body, `"samples_days":14`) || !strings.Contains(body, `"protocol_version":1`) {
		t.Fatalf("settings missing retention/protocol: %s", body)
	}
	if !strings.Contains(body, `"app_version"`) {
		t.Fatalf("settings missing app_version: %s", body)
	}

	backup := httptest.NewRequest(http.MethodGet, "/v1/backup", nil)
	backup.AddCookie(cookie)
	backupRec := httptest.NewRecorder()
	handler.ServeHTTP(backupRec, backup)
	if backupRec.Code != http.StatusOK || backupRec.Body.Len() == 0 {
		t.Fatalf("backup = %d len=%d", backupRec.Code, backupRec.Body.Len())
	}
	if disp := backupRec.Header().Get("Content-Disposition"); !strings.Contains(disp, "lantally.db") {
		t.Fatalf("backup disposition = %q", disp)
	}

	change := httptest.NewRequest(http.MethodPut, "/v1/settings/password", strings.NewReader(`{"current":"test-password","password":"newer-pass"}`))
	change.Header.Set("Content-Type", "application/json")
	change.AddCookie(cookie)
	changeRec := httptest.NewRecorder()
	handler.ServeHTTP(changeRec, change)
	if changeRec.Code != http.StatusOK {
		t.Fatalf("change password = %d %s", changeRec.Code, changeRec.Body.String())
	}
	if err := store.AuthenticateAdmin("newer-pass"); err != nil {
		t.Fatal(err)
	}
}
