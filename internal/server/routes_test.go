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

	"github.com/misakayyds/lantally/internal/enroll"
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
