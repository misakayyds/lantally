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

func TestOpenBacksUpWhenSchemaBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lantally.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`PRAGMA user_version = 10`); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	matches, err := filepath.Glob(filepath.Join(dir, "lantally.db.bak-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("pre-migration backups = %v, want 1", matches)
	}
}

func TestUpdateAdminPassword(t *testing.T) {
	store := openTestStore(t)
	if err := store.CreateAdminCredential("old-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAdminPassword("wrong", "new-password"); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("wrong current = %v, want unauthorized", err)
	}
	if err := store.UpdateAdminPassword("old-password", "short"); err == nil {
		t.Fatal("expected short password error")
	}
	if err := store.UpdateAdminPassword("old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthenticateAdmin("old-password"); err == nil {
		t.Fatal("old password should stop working")
	}
	if err := store.AuthenticateAdmin("new-password"); err != nil {
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

func TestDefaultBucketSecondsMatchesRange(t *testing.T) {
	to := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if got := DefaultBucketSeconds(to.Add(-24*time.Hour), to); got != 1800 {
		t.Fatalf("24h bucket = %d, want 1800", got)
	}
	if got := DefaultBucketSeconds(to.Add(-72*time.Hour), to); got != 1800 {
		t.Fatalf("72h bucket = %d, want 1800", got)
	}
	if got := DefaultBucketSeconds(to.Add(-7*24*time.Hour), to); got != 7200 {
		t.Fatalf("7d bucket = %d, want 7200", got)
	}
	if got := DefaultBucketSeconds(to.Add(-30*24*time.Hour), to); got != 86400 {
		t.Fatalf("30d bucket = %d, want 86400", got)
	}
}

func TestRollupDailyIsIdempotentAndSurvivesSamplePurge(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-20 * 24 * time.Hour)
	recent := now.Add(-2 * time.Hour)

	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 1, old, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 1000, Tx: 500},
		{Class: accounting.ClassProxyRaw, Outbound: "ss-test", Rx: 200, Tx: 50},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 2, recent, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 80, Tx: 20},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.RollupDaily(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupDaily(ctx, now); err != nil {
		t.Fatal(err)
	}

	day := time.Date(old.Year(), old.Month(), old.Day(), 0, 0, 0, 0, time.UTC).Unix()
	var rx, tx int64
	err := store.db.QueryRowContext(
		ctx,
		`SELECT rx, tx FROM ledger_daily
		 WHERE day = ? AND node_id = ? AND device_id = '' AND class = ? AND outbound = ''`,
		day, "proxy-20", accounting.ClassTotal,
	).Scan(&rx, &tx)
	if err != nil {
		t.Fatal(err)
	}
	if rx != 1000 || tx != 500 {
		t.Fatalf("daily total = rx=%d tx=%d, want 1000/500 after idempotent rollup", rx, tx)
	}

	if err := store.PurgeSamples(ctx, now); err != nil {
		t.Fatal(err)
	}

	series, err := store.TrafficSeries(ctx, TrafficQuery{
		From: now.Add(-30 * 24 * time.Hour),
		To:   now,
		Group: "node",
		Class: accounting.ClassTotal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if series.BucketSeconds != 86400 {
		t.Fatalf("30d bucket = %d, want 86400", series.BucketSeconds)
	}
	if series.Totals["proxy-20"] != 1600 {
		t.Fatalf("30d totals = %+v, want old 1500 + recent 100", series.Totals)
	}

	short, err := store.TrafficSeries(ctx, TrafficQuery{
		From: now.Add(-72 * time.Hour),
		To:   now,
		Group: "node",
		Class: accounting.ClassTotal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if short.BucketSeconds != 1800 {
		t.Fatalf("72h bucket = %d, want 1800", short.BucketSeconds)
	}
	if short.Totals["proxy-20"] != 100 {
		t.Fatalf("72h after purge should keep recent samples only, got %+v", short.Totals)
	}
}

func TestTrafficSeriesFiltersNodeAndDevice(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 1, at, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 100, Tx: 0},
		{DeviceID: "dev-a", Class: accounting.ClassTotal, Rx: 40, Tx: 0},
		{DeviceID: "dev-b", Class: accounting.ClassTotal, Rx: 60, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "home", "dns-21", "boot-b", 1, at, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 9, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}

	byNode, err := store.TrafficSeries(ctx, TrafficQuery{
		From: at.Add(-time.Hour), To: at.Add(time.Hour),
		Group: "node", Class: accounting.ClassTotal, NodeID: "proxy-20",
	})
	if err != nil {
		t.Fatal(err)
	}
	if byNode.Totals["proxy-20"] != 100 || byNode.Totals["dns-21"] != 0 {
		t.Fatalf("node filter = %+v", byNode.Totals)
	}

	byDevice, err := store.TrafficSeries(ctx, TrafficQuery{
		From: at.Add(-time.Hour), To: at.Add(time.Hour),
		Group: "device", Class: accounting.ClassTotal, DeviceID: "dev-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if byDevice.Totals["dev-a"] != 40 || byDevice.Totals["dev-b"] != 0 {
		t.Fatalf("device filter = %+v", byDevice.Totals)
	}
}

func TestMultiplierConfigRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.SetMultiplier(ctx, "ss-test", 1.5); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListMultipliers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["ss-test"] != 1.5 {
		t.Fatalf("multipliers = %+v", got)
	}

	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 1, time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC), []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "ss-test", Rx: 100, Tx: 0},
		{Class: accounting.ClassProxyRaw, Outbound: "unknown", Rx: 40, Tx: 0},
		{Class: accounting.ClassProxyUnadjusted, Outbound: "unknown", Rx: 40, Tx: 0},
		{Class: accounting.ClassProxyAdjusted, Outbound: "ss-test", Rx: 150, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListOutboundLedgers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]OutboundLedger{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if byName["ss-test"].Raw != 100 || byName["ss-test"].Adjusted != 150 || !byName["ss-test"].Configured || byName["ss-test"].Factor != 1.5 {
		t.Fatalf("ss-test = %+v", byName["ss-test"])
	}
	if byName["unknown"].Raw != 40 || byName["unknown"].Configured || byName["unknown"].Unadjusted != 40 {
		t.Fatalf("unknown = %+v", byName["unknown"])
	}
}

func TestRenameDeviceUpdatesDisplayName(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO devices (id, site_id, created_at) VALUES (?, ?, ?)`, "dev-a", "home", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameDevice(ctx, "dev-a", "书房电脑"); err != nil {
		t.Fatal(err)
	}
	devices, err := store.ListDeviceLedgers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Name != "书房电脑" {
		t.Fatalf("devices = %+v", devices)
	}
}

func TestEvaluateSilenceAcknowledgedDoesNotRetrigger(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if err := store.CreateNode(ctx, "proxy-20", "home", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchNode(ctx, "proxy-20", "boot-a", 15*time.Minute, now.Add(-46*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	open, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Kind != "silence" || open[0].NodeID != "proxy-20" {
		t.Fatalf("open alerts = %+v", open)
	}
	if err := store.AckAlert(ctx, open[0].ID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	open, err = store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("acked silence retriggered: %+v", open)
	}
}

func TestEvaluateGrowthUsesDailyMedian(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if err := store.CreateNode(ctx, "proxy-20", "home", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 7; i++ {
		day := now.AddDate(0, 0, -i)
		if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", uint64(i), day, []accounting.Increment{
			{Class: accounting.ClassTotal, Rx: 50, Tx: 50},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-b", 8, now, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 200, Tx: 200},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupDaily(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	open, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Kind != "growth" {
		t.Fatalf("growth alerts = %+v", open)
	}
}

func TestBillingReconciliationComputesRatioAndDrift(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if err := store.SetBilling(ctx, Billing{ResetDay: 1, ProviderBytes: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 1, now, []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "ss-test", Rx: 120, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupDaily(ctx, now); err != nil {
		t.Fatal(err)
	}
	status, err := store.BillingStatus(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if status.LocalBytes != 120 || status.ProviderBytes != 100 || status.Ratio < 0.19 || status.Ratio > 0.21 {
		t.Fatalf("billing status = %+v", status)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	open, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Kind != "drift" {
		t.Fatalf("drift alerts = %+v", open)
	}
}

func TestMigration0012ConvertsLegacyMultipliersOnce(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "lantally.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.CreateNode(ctx, "node-a", "site-a", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-0012 (user_version = 11) DB with outbound_multipliers and no 0012 tables.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS multiplier_rules`,
		`DROP TABLE IF EXISTS dimension_samples`,
		`DROP TABLE IF EXISTS dimension_daily`,
		`DROP TABLE IF EXISTS proxy_node_catalog`,
		`INSERT INTO outbound_multipliers (name, factor, updated_at) VALUES ('ss-legacy', 1.5, '2026-08-18T00:00:00Z')`,
		`PRAGMA user_version = 11`,
	} {
		if _, err := rawDB.Exec(stmt); err != nil {
			_ = rawDB.Close()
			t.Fatal(err)
		}
	}
	if err := rawDB.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "lantally.db.bak-*"))
	if err != nil || len(matches) == 0 {
		_ = migrated.Close()
		t.Fatalf("expected pre-migration backup, got %v err=%v", matches, err)
	}

	rules, err := migrated.ListMultiplierRules(ctx)
	if err != nil {
		_ = migrated.Close()
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].MatchType != accounting.MatchExact || rules[0].Pattern != "ss-legacy" || rules[0].Factor != 1.5 {
		_ = migrated.Close()
		t.Fatalf("migrated rules = %+v", rules)
	}
	node, err := migrated.GetNode(ctx, "node-a")
	if err != nil {
		_ = migrated.Close()
		t.Fatal(err)
	}
	if node.CollectDomains {
		_ = migrated.Close()
		t.Fatalf("collect_domains should default false")
	}

	// Deleting the migrated rule must NOT resurrect on reopen.
	if err := migrated.DeleteMultiplierRule(ctx, rules[0].ID); err != nil {
		_ = migrated.Close()
		t.Fatal(err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rulesAfter, err := reopened.ListMultiplierRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rulesAfter) != 0 {
		t.Fatalf("deleted rule resurrected after reopen: %+v", rulesAfter)
	}
}

func TestDimensionsAndClassIfaceIsolation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 19, 10, 15, 0, 0, time.UTC)

	applied, err := store.ApplyLedgerWithDimensionsOnce(
		ctx,
		"home",
		"proxy-20",
		"boot-a",
		1,
		at,
		[]accounting.Increment{
			{Class: accounting.ClassTotal, Rx: 1000, Tx: 500},
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 400, Tx: 100},
			{Class: accounting.ClassIface, Iface: "eth0", Rx: 1000, Tx: 500},
			{Class: accounting.ClassIface, Iface: "wg0", Rx: 200, Tx: 50},
		},
		[]DimensionIncrement{
			{Type: "group", Key: "PROXY", Rx: 400, Tx: 100},
			{Type: "process", Key: "curl", Rx: 300, Tx: 60},
			{Type: "domain", Key: "example.org", Rx: 150, Tx: 50},
			{Type: "iface", Key: "eth0", Rx: 9999, Tx: 9999}, // must not double-count ClassIface
		},
	)
	if err != nil || !applied {
		t.Fatalf("apply = (%v, %v)", applied, err)
	}

	outbounds, err := store.ListOutboundLedgers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 1 || outbounds[0].Name != "hk-01" {
		t.Fatalf("outbounds polluted by iface: %+v", outbounds)
	}

	ifaceStats, err := store.ListDimensionStats(ctx, DimensionQuery{
		Type: "iface",
		From: at.Add(-time.Hour),
		To:   at.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ifaceStats.TotalRx != 1200 || ifaceStats.TotalTx != 550 || ifaceStats.Total != 1750 {
		t.Fatalf("iface totals = %+v, want rx=1200 tx=550 total=1750", ifaceStats)
	}

	series, err := store.TrafficSeries(ctx, TrafficQuery{
		From:  at.Add(-time.Hour),
		To:    at.Add(time.Hour),
		Group: "process",
	})
	if err != nil {
		t.Fatal(err)
	}
	if series.RxTotals["curl"] != 300 || series.TxTotals["curl"] != 60 || series.Totals["curl"] != 360 {
		t.Fatalf("process series = %+v", series)
	}
}

func TestRollupAnd14DayBoundaryRetention(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	boundaryMorning := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -SampleRetentionDays).Add(6 * time.Hour)
	boundaryEvening := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -SampleRetentionDays).Add(18 * time.Hour)
	oldDay := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -20).Add(9 * time.Hour)

	for i, ts := range []time.Time{oldDay, boundaryMorning, boundaryEvening} {
		if _, err := store.ApplyLedgerWithDimensionsOnce(
			ctx,
			"home",
			"proxy-20",
			"boot-a",
			uint64(i+1),
			ts,
			[]accounting.Increment{{Class: accounting.ClassTotal, Rx: 100, Tx: 50}},
			[]DimensionIncrement{{Type: "process", Key: "curl", Rx: 60, Tx: 40}},
		); err != nil {
			t.Fatal(err)
		}
	}

	// Maintain twice at 12:00Z and 13:00Z on boundary day -> boundary day must NOT be partially purged or clobbered.
	if err := store.MaintainLedgers(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := store.MaintainLedgers(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	series, err := store.TrafficSeries(ctx, TrafficQuery{
		From:  oldDay.Add(-time.Hour),
		To:    now,
		Group: "process",
	})
	if err != nil {
		t.Fatal(err)
	}
	if series.RxTotals["curl"] != 180 || series.TxTotals["curl"] != 120 || series.Totals["curl"] != 300 {
		t.Fatalf("30d process totals after purge/rollup = %+v, want rx=180 tx=120 total=300", series.Totals)
	}

	// Short custom range > 14d ago must route to *_daily.
	oldCustom, err := store.TrafficSeries(ctx, TrafficQuery{
		From:  oldDay.Add(-6 * time.Hour),
		To:    oldDay.Add(6 * time.Hour),
		Group: "node",
		Class: accounting.ClassTotal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if oldCustom.Totals["proxy-20"] != 150 {
		t.Fatalf("short custom range >14d ago = %+v, want 150", oldCustom.Totals)
	}
}

func TestMultiplierRulesCRUDImportExport(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	created, err := store.CreateMultiplierRule(ctx, accounting.MultiplierRule{
		Name:      "hk-iepl",
		MatchType: accounting.MatchKeyword,
		Pattern:   "iepl",
		Factor:    2.0,
		Priority:  100,
		Enabled:   true,
	})
	if err != nil || created.ID == 0 {
		t.Fatalf("create rule = %+v err=%v", created, err)
	}
	updated, err := store.UpdateMultiplierRule(ctx, created.ID, accounting.MultiplierRule{
		Name:      "hk-iepl-v2",
		MatchType: accounting.MatchKeyword,
		Pattern:   "iepl",
		Factor:    3.0,
		Priority:  120,
		Enabled:   true,
	})
	if err != nil || updated.Factor != 3.0 || updated.Priority != 120 {
		t.Fatalf("update rule = %+v err=%v", updated, err)
	}

	// Atomic rollback on invalid import batch.
	if _, err := store.ImportMultiplierRules(ctx, "replace", []accounting.MultiplierRule{
		{Name: "valid", MatchType: accounting.MatchExact, Pattern: "n1", Factor: 2.0, Enabled: true},
		{Name: "broken", MatchType: accounting.MatchRegex, Pattern: "[", Factor: 2.0, Enabled: true},
	}); err == nil {
		t.Fatal("expected import with invalid regex to fail")
	}
	rulesAfterFail, err := store.ListMultiplierRules(ctx)
	if err != nil || len(rulesAfterFail) != 1 || rulesAfterFail[0].Name != "hk-iepl-v2" {
		t.Fatalf("failed import must not mutate existing rules: %+v err=%v", rulesAfterFail, err)
	}

	// Merge import adds new rule and updates matching pattern.
	merged, err := store.ImportMultiplierRules(ctx, "merge", []accounting.MultiplierRule{
		{Name: "hk-iepl-merged", MatchType: accounting.MatchKeyword, Pattern: "iepl", Factor: 2.5, Priority: 150, Enabled: true},
		{Name: "auto", MatchType: accounting.MatchAutoExtract, Factor: 1.0, Priority: 50, Enabled: true},
	})
	if err != nil || len(merged) != 2 {
		t.Fatalf("merge import = %+v err=%v", merged, err)
	}
}

func TestRetroactiveEvaluationAcrossAllQueries(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	if err := store.CreateNode(ctx, "proxy-20", "home", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO devices (id, site_id, created_at) VALUES ('dev-a', 'home', ?)`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBilling(ctx, Billing{ResetDay: 1, ProviderBytes: 150}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyLedgerOnce(ctx, "home", "proxy-20", "boot-a", 1, now.Add(-time.Hour), []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 140, Tx: 60},
		{Class: accounting.ClassProxyRaw, Outbound: "HK-IEPL-2x", Rx: 100, Tx: 50},
		{Class: accounting.ClassProxyRaw, Outbound: "US-Plain", Rx: 40, Tx: 10},
		{DeviceID: "dev-a", Class: accounting.ClassProxyRaw, Outbound: "HK-IEPL-2x", Rx: 100, Tx: 50},
	}); err != nil {
		t.Fatal(err)
	}

	// Initially no rules -> 1.0x default badge.
	initialOutbounds, err := store.ListOutboundLedgers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(initialOutbounds) != 2 || initialOutbounds[0].Configured || initialOutbounds[0].MatchSource != "default" || initialOutbounds[0].Factor != 1.0 {
		t.Fatalf("initial outbounds = %+v", initialOutbounds)
	}
	initialTotals, err := store.LedgerTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initialTotals[accounting.ClassProxyAdjusted] != 200 || initialTotals[accounting.ClassProxyUnadjusted] != 200 {
		t.Fatalf("initial totals = %+v, want adjusted=200 unadjusted=200", initialTotals)
	}

	// Add auto_extract rule -> retroactively recalculates HK-IEPL-2x as 2.0x (rx=200, tx=100) while US-Plain stays 1.0x default (rx=40, tx=10)!
	if _, err := store.CreateMultiplierRule(ctx, accounting.MultiplierRule{
		Name:      "auto-name-factor",
		MatchType: accounting.MatchAutoExtract,
		Factor:    1.0,
		Priority:  50,
		Enabled:   true,
	}); err != nil {
		t.Fatal(err)
	}

	breakdown, err := store.LedgerBreakdown(ctx, time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	adj := breakdown[accounting.ClassProxyAdjusted]
	unadj := breakdown[accounting.ClassProxyUnadjusted]
	if adj.Rx != 240 || adj.Tx != 110 || adj.Total != 350 {
		t.Fatalf("retroactive proxy_adjusted = %+v, want rx=240 tx=110 total=350", adj)
	}
	if unadj.Rx != 40 || unadj.Tx != 10 || unadj.Total != 50 {
		t.Fatalf("retroactive proxy_unadjusted = %+v, want rx=40 tx=10 total=50", unadj)
	}

	devices, err := store.ListDeviceLedgers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Breakdown[accounting.ClassProxyAdjusted].Total != 300 {
		t.Fatalf("retroactive device breakdown = %+v", devices)
	}

	series, err := store.TrafficSeries(ctx, TrafficQuery{
		From:  now.Add(-2 * time.Hour),
		To:    now,
		Group: "node",
		Class: "proxy_weighted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if series.RxTotals["proxy-20"] != 240 || series.TxTotals["proxy-20"] != 110 || series.Totals["proxy-20"] != 350 {
		t.Fatalf("retroactive traffic series = %+v", series)
	}

	billing, err := store.BillingStatus(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if billing.LocalBytes != 350 {
		t.Fatalf("retroactive billing local_bytes = %d, want 350", billing.LocalBytes)
	}
}

func TestIdleShortWindowsDoNotLeakEarlierSameUTCDayTraffic(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.CreateNode(ctx, "node-a", "home", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO devices (id, site_id, created_at) VALUES ('dev-a', 'home', '2026-08-18T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	// Traffic at 2026-08-18T10:00:00Z (18:00+08:00, before Asia/Shanghai midnight at 16:00Z).
	trafficAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx,
		"home",
		"node-a",
		"boot-a",
		1,
		trafficAt,
		[]accounting.Increment{
			{Class: accounting.ClassTotal, Rx: 1000, Tx: 500},
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 700, Tx: 300},
			{DeviceID: "dev-a", Class: accounting.ClassTotal, Rx: 1000, Tx: 500},
		},
		[]DimensionIncrement{
			{Type: "process", Key: "curl", Rx: 400, Tx: 100},
		},
	); err != nil {
		t.Fatal(err)
	}

	assertEmptyWindow := func(name string, from, to time.Time) {
		t.Helper()
		bd, err := store.LedgerBreakdown(ctx, from, to, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if bd[accounting.ClassTotal].Total != 0 || bd[accounting.ClassProxyRaw].Total != 0 {
			t.Fatalf("%s LedgerBreakdown leaked traffic: %+v", name, bd)
		}
		devs, err := store.ListDeviceLedgersInRange(ctx, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if len(devs) != 1 || devs[0].Total != 0 {
			t.Fatalf("%s ListDeviceLedgersInRange leaked traffic: %+v", name, devs)
		}
		obs, err := store.ListOutboundLedgersInRange(ctx, from, to, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(obs) != 0 {
			t.Fatalf("%s ListOutboundLedgersInRange leaked traffic: %+v", name, obs)
		}
		series, err := store.TrafficSeries(ctx, TrafficQuery{From: from, To: to, Group: "node", Class: accounting.ClassTotal})
		if err != nil {
			t.Fatal(err)
		}
		if series.Totals["node-a"] != 0 {
			t.Fatalf("%s TrafficSeries(node) leaked traffic: %+v", name, series.Totals)
		}
		dimSeries, err := store.TrafficSeries(ctx, TrafficQuery{From: from, To: to, Group: "process"})
		if err != nil {
			t.Fatal(err)
		}
		if dimSeries.Totals["curl"] != 0 {
			t.Fatalf("%s TrafficSeries(process) leaked traffic: %+v", name, dimSeries.Totals)
		}
		dims, err := store.ListDimensionStats(ctx, DimensionQuery{Type: "process", From: from, To: to})
		if err != nil {
			t.Fatal(err)
		}
		if dims.Total != 0 || len(dims.Items) != 0 {
			t.Fatalf("%s ListDimensionStats leaked traffic: %+v", name, dims)
		}
	}

	// (a) today & tz=Asia/Shanghai at 2026-08-18T18:00:00Z -> [16:00:00Z, 18:00:00Z]
	assertEmptyWindow(
		"today+Asia/Shanghai",
		time.Date(2026, 8, 18, 16, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC),
	)

	// (b) 24h after 25h+ idle when fromDay had traffic 28h ago -> [2026-08-18T14:00Z, 2026-08-19T14:00Z]
	now24h := trafficAt.Add(28 * time.Hour)
	assertEmptyWindow("24h-idle-28h", now24h.Add(-24*time.Hour), now24h)
}

func TestStraddling7DayWindowCombinesPurgedDailyAndSamples(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	if err := store.CreateNode(ctx, "node-a", "home", "cred-a", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO devices (id, site_id, created_at) VALUES ('dev-a', 'home', ?)`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	day15 := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -15).Add(10 * time.Hour)
	day13 := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -13).Add(14 * time.Hour)

	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-a", "boot-a", 1, day15,
		[]accounting.Increment{
			{Class: accounting.ClassTotal, Rx: 5000, Tx: 1000},
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 3000, Tx: 500},
			{DeviceID: "dev-a", Class: accounting.ClassTotal, Rx: 5000, Tx: 1000},
		},
		[]DimensionIncrement{{Type: "process", Key: "curl", Rx: 2000, Tx: 400}},
	); err != nil {
		t.Fatal(err)
	}

	// Purge day-15 from *_samples so it resides solely in *_daily.
	if err := store.MaintainLedgers(ctx, now); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-a", "boot-a", 2, day13,
		[]accounting.Increment{
			{Class: accounting.ClassTotal, Rx: 1000, Tx: 200},
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 700, Tx: 100},
			{DeviceID: "dev-a", Class: accounting.ClassTotal, Rx: 1000, Tx: 200},
		},
		[]DimensionIncrement{{Type: "process", Key: "curl", Rx: 400, Tx: 50}},
	); err != nil {
		t.Fatal(err)
	}

	// Custom <= 7d range straddling day-15 (daily-only) and day-13 (samples).
	from := day15.Add(-4 * time.Hour) // 06:00Z on day-15
	to := day13.Add(4 * time.Hour)    // 18:00Z on day-13

	bd, err := store.LedgerBreakdown(ctx, from, to, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if bd[accounting.ClassTotal].Rx != 6000 || bd[accounting.ClassTotal].Tx != 1200 || bd[accounting.ClassTotal].Total != 7200 {
		t.Fatalf("straddling LedgerBreakdown = %+v, want rx=6000 tx=1200 total=7200", bd[accounting.ClassTotal])
	}

	devs, err := store.ListDeviceLedgersInRange(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Rx != 6000 || devs[0].Tx != 1200 || devs[0].Total != 7200 {
		t.Fatalf("straddling ListDeviceLedgersInRange = %+v", devs)
	}

	obs, err := store.ListOutboundLedgersInRange(ctx, from, to, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].RawRx != 3700 || obs[0].RawTx != 600 || obs[0].Raw != 4300 {
		t.Fatalf("straddling ListOutboundLedgersInRange = %+v", obs)
	}

	dims, err := store.ListDimensionStats(ctx, DimensionQuery{Type: "process", From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if dims.TotalRx != 2400 || dims.TotalTx != 450 || dims.Total != 2850 {
		t.Fatalf("straddling ListDimensionStats = %+v", dims)
	}

	series, err := store.TrafficSeries(ctx, TrafficQuery{From: from, To: to, Group: "node", Class: accounting.ClassTotal})
	if err != nil {
		t.Fatal(err)
	}
	if series.Totals["node-a"] != 7200 {
		t.Fatalf("straddling TrafficSeries totals = %+v, want 7200", series.Totals)
	}
	var pointSum uint64
	for _, pt := range series.Points {
		pointSum += pt.Values["node-a"]
	}
	if pointSum != series.Totals["node-a"] {
		t.Fatalf("straddling TrafficSeries sum(points)=%d != totals=%d", pointSum, series.Totals["node-a"])
	}
}

func TestTrafficSeriesAlignedUpperBoundaryIncludesFinalBucket(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// 1. Exact 30m bucket boundary: to = 2026-08-19T14:30:00Z
	at30m := time.Date(2026, 8, 19, 14, 30, 0, 0, time.UTC)
	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-a", "boot-a", 1, at30m,
		[]accounting.Increment{{Class: accounting.ClassTotal, Rx: 220, Tx: 110}},
		[]DimensionIncrement{{Type: "process", Key: "curl", Rx: 150, Tx: 50}},
	); err != nil {
		t.Fatal(err)
	}

	s30m, err := store.TrafficSeries(ctx, TrafficQuery{
		From:          at30m.Add(-2 * time.Hour),
		To:            at30m,
		BucketSeconds: 1800,
		Group:         "node",
		Class:         accounting.ClassTotal,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sum30m, rx30m, tx30m uint64
	for _, pt := range s30m.Points {
		sum30m += pt.Values["node-a"]
		rx30m += pt.RxValues["node-a"]
		tx30m += pt.TxValues["node-a"]
	}
	if s30m.Totals["node-a"] != 330 || sum30m != 330 || rx30m != 220 || tx30m != 110 {
		t.Fatalf("30m aligned boundary mismatch: totals=%+v sum=%d rx=%d tx=%d", s30m.Totals, sum30m, rx30m, tx30m)
	}

	// 2. Exact 00:00:00Z daily boundary in >7d query
	midnightEnd := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-b", "boot-b", 1, midnightEnd,
		[]accounting.Increment{{Class: accounting.ClassTotal, Rx: 900, Tx: 300}},
		nil,
	); err != nil {
		t.Fatal(err)
	}

	sDaily, err := store.TrafficSeries(ctx, TrafficQuery{
		From:  midnightEnd.Add(-10 * 24 * time.Hour),
		To:    midnightEnd,
		Group: "node",
		Class: accounting.ClassTotal,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sumDaily uint64
	for _, pt := range sDaily.Points {
		sumDaily += pt.Values["node-b"]
	}
	if sDaily.Totals["node-b"] != 1200 || sumDaily != 1200 {
		t.Fatalf("daily aligned boundary mismatch: totals=%+v sum=%d", sDaily.Totals, sumDaily)
	}
}

func TestBackdatedSampleAfterPurgePreservesPriorDailyTotals(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	day20 := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -20).Add(8 * time.Hour)
	day20Start := day20.Truncate(24 * time.Hour).Unix()

	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-a", "boot-a", 1, day20,
		[]accounting.Increment{{Class: accounting.ClassTotal, Rx: 1000, Tx: 500}},
		[]DimensionIncrement{{Type: "process", Key: "curl", Rx: 600, Tx: 400}},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MaintainLedgers(ctx, now); err != nil {
		t.Fatal(err)
	}

	// Late/backdated batch arrives for already-purged day20.
	if _, err := store.ApplyLedgerWithDimensionsOnce(
		ctx, "home", "node-a", "boot-a", 2, day20.Add(time.Hour),
		[]accounting.Increment{{Class: accounting.ClassTotal, Rx: 200, Tx: 100}},
		[]DimensionIncrement{{Type: "process", Key: "curl", Rx: 50, Tx: 25}},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MaintainLedgers(ctx, now); err != nil {
		t.Fatal(err)
	}

	var lRx, lTx int64
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT rx, tx FROM ledger_daily WHERE day = ? AND node_id = 'node-a' AND device_id = '' AND class = ? AND outbound = ''`,
		day20Start, accounting.ClassTotal,
	).Scan(&lRx, &lTx); err != nil {
		t.Fatal(err)
	}
	if lRx != 1200 || lTx != 600 {
		t.Fatalf("ledger_daily after backdated MaintainLedgers = rx=%d tx=%d, want 1200/600", lRx, lTx)
	}

	var dRx, dTx int64
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT rx, tx FROM dimension_daily WHERE day = ? AND node_id = 'node-a' AND dim_type = 'process' AND dim_key = 'curl'`,
		day20Start,
	).Scan(&dRx, &dTx); err != nil {
		t.Fatal(err)
	}
	if dRx != 650 || dTx != 425 {
		t.Fatalf("dimension_daily after backdated MaintainLedgers = rx=%d tx=%d, want 650/425", dRx, dTx)
	}
}
