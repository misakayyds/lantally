package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/alert"
	"github.com/misakayyds/lantally/internal/collector/sim"
	"github.com/misakayyds/lantally/internal/enroll"
	"github.com/misakayyds/lantally/internal/identity"
	"github.com/misakayyds/lantally/internal/protocol"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
	webassets "github.com/misakayyds/lantally/web"
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

	// Retroactively update multiplier -> /v1/proxy immediately reflects new adjusted total.
	upd := httptest.NewRequest(http.MethodPut, "/v1/proxy/multipliers", strings.NewReader(`{"name":"ss-test","factor":2.5}`))
	upd.Header.Set("Content-Type", "application/json")
	upd.AddCookie(cookie)
	updRec := httptest.NewRecorder()
	handler.ServeHTTP(updRec, upd)
	if updRec.Code != http.StatusOK {
		t.Fatalf("update multiplier = %d %s", updRec.Code, updRec.Body.String())
	}
	proxyReq2 := httptest.NewRequest(http.MethodGet, "/v1/proxy", nil)
	proxyReq2.AddCookie(cookie)
	proxyRec2 := httptest.NewRecorder()
	handler.ServeHTTP(proxyRec2, proxyReq2)
	if !strings.Contains(proxyRec2.Body.String(), `"adjusted":250`) && !strings.Contains(proxyRec2.Body.String(), `"adjusted": 250`) {
		t.Fatalf("proxy did not recalculate retroactively: %s", proxyRec2.Body.String())
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
		{Class: accounting.ClassProxyRaw, Outbound: "ss-test", Rx: 120, Tx: 0},
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

func TestProxyRulesCRUDExportImportAndPreview(t *testing.T) {
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

	// Seed catalog + raw ledger outbounds.
	ctx := t.Context()
	if err := store.UpsertProxyNodeCatalog(ctx, "node-a", []sqlitestore.ProxyNodeCatalogEntry{
		{Name: "HK-IEPL-01", ProviderName: "sub-a"},
		{Name: "JP-0.5x", ProviderName: "sub-a"},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "home", "node-a", "boot-a", 1, time.Now().UTC(), []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "US-Plain", Rx: 100, Tx: 50},
	}); err != nil {
		t.Fatal(err)
	}

	// Create rule.
	createReq := httptest.NewRequest(http.MethodPost, "/v1/proxy/rules", strings.NewReader(`{"name":"iepl","match_type":"keyword","pattern":"IEPL","factor":2.5,"priority":100}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(cookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create rule = %d %s", createRec.Code, createRec.Body.String())
	}
	var createdBody struct {
		Rule accounting.MultiplierRule `json:"rule"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}

	// Update rule.
	updReq := httptest.NewRequest(http.MethodPut, "/v1/proxy/rules/"+itoa(createdBody.Rule.ID), strings.NewReader(`{"name":"iepl-v2","match_type":"keyword","pattern":"IEPL","factor":3.0,"priority":110,"enabled":true}`))
	updReq.Header.Set("Content-Type", "application/json")
	updReq.AddCookie(cookie)
	updRec := httptest.NewRecorder()
	handler.ServeHTTP(updRec, updReq)
	if updRec.Code != http.StatusOK || !strings.Contains(updRec.Body.String(), `"iepl-v2"`) {
		t.Fatalf("update rule = %d %s", updRec.Code, updRec.Body.String())
	}

	// List rules.
	listReq := httptest.NewRequest(http.MethodGet, "/v1/proxy/rules", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"iepl-v2"`) {
		t.Fatalf("list rules = %d %s", listRec.Code, listRec.Body.String())
	}

	// Preview draft rules (without saving) against catalog + outbounds + sample_names.
	prevReq := httptest.NewRequest(http.MethodPost, "/v1/proxy/rules/preview", strings.NewReader(`{
		"rules": [
			{"name":"auto","match_type":"auto_extract","pattern":"","factor":1,"priority":50,"enabled":true},
			{"name":"vip","match_type":"regex","pattern":"^HK-","factor":2,"priority":100,"enabled":true}
		],
		"sample_names": ["SG-Node-x2", "proxy-20"]
	}`))
	prevReq.Header.Set("Content-Type", "application/json")
	prevReq.AddCookie(cookie)
	prevRec := httptest.NewRecorder()
	handler.ServeHTTP(prevRec, prevReq)
	if prevRec.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", prevRec.Code, prevRec.Body.String())
	}
	var prevBody struct {
		Matches []sqlitestore.PreviewMatch `json:"matches"`
	}
	if err := json.Unmarshal(prevRec.Body.Bytes(), &prevBody); err != nil {
		t.Fatal(err)
	}
	byName := map[string]sqlitestore.PreviewMatch{}
	for _, m := range prevBody.Matches {
		byName[m.Name] = m
	}
	if byName["HK-IEPL-01"].Factor != 2.0 || byName["HK-IEPL-01"].MatchSource != accounting.SourceRule {
		t.Fatalf("HK-IEPL-01 preview = %+v", byName["HK-IEPL-01"])
	}
	if byName["JP-0.5x"].Factor != 0.5 || byName["SG-Node-x2"].Factor != 2.0 {
		t.Fatalf("auto_extract previews = %+v / %+v", byName["JP-0.5x"], byName["SG-Node-x2"])
	}
	if byName["proxy-20"].Factor != 1.0 || byName["proxy-20"].MatchSource != accounting.SourceDefault {
		t.Fatalf("proxy-20 should default 1.0x without false-positive, got %+v", byName["proxy-20"])
	}
	if byName["US-Plain"].Factor != 1.0 || byName["US-Plain"].MatchSource != accounting.SourceDefault {
		t.Fatalf("US-Plain should default 1.0x, got %+v", byName["US-Plain"])
	}

	// Export rules, then atomic import failure, then replace import.
	expReq := httptest.NewRequest(http.MethodGet, "/v1/proxy/rules/export", nil)
	expReq.AddCookie(cookie)
	expRec := httptest.NewRecorder()
	handler.ServeHTTP(expRec, expReq)
	if expRec.Code != http.StatusOK || !strings.Contains(expRec.Header().Get("Content-Disposition"), "lantally-multiplier-rules.json") {
		t.Fatalf("export = %d disp=%q", expRec.Code, expRec.Header().Get("Content-Disposition"))
	}

	badImp := httptest.NewRequest(http.MethodPost, "/v1/proxy/rules/import", strings.NewReader(`{"mode":"replace","rules":[{"name":"bad","match_type":"regex","pattern":"[","factor":1}]}`))
	badImp.Header.Set("Content-Type", "application/json")
	badImp.AddCookie(cookie)
	badImpRec := httptest.NewRecorder()
	handler.ServeHTTP(badImpRec, badImp)
	if badImpRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid import = %d, want 400", badImpRec.Code)
	}

	goodImp := httptest.NewRequest(http.MethodPost, "/v1/proxy/rules/import", expRec.Body)
	goodImp.Header.Set("Content-Type", "application/json")
	goodImp.AddCookie(cookie)
	goodImpRec := httptest.NewRecorder()
	handler.ServeHTTP(goodImpRec, goodImp)
	if goodImpRec.Code != http.StatusOK || !strings.Contains(goodImpRec.Body.String(), `"imported":1`) {
		t.Fatalf("re-import exported payload = %d %s", goodImpRec.Code, goodImpRec.Body.String())
	}

	// Delete rule & 404 check.
	var listAfter struct {
		Rules []accounting.MultiplierRule `json:"rules"`
	}
	_ = json.Unmarshal(goodImpRec.Body.Bytes(), &listAfter)
	delReq := httptest.NewRequest(http.MethodDelete, "/v1/proxy/rules/"+itoa(listAfter.Rules[0].ID), nil)
	delReq.AddCookie(cookie)
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete rule = %d %s", delRec.Code, delRec.Body.String())
	}
	del404 := httptest.NewRequest(http.MethodDelete, "/v1/proxy/rules/99999", nil)
	del404.AddCookie(cookie)
	del404Rec := httptest.NewRecorder()
	handler.ServeHTTP(del404Rec, del404)
	if del404Rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing rule = %d, want 404", del404Rec.Code)
	}
}

func TestNodeConfigTogglesCollectDomains(t *testing.T) {
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

	// Auth required.
	unauth := httptest.NewRequest(http.MethodPut, "/v1/nodes/node-a/config", strings.NewReader(`{"collect_domains":true}`))
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauth)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth config = %d, want 401", unauthRec.Code)
	}

	// Enroll node.
	enrollReq := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"site_id":"home","node_id":"node-a"}`))
	enrollReq.Header.Set("Content-Type", "application/json")
	enrollReq.AddCookie(cookie)
	enrollRec := httptest.NewRecorder()
	handler.ServeHTTP(enrollRec, enrollReq)
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(enrollRec.Body.Bytes(), &issued)

	// Missing field -> 400.
	bad := httptest.NewRequest(http.MethodPut, "/v1/nodes/node-a/config", strings.NewReader(`{}`))
	bad.Header.Set("Content-Type", "application/json")
	bad.AddCookie(cookie)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("missing collect_domains = %d, want 400", badRec.Code)
	}

	// Unknown node -> 404.
	missing := httptest.NewRequest(http.MethodPut, "/v1/nodes/nope/config", strings.NewReader(`{"collect_domains":true}`))
	missing.Header.Set("Content-Type", "application/json")
	missing.AddCookie(cookie)
	missingRec := httptest.NewRecorder()
	handler.ServeHTTP(missingRec, missing)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing node = %d, want 404", missingRec.Code)
	}

	// Toggle true -> reflected in GET /v1/nodes/node-a and POST /v1/ingest.
	onReq := httptest.NewRequest(http.MethodPut, "/v1/nodes/node-a/config", strings.NewReader(`{"collect_domains":true}`))
	onReq.Header.Set("Content-Type", "application/json")
	onReq.AddCookie(cookie)
	onRec := httptest.NewRecorder()
	handler.ServeHTTP(onRec, onReq)
	if onRec.Code != http.StatusOK || !strings.Contains(onRec.Body.String(), `"collect_domains":true`) {
		t.Fatalf("toggle on = %d %s", onRec.Code, onRec.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/v1/nodes/node-a", nil)
	detailReq.AddCookie(cookie)
	detailRec := httptest.NewRecorder()
	handler.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK || !strings.Contains(detailRec.Body.String(), `"collect_domains":true`) {
		t.Fatalf("node detail missing collect_domains=true: %s", detailRec.Body.String())
	}

	raw, _ := protocol.Encode(protocol.Batch{
		ProtocolVersion: 1,
		SiteID:          "home",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Now().UTC(),
		IntervalMS:      15000,
	})
	ingReq := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw))
	ingReq.Header.Set("Authorization", "Bearer "+issued.Token)
	ingReq.Header.Set("Content-Encoding", "gzip")
	ingReq.Header.Set("Content-Type", "application/json")
	ingRec := httptest.NewRecorder()
	handler.ServeHTTP(ingRec, ingReq)
	if ingRec.Code != http.StatusOK || !strings.Contains(ingRec.Body.String(), `"collect_domains":true`) {
		t.Fatalf("ingest heartbeat missing collect_domains=true: %d %s", ingRec.Code, ingRec.Body.String())
	}
}

func TestDimensionsAndTimeRangeFiltersAcrossEndpoints(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := store.CreateNode(ctx, "node-a", "home", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	shanghaiLoc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	nowShanghai := now.In(shanghaiLoc)
	shanghaiMidnightUTC := time.Date(nowShanghai.Year(), nowShanghai.Month(), nowShanghai.Day(), 0, 0, 0, 0, shanghaiLoc).UTC()
	utcMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	todayStart := utcMidnight
	if shanghaiMidnightUTC.After(todayStart) {
		todayStart = shanghaiMidnightUTC
	}

	// Sample 1: 10 days ago (within 30d, outside 7d)
	// Sample 2: strictly inside [todayStart, now] (within today in both UTC & Asia/Shanghai, 24h, 72h, 7d, 30d)
	oldTS := now.Add(-10 * 24 * time.Hour)
	recentTS := now.Add(-20 * time.Minute)
	if recentTS.Before(todayStart) {
		recentTS = todayStart.Add(now.Sub(todayStart) / 2)
	}
	if _, err := store.ApplyLedgerWithDimensionsOnce(ctx, "home", "node-a", "boot-a", 1, oldTS, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 1000, Tx: 200},
		{Class: accounting.ClassIface, Iface: "eth0", Rx: 1000, Tx: 200},
	}, []sqlitestore.DimensionIncrement{
		{Type: "process", Key: "curl", Rx: 500, Tx: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerWithDimensionsOnce(ctx, "home", "node-a", "boot-a", 2, recentTS, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 300, Tx: 100},
		{Class: accounting.ClassIface, Iface: "eth0", Rx: 300, Tx: 100},
	}, []sqlitestore.DimensionIncrement{
		{Type: "process", Key: "curl", Rx: 200, Tx: 50},
		{Type: "group", Key: "PROXY", Rx: 200, Tx: 50},
	}); err != nil {
		t.Fatal(err)
	}

	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	// 24h dimensions -> only recent sample (rx=200, tx=50)
	dim24 := httptest.NewRequest(http.MethodGet, "/v1/dimensions?type=process&range=24h", nil)
	dim24.AddCookie(cookie)
	dim24Rec := httptest.NewRecorder()
	handler.ServeHTTP(dim24Rec, dim24)
	if dim24Rec.Code != http.StatusOK {
		t.Fatalf("dimensions 24h = %d %s", dim24Rec.Code, dim24Rec.Body.String())
	}
	var dimRes sqlitestore.DimensionResult
	if err := json.Unmarshal(dim24Rec.Body.Bytes(), &dimRes); err != nil {
		t.Fatal(err)
	}
	if dimRes.TotalRx != 200 || dimRes.TotalTx != 50 || len(dimRes.Items) != 1 || dimRes.Items[0].Share != 1 {
		t.Fatalf("24h dimensions = %+v", dimRes)
	}

	// 30d dimensions -> immediately includes both old + recent without waiting for MaintainLedgers!
	dim30 := httptest.NewRequest(http.MethodGet, "/v1/dimensions?type=process&range=30d", nil)
	dim30.AddCookie(cookie)
	dim30Rec := httptest.NewRecorder()
	handler.ServeHTTP(dim30Rec, dim30)
	if dim30Rec.Code != http.StatusOK {
		t.Fatalf("dimensions 30d = %d %s", dim30Rec.Code, dim30Rec.Body.String())
	}
	var dim30Res sqlitestore.DimensionResult
	_ = json.Unmarshal(dim30Rec.Body.Bytes(), &dim30Res)
	if dim30Res.TotalRx != 700 || dim30Res.TotalTx != 150 || dim30Res.Total != 850 {
		t.Fatalf("30d dimensions = %+v, want rx=700 tx=150 total=850", dim30Res)
	}

	// Overview with range=24h shows rx/tx/total for 24h only.
	ov24 := httptest.NewRequest(http.MethodGet, "/v1/overview?range=24h", nil)
	ov24.AddCookie(cookie)
	ov24Rec := httptest.NewRecorder()
	handler.ServeHTTP(ov24Rec, ov24)
	if ov24Rec.Code != http.StatusOK {
		t.Fatalf("overview 24h = %d %s", ov24Rec.Code, ov24Rec.Body.String())
	}
	var ovBody struct {
		Rx    uint64 `json:"rx"`
		Tx    uint64 `json:"tx"`
		Total uint64 `json:"total"`
	}
	_ = json.Unmarshal(ov24Rec.Body.Bytes(), &ovBody)
	if ovBody.Rx != 300 || ovBody.Tx != 100 || ovBody.Total != 400 {
		t.Fatalf("overview 24h = %+v, want rx=300 tx=100 total=400", ovBody)
	}

	// Traffic group=iface with range=today&tz=Asia/Shanghai returns rx_totals/tx_totals.
	trToday := httptest.NewRequest(http.MethodGet, "/v1/traffic?group=iface&range=today&tz=Asia/Shanghai", nil)
	trToday.AddCookie(cookie)
	trTodayRec := httptest.NewRecorder()
	handler.ServeHTTP(trTodayRec, trToday)
	if trTodayRec.Code != http.StatusOK {
		t.Fatalf("traffic today = %d %s", trTodayRec.Code, trTodayRec.Body.String())
	}
	var series sqlitestore.TrafficSeries
	_ = json.Unmarshal(trTodayRec.Body.Bytes(), &series)
	if series.RxTotals["eth0"] != 300 || series.TxTotals["eth0"] != 100 {
		t.Fatalf("traffic today iface rx/tx = %+v", series)
	}

	// Invalid range/tz/device+dimension -> 400.
	for _, badURL := range []string{
		"/v1/traffic?range=bogus",
		"/v1/traffic?range=7d&from=2026-08-01",
		"/v1/traffic?tz=Invalid/Zone",
		"/v1/traffic?group=process&device=dev-1",
		"/v1/dimensions?type=bogus",
	} {
		req := httptest.NewRequest(http.MethodGet, badURL, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s -> %d, want 400", badURL, rec.Code)
		}
	}
}

func TestIdleAndStraddlingTimeRangeHTTP(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := store.CreateNode(ctx, "node-a", "home", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	at10Z := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	devID, _, err := store.ResolveIdentityAtomic("home", "node-a", []identity.Evidence{
		{Rank: identity.RankStableMAC, Value: "02:00:00:00:00:01"},
	}, "", at10Z)
	if err != nil {
		t.Fatal(err)
	}

	// (a) today&tz=Asia/Shanghai at 2026-08-18T18:00:00Z resolves to [16:00Z, 18:00Z]
	// and must return 0 when traffic occurred at 2026-08-18T10:00:00Z.
	reqTZ := httptest.NewRequest(http.MethodGet, "/v1/overview?range=today&tz=Asia/Shanghai", nil)
	trTZ, err := parseTimeRange(reqTZ, store, time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := time.Date(2026, 8, 18, 16, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC)
	if !trTZ.From.Equal(wantFrom) || !trTZ.To.Equal(wantTo) {
		t.Fatalf("parseTimeRange(today, Asia/Shanghai) = %+v, want [%s, %s]", trTZ, wantFrom, wantTo)
	}

	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-a", "boot-a", 1, at10Z,
		[]accounting.Increment{
			{Class: accounting.ClassTotal, Rx: 800, Tx: 200},
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 500, Tx: 100},
			{DeviceID: devID, Class: accounting.ClassTotal, Rx: 800, Tx: 200},
		},
		[]sqlitestore.DimensionIncrement{{Type: "process", Key: "curl", Rx: 300, Tx: 100}},
	); err != nil {
		t.Fatal(err)
	}

	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	assertZeroRange := func(label, q string) {
		t.Helper()
		ovReq := httptest.NewRequest(http.MethodGet, "/v1/overview?"+q, nil)
		ovReq.AddCookie(cookie)
		ovRec := httptest.NewRecorder()
		handler.ServeHTTP(ovRec, ovReq)
		if ovRec.Code != http.StatusOK {
			t.Fatalf("%s overview = %d %s", label, ovRec.Code, ovRec.Body.String())
		}
		var ov struct {
			Total uint64 `json:"total"`
		}
		_ = json.Unmarshal(ovRec.Body.Bytes(), &ov)
		if ov.Total != 0 {
			t.Fatalf("%s overview total = %d, want 0", label, ov.Total)
		}

		trReq := httptest.NewRequest(http.MethodGet, "/v1/traffic?group=node&"+q, nil)
		trReq.AddCookie(cookie)
		trRec := httptest.NewRecorder()
		handler.ServeHTTP(trRec, trReq)
		var ts sqlitestore.TrafficSeries
		_ = json.Unmarshal(trRec.Body.Bytes(), &ts)
		if ts.Totals["node-a"] != 0 {
			t.Fatalf("%s traffic totals = %+v, want 0", label, ts.Totals)
		}

		dimReq := httptest.NewRequest(http.MethodGet, "/v1/dimensions?type=process&"+q, nil)
		dimReq.AddCookie(cookie)
		dimRec := httptest.NewRecorder()
		handler.ServeHTTP(dimRec, dimReq)
		var dr sqlitestore.DimensionResult
		_ = json.Unmarshal(dimRec.Body.Bytes(), &dr)
		if dr.Total != 0 {
			t.Fatalf("%s dimensions total = %d, want 0", label, dr.Total)
		}

		pxReq := httptest.NewRequest(http.MethodGet, "/v1/proxy?"+q, nil)
		pxReq.AddCookie(cookie)
		pxRec := httptest.NewRecorder()
		handler.ServeHTTP(pxRec, pxReq)
		var px struct {
			Total     uint64                     `json:"total"`
			Outbounds []sqlitestore.OutboundLedger `json:"outbounds"`
		}
		_ = json.Unmarshal(pxRec.Body.Bytes(), &px)
		if px.Total != 0 || len(px.Outbounds) != 0 {
			t.Fatalf("%s proxy = %+v, want 0", label, px)
		}

		dvReq := httptest.NewRequest(http.MethodGet, "/v1/devices?"+q, nil)
		dvReq.AddCookie(cookie)
		dvRec := httptest.NewRecorder()
		handler.ServeHTTP(dvRec, dvReq)
		var dv struct {
			Devices []sqlitestore.DeviceLedger `json:"devices"`
		}
		_ = json.Unmarshal(dvRec.Body.Bytes(), &dv)
		if len(dv.Devices) != 1 || dv.Devices[0].Total != 0 {
			t.Fatalf("%s devices = %+v, want 0", label, dv.Devices)
		}
	}

	assertZeroRange("today-asia-shanghai-window", "from=2026-08-18T16:00:00Z&to=2026-08-18T18:00:00Z")

	// (b) Live 24h idle check: place a sample earlier on (now-24h)'s UTC day before (now-24h).
	now := time.Now().UTC()
	from24 := now.Add(-24 * time.Hour)
	fromDay := from24.Truncate(24 * time.Hour)
	if offset := from24.Sub(fromDay); offset > 2*time.Second {
		idleSampleAt := fromDay.Add(offset / 2)
		if _, err := store.ApplyLedgerWithDimensionsOnce(
			ctx, "home", "node-a", "boot-a", 2, idleSampleAt,
			[]accounting.Increment{
				{Class: accounting.ClassTotal, Rx: 400, Tx: 100},
				{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 200, Tx: 50},
				{DeviceID: devID, Class: accounting.ClassTotal, Rx: 400, Tx: 100},
			},
			[]sqlitestore.DimensionIncrement{{Type: "process", Key: "curl", Rx: 150, Tx: 50}},
		); err != nil {
			t.Fatal(err)
		}
		assertZeroRange("range=24h-idle", "range=24h")
	}

	// (c) Straddling <=7d window across 14d purge boundary (day-15 daily-only + day-13 samples).
	day15 := now.Truncate(24*time.Hour).AddDate(0, 0, -15).Add(10 * time.Hour)
	day13 := now.Truncate(24*time.Hour).AddDate(0, 0, -13).Add(10 * time.Hour)
	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-a", "boot-a", 3, day15,
		[]accounting.Increment{
			{Class: accounting.ClassTotal, Rx: 5000, Tx: 1000},
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 3000, Tx: 500},
			{DeviceID: devID, Class: accounting.ClassTotal, Rx: 5000, Tx: 1000},
		},
		[]sqlitestore.DimensionIncrement{{Type: "process", Key: "curl", Rx: 2000, Tx: 400}},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MaintainLedgers(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-a", "boot-a", 4, day13,
		[]accounting.Increment{
			{Class: accounting.ClassTotal, Rx: 1000, Tx: 200},
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 700, Tx: 100},
			{DeviceID: devID, Class: accounting.ClassTotal, Rx: 1000, Tx: 200},
		},
		[]sqlitestore.DimensionIncrement{{Type: "process", Key: "curl", Rx: 400, Tx: 50}},
	); err != nil {
		t.Fatal(err)
	}

	straddleQ := "from=" + day15.Add(-4*time.Hour).Format(time.RFC3339) + "&to=" + day13.Add(4*time.Hour).Format(time.RFC3339)

	ovReq := httptest.NewRequest(http.MethodGet, "/v1/overview?"+straddleQ, nil)
	ovReq.AddCookie(cookie)
	ovRec := httptest.NewRecorder()
	handler.ServeHTTP(ovRec, ovReq)
	var ov struct {
		Total uint64 `json:"total"`
	}
	_ = json.Unmarshal(ovRec.Body.Bytes(), &ov)
	if ov.Total != 7200 {
		t.Fatalf("straddling HTTP overview total = %d, want 7200", ov.Total)
	}

	dimReq := httptest.NewRequest(http.MethodGet, "/v1/dimensions?type=process&"+straddleQ, nil)
	dimReq.AddCookie(cookie)
	dimRec := httptest.NewRecorder()
	handler.ServeHTTP(dimRec, dimReq)
	var dr sqlitestore.DimensionResult
	_ = json.Unmarshal(dimRec.Body.Bytes(), &dr)
	if dr.Total != 2850 {
		t.Fatalf("straddling HTTP dimensions total = %d, want 2850", dr.Total)
	}
}

func TestDimensionsAndRulesReturn400ForValidationAnd503ForDBFailure(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	// Validation errors -> 400.
	badRequests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/dimensions?type=bogus", ""},
		{http.MethodPost, "/v1/proxy/rules", `{"name":"","match_type":"keyword","pattern":"hk","factor":1}`},
		{http.MethodPut, "/v1/proxy/rules/1", `{"name":"bad","match_type":"regex","pattern":"[","factor":1}`},
		{http.MethodPost, "/v1/proxy/rules/import", `{"mode":"replace","rules":[{"name":"a","match_type":"exact","pattern":"dup","factor":1},{"name":"b","match_type":"exact","pattern":"dup","factor":2}]}`},
		{http.MethodPost, "/v1/proxy/rules/preview", `{"rules":[{"name":"bad","match_type":"regex","pattern":"[","factor":1}]}`},
	}
	for _, tc := range badRequests {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s validation = %d, want 400 (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	// Unexpected DB failure (closed store) -> 503 for all 5 endpoints.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	dbErrorRequests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/dimensions?type=process", ""},
		{http.MethodPost, "/v1/proxy/rules", `{"name":"hk","match_type":"keyword","pattern":"hk","factor":2}`},
		{http.MethodPut, "/v1/proxy/rules/1", `{"name":"hk","match_type":"keyword","pattern":"hk","factor":2}`},
		{http.MethodPost, "/v1/proxy/rules/import", `{"mode":"replace","rules":[{"name":"hk","match_type":"keyword","pattern":"hk","factor":2}]}`},
		{http.MethodPost, "/v1/proxy/rules/preview", `{"rules":[]}`},
	}
	for _, tc := range dbErrorRequests {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s DB failure = %d, want 503 (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestLastTrafficWithRangeAndTimeRange400Vs503(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})
	cookie := loginCookie(t, handler, "test-password")

	badQueryPaths := []string{
		"/v1/overview?group=unknown",
		"/v1/overview?bucket=0",
		"/v1/proxy?group=process&device=dev-1",
		"/v1/nodes?bucket=abc",
		"/v1/overview?range=bogus",
		"/v1/overview?tz=Invalid/Zone",
	}
	for _, p := range badQueryPaths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 (%s)", p, rec.Code, rec.Body.String())
		}
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	dbErrPaths := []string{
		"/v1/overview?range=billing",
		"/v1/overview",
		"/v1/proxy",
		"/v1/nodes",
	}
	for _, p := range dbErrPaths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s closed DB = %d, want 503 (%s)", p, rec.Code, rec.Body.String())
		}
	}
}

func TestWatchlistHTTPRoutesAndOverviewPinnedWatch(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})

	// Auth guard -> 401.
	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/watch", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth GET /v1/watch = %d, want 401", unauthRec.Code)
	}

	cookie := loginCookie(t, handler, "test-password")

	if err := store.CreateNode(t.Context(), "node-w1", "site-1", "cred-w1", []byte("h1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMultiplierRule(t.Context(), accounting.MultiplierRule{
		Name: "HK2x", MatchType: accounting.MatchExact, Pattern: "hk-01", Factor: 2.0, Priority: 100, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	monthStartUTC := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	sampleTS := now.Add(-1 * time.Hour)
	if sampleTS.Before(monthStartUTC) {
		sampleTS = monthStartUTC.Add(now.Sub(monthStartUTC) / 2)
	}
	if _, err := store.ApplyLedgerWithDimensionsOnce(t.Context(), "site-1", "node-w1", "boot-1", 1, sampleTS,
		[]accounting.Increment{{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 700, Tx: 300}},
		[]sqlitestore.DimensionIncrement{
			{Type: "group", Key: "AI", Rx: 500, Tx: 100},
			{Type: "process", Key: "Cursor", Rx: 500, Tx: 100},
		},
	); err != nil {
		t.Fatal(err)
	}

	// Empty overview pinned_watch must be [] (non-nil).
	ovEmptyReq := httptest.NewRequest(http.MethodGet, "/v1/overview", nil)
	ovEmptyReq.AddCookie(cookie)
	ovEmptyRec := httptest.NewRecorder()
	handler.ServeHTTP(ovEmptyRec, ovEmptyReq)
	if ovEmptyRec.Code != http.StatusOK || !strings.Contains(ovEmptyRec.Body.String(), `"pinned_watch":[]`) {
		t.Fatalf("overview empty pinned_watch = %d %s", ovEmptyRec.Code, ovEmptyRec.Body.String())
	}

	// Validation errors -> 400.
	badPayloads := []string{
		`{"name":"","matchers":{"process":{"type":"exact","values":["Cursor"]}}}`,
		`{"name":"Empty","matchers":{}}`,
		`{"name":"AllBlank","matchers":{"domain":{"type":"exact","values":["  ","\t"]}}}`,
		`{"name":"BadRe","matchers":{"domain":{"type":"regex","values":["("]}}}`,
		`{"name":"BadOv","matchers":{"outbound":{"type":"exact","values":["hk-01"]}},"override_factor":0}`,
		`{"name":"BadDay","matchers":{"process":{"type":"exact","values":["Cursor"]}},"reset_type":"monthly","reset_day":29}`,
		`{"name":"BadUnit","matchers":{"process":{"type":"exact","values":["Cursor"]}},"quota_value":1,"quota_unit":"PB"}`,
		`{"name":"HugeQuotaVal","matchers":{"process":{"type":"exact","values":["Cursor"]}},"quota_value":9000000,"quota_unit":"TB"}`,
		`{"name":"HugeQuotaBytes","matchers":{"process":{"type":"exact","values":["Cursor"]}},"quota_bytes":9223372036854775808,"quota_unit":"GB"}`,
	}
	for i, body := range badPayloads {
		req := httptest.NewRequest(http.MethodPost, "/v1/watch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("bad watch #%d = %d, want 400 (%s)", i, rec.Code, rec.Body.String())
		}
	}

	// Create pinned watch with group+process + override_factor.
	cReq := httptest.NewRequest(http.MethodPost, "/v1/watch", strings.NewReader(`{
		"name":"AI Coding",
		"matchers":{"group":{"type":"exact","values":["AI"]},"process":{"type":"exact","values":["Cursor"]}},
		"quota_value":1,"quota_unit":"MB","reset_type":"monthly","reset_day":1,"pinned":true
	}`))
	cReq.Header.Set("Content-Type", "application/json")
	cReq.AddCookie(cookie)
	cRec := httptest.NewRecorder()
	handler.ServeHTTP(cRec, cReq)
	if cRec.Code != http.StatusCreated && cRec.Code != http.StatusOK {
		t.Fatalf("POST /v1/watch = %d %s", cRec.Code, cRec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(cRec.Body.Bytes(), &created); err != nil || created.ID <= 0 {
		t.Fatalf("expected top-level id, got %s", cRec.Body.String())
	}

	// GET /v1/watch -> 500/100 (no double counting), non-empty series.
	lReq := httptest.NewRequest(http.MethodGet, "/v1/watch", nil)
	lReq.AddCookie(cookie)
	lRec := httptest.NewRecorder()
	handler.ServeHTTP(lRec, lReq)
	if lRec.Code != http.StatusOK {
		t.Fatalf("GET /v1/watch = %d %s", lRec.Code, lRec.Body.String())
	}
	var listResp struct {
		Items []sqlitestore.WatchItemStatus `json:"items"`
	}
	if err := json.Unmarshal(lRec.Body.Bytes(), &listResp); err != nil || len(listResp.Items) != 1 {
		t.Fatalf("unexpected GET /v1/watch body: %s", lRec.Body.String())
	}
	if listResp.Items[0].Rx != 500 || listResp.Items[0].Tx != 100 || len(listResp.Items[0].Series) == 0 {
		t.Fatalf("watch eval mismatch: %+v", listResp.Items[0])
	}

	// GET /v1/watch/{id}
	gReq := httptest.NewRequest(http.MethodGet, "/v1/watch/"+itoa(created.ID), nil)
	gReq.AddCookie(cookie)
	gRec := httptest.NewRecorder()
	handler.ServeHTTP(gRec, gReq)
	if gRec.Code != http.StatusOK || !strings.Contains(gRec.Body.String(), `"buckets":[`) {
		t.Fatalf("GET /v1/watch/{id} = %d %s", gRec.Code, gRec.Body.String())
	}

	// GET /v1/overview includes pinned_watch.
	ovReq := httptest.NewRequest(http.MethodGet, "/v1/overview", nil)
	ovReq.AddCookie(cookie)
	ovRec := httptest.NewRecorder()
	handler.ServeHTTP(ovRec, ovReq)
	if ovRec.Code != http.StatusOK || !strings.Contains(ovRec.Body.String(), `"AI Coding"`) {
		t.Fatalf("GET /v1/overview missing pinned_watch: %s", ovRec.Body.String())
	}

	// PUT & DELETE + 404 on missing.
	uReq := httptest.NewRequest(http.MethodPut, "/v1/watch/"+itoa(created.ID), strings.NewReader(`{
		"name":"AI Renamed","matchers":{"outbound":{"type":"exact","values":["hk-01"]}},"override_factor":1.0
	}`))
	uReq.Header.Set("Content-Type", "application/json")
	uReq.AddCookie(cookie)
	uRec := httptest.NewRecorder()
	handler.ServeHTTP(uRec, uReq)
	if uRec.Code != http.StatusOK || !strings.Contains(uRec.Body.String(), `"AI Renamed"`) {
		t.Fatalf("PUT /v1/watch/{id} = %d %s", uRec.Code, uRec.Body.String())
	}

	dReq := httptest.NewRequest(http.MethodDelete, "/v1/watch/"+itoa(created.ID), nil)
	dReq.AddCookie(cookie)
	dRec := httptest.NewRecorder()
	handler.ServeHTTP(dRec, dReq)
	if dRec.Code != http.StatusOK {
		t.Fatalf("DELETE /v1/watch/{id} = %d %s", dRec.Code, dRec.Body.String())
	}

	missReq := httptest.NewRequest(http.MethodGet, "/v1/watch/"+itoa(created.ID), nil)
	missReq.AddCookie(cookie)
	missRec := httptest.NewRecorder()
	handler.ServeHTTP(missRec, missReq)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted watch = %d, want 404", missRec.Code)
	}

	// Closed DB -> 503.
	_ = store.Close()
	closedReq := httptest.NewRequest(http.MethodGet, "/v1/watch", nil)
	closedReq.AddCookie(cookie)
	closedRec := httptest.NewRecorder()
	handler.ServeHTTP(closedRec, closedReq)
	if closedRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed DB GET /v1/watch = %d, want 503", closedRec.Code)
	}
}

func TestWatchlistHTTPMixedWhitespaceNormalized(t *testing.T) {
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

	// Mixed valid + whitespace-only + trailing-dot duplicate -> 201/200 and normalized.
	cReq := httptest.NewRequest(http.MethodPost, "/v1/watch", strings.NewReader(`{
		"name":"MixedWatch",
		"matchers":{"domain":{"type":"exact","values":["dup.example.test","  ","dup.example.test."]}}
	}`))
	cReq.Header.Set("Content-Type", "application/json")
	cReq.AddCookie(cookie)
	cRec := httptest.NewRecorder()
	handler.ServeHTTP(cRec, cReq)
	if cRec.Code != http.StatusCreated && cRec.Code != http.StatusOK {
		t.Fatalf("POST /v1/watch mixed whitespace = %d %s", cRec.Code, cRec.Body.String())
	}
	var created accounting.WatchItem
	if err := json.Unmarshal(cRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created watch: %v (%s)", err, cRec.Body.String())
	}
	vals := created.Matchers[accounting.WatchDimDomain].Values
	if len(vals) != 1 || vals[0] != "dup.example.test" {
		t.Fatalf("normalized domain values = %v, want [dup.example.test]", vals)
	}

	// All-whitespace values -> 400.
	bReq := httptest.NewRequest(http.MethodPost, "/v1/watch", strings.NewReader(`{
		"name":"BlankWatch",
		"matchers":{"domain":{"type":"exact","values":["  ","\t"]}}
	}`))
	bReq.Header.Set("Content-Type", "application/json")
	bReq.AddCookie(cookie)
	bRec := httptest.NewRecorder()
	handler.ServeHTTP(bRec, bReq)
	if bRec.Code != http.StatusBadRequest {
		t.Fatalf("POST /v1/watch all whitespace = %d, want 400 (%s)", bRec.Code, bRec.Body.String())
	}
}

func TestProxyProvidersHTTPRoutesAndAlerts(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{})

	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/proxy/providers", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth GET /v1/proxy/providers = %d, want 401", unauthRec.Code)
	}

	cookie := loginCookie(t, handler, "test-password")

	// Empty list returns non-nil [].
	emptyReq := httptest.NewRequest(http.MethodGet, "/v1/proxy/providers", nil)
	emptyReq.AddCookie(cookie)
	emptyRec := httptest.NewRecorder()
	handler.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK || !strings.Contains(emptyRec.Body.String(), `"providers":[]`) {
		t.Fatalf("empty GET /v1/proxy/providers = %d %s", emptyRec.Code, emptyRec.Body.String())
	}

	// Validation errors -> 400.
	badPayloads := []string{
		`{"name":"","unit":"GB","total_value":10,"reset_day":1}`,
		`{"name":"BadUnit","unit":"PB","total_value":10,"reset_day":1}`,
		`{"name":"BadDay","unit":"GB","total_value":10,"reset_day":31}`,
		`{"name":"BadRe","unit":"GB","total_value":10,"reset_day":1,"outbound_pattern":"["}`,
		`{"name":"HugeTotalVal","unit":"TB","total_value":9000000,"reset_day":1}`,
		`{"name":"HugeTotalBytes","unit":"GB","total_bytes":9223372036854775808,"reset_day":1}`,
		`{"name":"HugeUploadBytes","unit":"GB","upload_bytes":9223372036854775808,"reset_day":1}`,
		`{"name":"HugeDownloadBytes","unit":"GB","download_bytes":9223372036854775808,"reset_day":1}`,
	}
	for i, body := range badPayloads {
		req := httptest.NewRequest(http.MethodPost, "/v1/proxy/providers", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("bad provider #%d = %d, want 400 (%s)", i, rec.Code, rec.Body.String())
		}
	}

	// Create manual provider -> 201 with top-level id and binary GiB total_bytes.
	cReq := httptest.NewRequest(http.MethodPost, "/v1/proxy/providers", strings.NewReader(`{
		"name":"ManualSub","unit":"GB","total_value":50,"used_value":5,"reset_day":15
	}`))
	cReq.Header.Set("Content-Type", "application/json")
	cReq.AddCookie(cookie)
	cRec := httptest.NewRecorder()
	handler.ServeHTTP(cRec, cReq)
	if cRec.Code != http.StatusCreated && cRec.Code != http.StatusOK {
		t.Fatalf("POST /v1/proxy/providers = %d %s", cRec.Code, cRec.Body.String())
	}
	var created struct {
		ID         int64  `json:"id"`
		TotalBytes uint64 `json:"total_bytes"`
	}
	if err := json.Unmarshal(cRec.Body.Bytes(), &created); err != nil || created.ID <= 0 || created.TotalBytes != 50*(1<<30) {
		t.Fatalf("unexpected POST /v1/proxy/providers body: %s", cRec.Body.String())
	}

	// Update & Delete + 404.
	uReq := httptest.NewRequest(http.MethodPut, "/v1/proxy/providers/"+itoa(created.ID), strings.NewReader(`{
		"name":"ManualSub","unit":"TB","total_value":1,"used_value":0,"reset_day":15
	}`))
	uReq.Header.Set("Content-Type", "application/json")
	uReq.AddCookie(cookie)
	uRec := httptest.NewRecorder()
	handler.ServeHTTP(uRec, uReq)
	if uRec.Code != http.StatusOK || !strings.Contains(uRec.Body.String(), `"total_bytes":1099511627776`) {
		t.Fatalf("PUT /v1/proxy/providers/{id} = %d %s", uRec.Code, uRec.Body.String())
	}

	dReq := httptest.NewRequest(http.MethodDelete, "/v1/proxy/providers/"+itoa(created.ID), nil)
	dReq.AddCookie(cookie)
	dRec := httptest.NewRecorder()
	handler.ServeHTTP(dRec, dReq)
	if dRec.Code != http.StatusOK {
		t.Fatalf("DELETE /v1/proxy/providers/{id} = %d %s", dRec.Code, dRec.Body.String())
	}

	missReq := httptest.NewRequest(http.MethodDelete, "/v1/proxy/providers/"+itoa(created.ID), nil)
	missReq.AddCookie(cookie)
	missRec := httptest.NewRecorder()
	handler.ServeHTTP(missRec, missReq)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("DELETE missing provider = %d, want 404", missRec.Code)
	}

	// Closed DB -> 503.
	_ = store.Close()
	closedReq := httptest.NewRequest(http.MethodGet, "/v1/proxy/providers", nil)
	closedReq.AddCookie(cookie)
	closedRec := httptest.NewRecorder()
	handler.ServeHTTP(closedRec, closedReq)
	if closedRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed DB GET /v1/proxy/providers = %d, want 503", closedRec.Code)
	}
}

func TestProxyBillingRejectsMaxInt64Overflow(t *testing.T) {
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

	// Seed valid billing first.
	validReq := httptest.NewRequest(http.MethodPut, "/v1/proxy/billing", strings.NewReader(`{"reset_day":5,"provider_bytes":1000}`))
	validReq.Header.Set("Content-Type", "application/json")
	validReq.AddCookie(cookie)
	validRec := httptest.NewRecorder()
	handler.ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusOK {
		t.Fatalf("valid PUT /v1/proxy/billing = %d %s", validRec.Code, validRec.Body.String())
	}

	// > math.MaxInt64 (9223372036854775808) must return 400 and preserve prior valid billing.
	overflowReq := httptest.NewRequest(http.MethodPut, "/v1/proxy/billing", strings.NewReader(`{"reset_day":1,"provider_bytes":9223372036854775808}`))
	overflowReq.Header.Set("Content-Type", "application/json")
	overflowReq.AddCookie(cookie)
	overflowRec := httptest.NewRecorder()
	handler.ServeHTTP(overflowRec, overflowReq)
	if overflowRec.Code != http.StatusBadRequest {
		t.Fatalf("overflow PUT /v1/proxy/billing = %d, want 400 (%s)", overflowRec.Code, overflowRec.Body.String())
	}

	proxyReq := httptest.NewRequest(http.MethodGet, "/v1/proxy", nil)
	proxyReq.AddCookie(cookie)
	proxyRec := httptest.NewRecorder()
	handler.ServeHTTP(proxyRec, proxyReq)
	if proxyRec.Code != http.StatusOK || !strings.Contains(proxyRec.Body.String(), `"provider_bytes":1000`) || !strings.Contains(proxyRec.Body.String(), `"reset_day":5`) {
		t.Fatalf("GET /v1/proxy after rejected billing = %d %s", proxyRec.Code, proxyRec.Body.String())
	}

	// Exact math.MaxInt64 (9223372036854775807) succeeds.
	maxReq := httptest.NewRequest(http.MethodPut, "/v1/proxy/billing", strings.NewReader(`{"reset_day":1,"provider_bytes":9223372036854775807}`))
	maxReq.Header.Set("Content-Type", "application/json")
	maxReq.AddCookie(cookie)
	maxRec := httptest.NewRecorder()
	handler.ServeHTTP(maxRec, maxReq)
	if maxRec.Code != http.StatusOK || !strings.Contains(maxRec.Body.String(), `"provider_bytes":9223372036854775807`) {
		t.Fatalf("MaxInt64 PUT /v1/proxy/billing = %d %s", maxRec.Code, maxRec.Body.String())
	}
}

func TestStaticAssetsAndSPAFallback(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mockFS := fstest.MapFS{
		"index.html": {
			Data: []byte(`<!doctype html><html><head><title>LanTally</title><link rel="stylesheet" href="/assets/app-abc.css"><script type="module" src="/assets/app-abc.js"></script></head><body><div id="root"></div></body></html>`),
		},
		"assets/app-abc.js": {
			Data: []byte(`console.log("lantally");`),
		},
		"assets/app-abc.css": {
			Data: []byte(`body { margin: 0; }`),
		},
	}

	handler := Routes(store, Config{StaticFS: mockFS})

	for _, p := range []string{"/", "/overview", "/proxy", "/watchlist", "/apps", "/devices", "/nodes", "/alerts"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("GET %s Content-Type = %q, want text/html", p, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("GET %s Cache-Control = %q, want no-cache", p, cc)
		}
		if !strings.Contains(rec.Body.String(), "<title>LanTally</title>") {
			t.Fatalf("GET %s body missing title: %s", p, rec.Body.String())
		}
	}

	jsReq := httptest.NewRequest(http.MethodGet, "/assets/app-abc.js", nil)
	jsRec := httptest.NewRecorder()
	handler.ServeHTTP(jsRec, jsReq)
	if jsRec.Code != http.StatusOK {
		t.Fatalf("GET /assets/app-abc.js = %d, want 200", jsRec.Code)
	}
	if ct := jsRec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("JS Content-Type = %q, want javascript", ct)
	}
	if cc := jsRec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Fatalf("JS Cache-Control = %q, want immutable max-age=31536000", cc)
	}

	cssReq := httptest.NewRequest(http.MethodGet, "/assets/app-abc.css", nil)
	cssRec := httptest.NewRecorder()
	handler.ServeHTTP(cssRec, cssReq)
	if cssRec.Code != http.StatusOK {
		t.Fatalf("GET /assets/app-abc.css = %d, want 200", cssRec.Code)
	}
	if ct := cssRec.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Fatalf("CSS Content-Type = %q, want text/css", ct)
	}
	if cc := cssRec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("CSS Cache-Control = %q, want immutable", cc)
	}

	for _, missing := range []string{"/assets/missing.js", "/nonexistent.js", "/styles.css"} {
		req := httptest.NewRequest(http.MethodGet, missing, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", missing, rec.Code)
		}
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, apiPath := range []string{"/v1/nonexistent", "/agents"} {
			req := httptest.NewRequest(method, apiPath, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want 404", method, apiPath, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
				t.Fatalf("%s %s leaked text/html Content-Type: %q", method, apiPath, ct)
			}
		}
	}

	spaOnly := staticSPAHandler(mockFS)
	for _, apiPath := range []string{"/v1", "/v1/nonexistent", "/agents"} {
		req := httptest.NewRequest(http.MethodPost, apiPath, nil)
		rec := httptest.NewRecorder()
		spaOnly.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("staticSPAHandler POST %s = %d, want 404", apiPath, rec.Code)
		}
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/"},
		{http.MethodPut, "/assets/app-abc.js"},
		{http.MethodDelete, "/overview"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s = %d, want 405", tc.method, tc.path, rec.Code)
		}
	}

	for _, trav := range []string{"/../etc/passwd", "/assets/../../etc/passwd"} {
		req := httptest.NewRequest(http.MethodGet, trav, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusInternalServerError || strings.Contains(rec.Body.String(), "root:x:") {
			t.Fatalf("traversal %s returned %d body=%s", trav, rec.Code, rec.Body.String())
		}
	}
}

func TestEmbeddedWebDistHasAssetsAndNoExternalCDNs(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	staticFS, err := fs.Sub(webassets.Dist, "dist")
	if err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{StaticFS: staticFS})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html", ct)
	}
	html := rec.Body.String()
	if !strings.Contains(html, "LanTally") {
		t.Fatalf("GET / missing LanTally title")
	}

	assetRe := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
	matches := assetRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		t.Fatalf("GET / HTML has no /assets/* references:\n%s", html)
	}

	forbidden := []string{"fonts.googleapis.com", "fonts.gstatic.com", "unpkg.com", "cdn.jsdelivr.net"}
	for _, f := range forbidden {
		if strings.Contains(html, f) {
			t.Fatalf("GET / HTML contains forbidden CDN string %q", f)
		}
	}

	var sawJS, sawCSS bool
	for _, m := range matches {
		assetPath := m[1]
		aReq := httptest.NewRequest(http.MethodGet, assetPath, nil)
		aRec := httptest.NewRecorder()
		handler.ServeHTTP(aRec, aReq)
		if aRec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", assetPath, aRec.Code)
		}
		if aRec.Body.Len() == 0 {
			t.Fatalf("GET %s returned empty body", assetPath)
		}
		ct := aRec.Header().Get("Content-Type")
		body := aRec.Body.String()
		if strings.HasSuffix(assetPath, ".js") {
			sawJS = true
			if !strings.Contains(ct, "javascript") {
				t.Fatalf("GET %s Content-Type = %q, want javascript", assetPath, ct)
			}
		}
		if strings.HasSuffix(assetPath, ".css") {
			sawCSS = true
			if !strings.Contains(ct, "text/css") {
				t.Fatalf("GET %s Content-Type = %q, want text/css", assetPath, ct)
			}
		}
		for _, f := range forbidden {
			if strings.Contains(body, f) {
				t.Fatalf("asset %s contains forbidden CDN string %q", assetPath, f)
			}
		}
	}
	if !sawJS || !sawCSS {
		t.Fatalf("expected both .js and .css in /assets/* references, sawJS=%v sawCSS=%v", sawJS, sawCSS)
	}
}

func TestAgentBinaryRejectsPathTraversal(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	staticFS, err := fs.Sub(webassets.Dist, "dist")
	if err != nil {
		t.Fatal(err)
	}
	handler := Routes(store, Config{
		StaticFS: staticFS,
		AgentDir: t.TempDir(),
	})

	for _, p := range []string{"/agents/../../etc/passwd", "/agents/linux/mips"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}

func TestDeviceMergeRejectsSelfMergeAndPinConflictAndUnmerge404(t *testing.T) {
	store, handler, cookie := setupAuthedServer(t, Config{})
	ctx := context.Background()

	enrollRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/enroll", `{"site_id":"site-m","node_id":"node-m"}`)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", enrollRec.Code, enrollRec.Body.String())
	}
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(enrollRec.Body.Bytes(), &issued)

	b := protocol.Batch{
		ProtocolVersion: 1,
		SiteID:          "site-m",
		NodeID:          "node-m",
		BootID:          "boot-m",
		Sequence:        1,
		SampledAt:       time.Now().UTC(),
		IntervalMS:      15000,
		Capabilities:    []protocol.Capability{protocol.CapNlbwmon},
		Devices: []protocol.DeviceDelta{
			{ObsIP: "203.0.113.10", ObsMAC: "02:00:00:00:00:10", RxDelta: 500, TxDelta: 100, Source: protocol.SourceNlbwmon},
			{ObsIP: "203.0.113.11", ObsMAC: "02:00:00:00:00:11", RxDelta: 300, TxDelta: 100, Source: protocol.SourceNlbwmon},
		},
	}
	raw, _ := protocol.Encode(b)
	ingReq := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw))
	ingReq.Header.Set("Authorization", "Bearer "+issued.Token)
	ingReq.Header.Set("Content-Encoding", "gzip")
	ingReq.Header.Set("Content-Type", "application/json")
	ingRec := httptest.NewRecorder()
	handler.ServeHTTP(ingRec, ingReq)
	if ingRec.Code != http.StatusOK {
		t.Fatalf("ingest 1 = %d %s", ingRec.Code, ingRec.Body.String())
	}

	devsRec := doAuthedJSON(handler, cookie, http.MethodGet, "/v1/devices", "")
	var devsBody struct {
		Devices []sqlitestore.DeviceLedger `json:"devices"`
	}
	_ = json.Unmarshal(devsRec.Body.Bytes(), &devsBody)
	if len(devsBody.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %+v", devsBody.Devices)
	}
	devA, devB := devsBody.Devices[0].ID, devsBody.Devices[1].ID

	// 1. Self-merge returns 400 and does NOT break subsequent ingest.
	selfRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/devices/"+devA+"/merge", `{"other_id":"`+devA+`"}`)
	if selfRec.Code != http.StatusBadRequest {
		t.Fatalf("self-merge = %d %s, want 400", selfRec.Code, selfRec.Body.String())
	}
	b.Sequence = 2
	raw2, _ := protocol.Encode(b)
	ingReq2 := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw2))
	ingReq2.Header.Set("Authorization", "Bearer "+issued.Token)
	ingReq2.Header.Set("Content-Encoding", "gzip")
	ingReq2.Header.Set("Content-Type", "application/json")
	ingRec2 := httptest.NewRecorder()
	handler.ServeHTTP(ingRec2, ingReq2)
	if ingRec2.Code != http.StatusOK {
		t.Fatalf("subsequent ingest after self-merge attempt = %d %s, want 200", ingRec2.Code, ingRec2.Body.String())
	}

	// 2. Missing device merge returns 404.
	missRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/devices/dev_missing/merge", `{"other_id":"`+devB+`"}`)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("missing device merge = %d %s, want 404", missRec.Code, missRec.Body.String())
	}

	// 3. Unmerge before merge returns 404.
	unmerge404 := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/devices/"+devA+"/unmerge", `{}`)
	if unmerge404.Code != http.StatusNotFound {
		t.Fatalf("unmerge before merge = %d %s, want 404", unmerge404.Code, unmerge404.Body.String())
	}

	// 4. Conflicting pins return 409 Conflict.
	now := time.Now().UTC()
	if err := store.PinIdentity("site-m", devA, "pin-alpha", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PinIdentity("site-m", devB, "pin-beta", now); err != nil {
		t.Fatal(err)
	}
	pinConflict := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/devices/"+devA+"/merge", `{"other_id":"`+devB+`"}`)
	if pinConflict.Code != http.StatusConflict {
		t.Fatalf("pin conflict merge = %d %s, want 409", pinConflict.Code, pinConflict.Body.String())
	}

	// 5. Merge fresh devices devC & devD, then unmerge -> 200, second unmerge -> 404.
	b.Sequence = 3
	b.Devices = []protocol.DeviceDelta{
		{ObsIP: "203.0.113.12", ObsMAC: "02:00:00:00:00:12", RxDelta: 100, TxDelta: 50, Source: protocol.SourceNlbwmon},
		{ObsIP: "203.0.113.13", ObsMAC: "02:00:00:00:00:13", RxDelta: 100, TxDelta: 50, Source: protocol.SourceNlbwmon},
	}
	raw3, _ := protocol.Encode(b)
	ingReq3 := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw3))
	ingReq3.Header.Set("Authorization", "Bearer "+issued.Token)
	ingReq3.Header.Set("Content-Encoding", "gzip")
	ingReq3.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), ingReq3)

	resv := identity.NewResolver(store, "node-m")
	devC, _, _ := resv.Resolve("site-m", b.Devices[0], now)
	devD, _, _ := resv.Resolve("site-m", b.Devices[1], now)

	mergeOK := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/devices/"+devC+"/merge", `{"other_id":"`+devD+`"}`)
	if mergeOK.Code != http.StatusOK {
		t.Fatalf("valid merge = %d %s", mergeOK.Code, mergeOK.Body.String())
	}
	unmergeOK := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/devices/"+devC+"/unmerge", `{}`)
	if unmergeOK.Code != http.StatusOK {
		t.Fatalf("valid unmerge = %d %s", unmergeOK.Code, unmergeOK.Body.String())
	}
	unmergeAgain := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/devices/"+devC+"/unmerge", `{}`)
	if unmergeAgain.Code != http.StatusNotFound {
		t.Fatalf("second unmerge = %d %s, want 404", unmergeAgain.Code, unmergeAgain.Body.String())
	}
	_ = ctx
}

func TestRevokeNodeResolvesSilenceAlert(t *testing.T) {
	store, handler, cookie := setupAuthedServer(t, Config{})

	enrollRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/enroll", `{"site_id":"site-s","node_id":"node-sil"}`)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", enrollRec.Code, enrollRec.Body.String())
	}
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(enrollRec.Body.Bytes(), &issued)

	// Touch node and raise silence alert.
	if err := store.TouchNode(context.Background(), "node-sil", "boot-s", 200*time.Millisecond, time.Now().UTC().Add(-2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.RaiseAlert(context.Background(), alert.Record{
		Kind:        alert.KindSilence,
		SiteID:      "site-s",
		NodeID:      "node-sil",
		Message:     "node stopped reporting within expected interval window",
		ObservedAt:  time.Now().UTC(),
		Fingerprint: "silence|node-sil",
	}); err != nil {
		t.Fatal(err)
	}

	before := doAuthedJSON(handler, cookie, http.MethodGet, "/v1/alerts", "")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"silence"`) {
		t.Fatalf("expected silence alert before revoke, got %d %s", before.Code, before.Body.String())
	}

	revokeRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/nodes/node-sil/revoke", `{}`)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", revokeRec.Code, revokeRec.Body.String())
	}

	after := doAuthedJSON(handler, cookie, http.MethodGet, "/v1/alerts", "")
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"alerts":[]`) {
		t.Fatalf("expected open alerts cleared after revoke, got %d %s", after.Code, after.Body.String())
	}
}

func TestLoginRateLimiterIgnoresUntrustedXForwardedFor(t *testing.T) {
	_, handler, _ := setupAuthedServer(t, Config{})

	// Non-loopback untrusted peer (203.0.113.10:1234) attempts 5 bad logins.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"wrong"}`))
		req.RemoteAddr = "203.0.113.10:1234"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, rec.Code)
		}
	}

	// 6th attempt from same untrusted peer spoofing X-Forwarded-For must STILL be rate-limited (429).
	spoofReq := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"test-admin-password"}`))
	spoofReq.RemoteAddr = "203.0.113.10:1234"
	spoofReq.Header.Set("Content-Type", "application/json")
	spoofReq.Header.Set("X-Forwarded-For", "198.51.100.77")
	spoofRec := httptest.NewRecorder()
	handler.ServeHTTP(spoofRec, spoofReq)
	if spoofRec.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed XFF from untrusted peer = %d, want 429", spoofRec.Code)
	}

	// Trusted loopback proxy honoring XFF for a different client IP succeeds.
	loopReq := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"password":"test-admin-password"}`))
	loopReq.RemoteAddr = "127.0.0.1:4321"
	loopReq.Header.Set("Content-Type", "application/json")
	loopReq.Header.Set("X-Forwarded-For", "198.51.100.88")
	loopRec := httptest.NewRecorder()
	handler.ServeHTTP(loopRec, loopReq)
	if loopRec.Code != http.StatusOK {
		t.Fatalf("trusted loopback XFF = %d, want 200", loopRec.Code)
	}
}

func TestLogoutChangePasswordAndZeroRuleMultiplier(t *testing.T) {
	_, handler, cookie := setupAuthedServer(t, Config{})

	// 1. Change password error paths and success.
	wrongCur := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/settings/password", `{"current":"wrong-pass","password":"new-password-1"}`)
	if wrongCur.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password = %d, want 401", wrongCur.Code)
	}
	shortNew := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/settings/password", `{"current":"test-admin-password","password":"short"}`)
	if shortNew.Code != http.StatusBadRequest {
		t.Fatalf("short new password = %d, want 400", shortNew.Code)
	}
	okPW := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/settings/password", `{"current":"test-admin-password","password":"updated-password-1"}`)
	if okPW.Code != http.StatusOK {
		t.Fatalf("valid password update = %d %s", okPW.Code, okPW.Body.String())
	}

	// 2. Zero-factor rule (0.0x free node) via /v1/proxy/rules succeeds and legacy /v1/proxy/multipliers rejects <=0.
	rule0 := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/proxy/rules", `{"name":"FreeNode","match_type":"exact","pattern":"Free-01","factor":0,"priority":100,"enabled":true}`)
	if rule0.Code != http.StatusOK {
		t.Fatalf("POST 0x rule = %d %s", rule0.Code, rule0.Body.String())
	}
	legacy0 := doAuthedJSON(handler, cookie, http.MethodPut, "/v1/proxy/multipliers", `{"name":"Free-01","factor":0}`)
	if legacy0.Code != http.StatusBadRequest {
		t.Fatalf("legacy PUT multiplier 0 = %d, want 400", legacy0.Code)
	}

	// 3. Logout invalidates session cookie.
	logoutRec := doAuthedJSON(handler, cookie, http.MethodPost, "/v1/logout", "")
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", logoutRec.Code, logoutRec.Body.String())
	}
	afterLogout := doAuthedJSON(handler, cookie, http.MethodGet, "/v1/overview", "")
	if afterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("overview after logout = %d, want 401", afterLogout.Code)
	}
}
