package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
