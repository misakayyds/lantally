package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/enroll"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestBackupCreatesCopy(t *testing.T) {
	store := openTestStore(t)
	path, err := store.Backup()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestAdminCredentialRoundTrip(t *testing.T) {
	store := openTestStore(t)
	if err := store.CreateAdminCredential("test-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthenticateAdmin("test-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthenticateAdmin("wrong"); err == nil {
		t.Fatal("expected unauthorized for wrong password")
	}
}

func TestOpenMigratesLegacyNodesTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE nodes (
			id TEXT PRIMARY KEY,
			site_id TEXT NOT NULL,
			token_hash BLOB NOT NULL,
			revoked INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	_, err = legacy.Exec(
		`INSERT INTO nodes (id, site_id, token_hash) VALUES (?, ?, ?)`,
		"legacy-node",
		"site-a",
		enroll.HashToken(tokenFor("legacy-cred", 'l')),
	)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	token := tokenFor("migrated-cred", 'm')
	if err := store.CreateNode(
		context.Background(),
		"migrated-node",
		"site-a",
		"migrated-cred",
		enroll.HashToken(token),
	); err != nil {
		t.Fatal(err)
	}
	node, err := store.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if node.ID != "migrated-node" {
		t.Fatalf("authenticated node = %q, want migrated-node", node.ID)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("authenticate after idempotent reopen: %v", err)
	}
}

func tokenFor(credentialID string, fill byte) string {
	return "lt_" + credentialID + "_" + strings.Repeat(string(fill), 32)
}

func createTestNode(t *testing.T, store *Store, nodeID, credentialID string, fill byte) string {
	t.Helper()
	token := tokenFor(credentialID, fill)
	if err := store.CreateNode(
		context.Background(),
		nodeID,
		"site-a",
		credentialID,
		enroll.HashToken(token),
	); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestInsertBatchDeduplicatesAndPreservesHash(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	createTestNode(t, store, "node-a", "cred-a", 'a')

	raw := []byte(`{"synthetic":"payload"}`)
	inserted, err := store.InsertBatch(ctx, "node-a", "boot-a", 1, raw)
	if err != nil || !inserted {
		t.Fatalf("first insert: inserted=%v err=%v", inserted, err)
	}
	inserted, err = store.InsertBatch(ctx, "node-a", "boot-a", 1, raw)
	if err != nil || inserted {
		t.Fatalf("duplicate insert: inserted=%v err=%v", inserted, err)
	}

	count, err := store.BatchCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one row, got %d", count)
	}
	wantHash := sha256.Sum256(raw)
	gotHash, err := store.BatchPayloadHash(ctx, "node-a", "boot-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("payload hash = %q, want %q", gotHash, hex.EncodeToString(wantHash[:]))
	}
}

func TestInsertBatchRejectsSequenceRegressionWithinBoot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	createTestNode(t, store, "node-a", "cred-a", 'a')
	if _, err := store.InsertBatch(ctx, "node-a", "boot-a", 2, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertBatch(ctx, "node-a", "boot-a", 1, []byte("one")); !errors.Is(err, ErrSequenceRegression) {
		t.Fatalf("expected ErrSequenceRegression, got %v", err)
	}
}

func TestInsertBatchAllowsNewBootAtSequenceOne(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	createTestNode(t, store, "node-a", "cred-a", 'a')
	if _, err := store.InsertBatch(ctx, "node-a", "boot-a", 8, []byte("old boot")); err != nil {
		t.Fatal(err)
	}
	inserted, err := store.InsertBatch(ctx, "node-a", "boot-b", 1, []byte("new boot"))
	if err != nil || !inserted {
		t.Fatalf("new boot insert: inserted=%v err=%v", inserted, err)
	}
}

func TestAuthenticateRejectsRevokedToken(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	token := createTestNode(t, store, "node-a", "cred-a", 'a')
	node, err := store.Authenticate(ctx, token)
	if err != nil || node.ID != "node-a" || node.SiteID != "site-a" {
		t.Fatalf("authenticate: node=%+v err=%v", node, err)
	}
	if err := store.RevokeNode(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAuthenticateOnlyChecksHashSelectedByCredentialID(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	createTestNode(t, store, "node-a", "cred-a", 'a')

	forged := tokenFor("cred-a", 'b')
	if err := store.CreateNode(
		ctx,
		"node-b",
		"site-a",
		"cred-b",
		enroll.HashToken(forged),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Authenticate(ctx, forged); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected credential-id-selected hash rejection, got %v", err)
	}
}

func TestApplyLedgerOnceIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	increments := []accounting.Increment{{
		Class: accounting.ClassTotal,
		Rx:    100,
		Tx:    50,
	}}
	first, err := store.ApplyLedgerOnce(ctx, "site-a", "node-a", "boot-a", 1, time.Now().UTC(), increments)
	if err != nil || !first {
		t.Fatalf("first apply = %v %v", first, err)
	}
	second, err := store.ApplyLedgerOnce(ctx, "site-a", "node-a", "boot-a", 1, time.Now().UTC(), increments)
	if err != nil || second {
		t.Fatalf("second apply = %v %v, want not applied", second, err)
	}
	totals, err := store.LedgerTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if totals[accounting.ClassTotal] != 150 {
		t.Fatalf("total = %d, want 150", totals[accounting.ClassTotal])
	}
}

func TestApplyLedgerOnceStoresOutboundBreakdown(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	sampledAt := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 1, sampledAt, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 100, Tx: 20},
		{Class: accounting.ClassProxyRaw, Outbound: "proxy-a", Rx: 16, Tx: 4},
		{Class: accounting.ClassProxyRaw, Outbound: "proxy-b", Rx: 30, Tx: 10},
	}); err != nil {
		t.Fatal(err)
	}

	totals, err := store.LedgerTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if totals[accounting.ClassProxyRaw] != 60 {
		t.Fatalf("proxy_raw total = %d, want sum of outbounds 60", totals[accounting.ClassProxyRaw])
	}
	if totals[accounting.ClassTotal] != 120 {
		t.Fatalf("total = %d, want 120 (not proxy_raw)", totals[accounting.ClassTotal])
	}

	rows, err := store.db.QueryContext(ctx, `SELECT outbound, rx + tx FROM ledger_totals WHERE class = ? AND device_id = '' ORDER BY outbound`, accounting.ClassProxyRaw)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var outbound string
		var bytes int64
		if err := rows.Scan(&outbound, &bytes); err != nil {
			t.Fatal(err)
		}
		got[outbound] = bytes
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["proxy-a"] != 20 || got["proxy-b"] != 40 || len(got) != 2 {
		t.Fatalf("stored outbound rows = %+v", got)
	}

	series, err := store.TrafficSeries(ctx, TrafficQuery{
		From:          sampledAt.Add(-time.Hour),
		To:            sampledAt.Add(time.Hour),
		BucketSeconds: 1800,
		Class:         accounting.ClassProxyRaw,
		Group:         "outbound",
	})
	if err != nil {
		t.Fatal(err)
	}
	if series.Totals["proxy-a"] != 20 || series.Totals["proxy-b"] != 40 {
		t.Fatalf("traffic outbound totals = %+v", series.Totals)
	}
}

func TestTrafficSeriesBucketsNodeTotals(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	t1 := time.Date(2026, 8, 19, 4, 5, 0, 0, time.UTC)
	t2 := time.Date(2026, 8, 19, 4, 40, 0, 0, time.UTC)
	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 1, t1, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 1000, Tx: 500},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 2, t2, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 200, Tx: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "home", "dns-21", "boot-b", 1, t1, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 50, Tx: 25},
	}); err != nil {
		t.Fatal(err)
	}

	series, err := store.TrafficSeries(ctx, TrafficQuery{
		From:          t1.Add(-time.Hour),
		To:            t2.Add(time.Hour),
		BucketSeconds: 1800,
		Class:         accounting.ClassTotal,
		Group:         "node",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Keys) != 2 {
		t.Fatalf("keys = %+v, want two nodes", series.Keys)
	}
	if series.Totals["proxy-20"] != 1800 || series.Totals["dns-21"] != 75 {
		t.Fatalf("totals = %+v", series.Totals)
	}
	got := map[int64]uint64{}
	for _, point := range series.Points {
		if point.Values["proxy-20"] > 0 {
			got[point.Bucket.Unix()] = point.Values["proxy-20"]
		}
	}
	firstBucket := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC).Unix()
	secondBucket := time.Date(2026, 8, 19, 4, 30, 0, 0, time.UTC).Unix()
	if got[firstBucket] != 1500 || got[secondBucket] != 300 {
		t.Fatalf("proxy-20 buckets = %+v", got)
	}
}
