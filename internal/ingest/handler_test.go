package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const testToken = "synthetic-node-token"

func testServer(t *testing.T) (*sqlitestore.Store, http.Handler) {
	t.Helper()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateNode(
		context.Background(),
		"sim-node",
		"sim-site",
		enroll.HashToken(testToken),
	); err != nil {
		t.Fatal(err)
	}
	return store, Routes(store)
}

func postBatch(t *testing.T, handler http.Handler, token string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestIngestRetryStoresOneBatchAndStablePayloadHash(t *testing.T) {
	store, handler := testServer(t)
	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}

	first := postBatch(t, handler, testToken, raw)
	if first.Code != http.StatusOK || first.Body.String() != "{\"status\":\"ok\",\"duplicate\":false}\n" {
		t.Fatalf("first response: status=%d body=%q", first.Code, first.Body.String())
	}
	second := postBatch(t, handler, testToken, raw)
	if second.Code != http.StatusOK || second.Body.String() != "{\"status\":\"ok\",\"duplicate\":true}\n" {
		t.Fatalf("retry response: status=%d body=%q", second.Code, second.Body.String())
	}

	count, err := store.BatchCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one stored batch, got %d", count)
	}
	sum := sha256.Sum256(raw)
	hash, err := store.BatchPayloadHash(context.Background(), "sim-node", "boot-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("payload hash = %q, want %q", hash, hex.EncodeToString(sum[:]))
	}
}

func TestIngestAcceptsRawJSON(t *testing.T) {
	_, handler := testServer(t)
	raw, err := json.Marshal(sim.Snapshot(1, "boot-raw"))
	if err != nil {
		t.Fatal(err)
	}
	rec := postBatch(t, handler, testToken, raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestIngestRejectsBadAndRevokedTokens(t *testing.T) {
	store, handler := testServer(t)
	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, "wrong-token", raw); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status=%d body=%q", rec.Code, rec.Body.String())
	}
	if err := store.RevokeNode(context.Background(), "sim-node"); err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, testToken, raw); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestIngestRejectsProtocolV2(t *testing.T) {
	_, handler := testServer(t)
	batch := sim.Snapshot(1, "boot-a")
	batch.ProtocolVersion = 2
	raw, err := protocol.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, testToken, raw); rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestIngestRejectsSequenceRegression(t *testing.T) {
	_, handler := testServer(t)
	for _, seq := range []uint64{2, 1} {
		raw, err := protocol.Encode(sim.Snapshot(seq, "boot-a"))
		if err != nil {
			t.Fatal(err)
		}
		rec := postBatch(t, handler, testToken, raw)
		want := http.StatusOK
		if seq == 1 {
			want = http.StatusConflict
		}
		if rec.Code != want {
			t.Fatalf("sequence %d: status=%d body=%q", seq, rec.Code, rec.Body.String())
		}
	}
}

func TestIngestReturnsEmpty503WhenSQLiteUnavailable(t *testing.T) {
	store, handler := testServer(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}
	rec := postBatch(t, handler, testToken, raw)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no ACK body, got %q", rec.Body.String())
	}
}

func TestIngestRejectsCredentialIdentityMismatch(t *testing.T) {
	_, handler := testServer(t)
	batch := sim.Snapshot(1, "boot-a")
	batch.NodeID = "other-node"
	raw, err := protocol.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	rec := postBatch(t, handler, testToken, raw)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHealthzChecksSQLite(t *testing.T) {
	store, handler := testServer(t)
	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return rec
	}
	if rec := request(); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthy response: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if rec := request(); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy response: status=%d body=%q", rec.Code, rec.Body.String())
	}
}
