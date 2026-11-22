package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/alert"
	"github.com/misakayyds/lantally/internal/identity"
	"github.com/misakayyds/lantally/internal/protocol"
)

func TestOpenMigratesV12ToV13WithBackupAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "lantally.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if db != nil {
			db.SetMaxIdleConns(0)
			_ = db.Close()
		}
	}()
	// Apply 0001..0012 (skipping 0002/0007 which are conditional on pre-credential/pre-outbound tables) and set user_version = 12.
	for _, name := range []string{
		"migrations/0001_init.sql",
		"migrations/0003_devices.sql",
		"migrations/0004_admin.sql",
		"migrations/0005_ledgers.sql",
		"migrations/0006_ledger_samples.sql",
		"migrations/0008_ledger_daily.sql",
		"migrations/0009_multipliers_devices.sql",
		"migrations/0010_alerts_billing.sql",
		"migrations/0011_claim_codes.sql",
		"migrations/0012_dimensions_rules.sql",
	} {
		raw, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO billing_checkpoints (id, reset_day, provider_bytes, updated_at) VALUES (1, 12, 5000, '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO multiplier_rules (name, match_type, pattern, factor, priority, enabled, updated_at) VALUES ('hk', 'keyword', 'hk', 2.0, 100, 1, '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 12`); err != nil {
		t.Fatal(err)
	}
	db.SetMaxIdleConns(0)
	_ = db.Close()
	db = nil

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open v12->v13: %v", err)
	}
	defer store.Close()

	// Verify .bak-<unix> backup was created.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var hasBak bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "lantally.db.bak-") {
			hasBak = true
		}
	}
	if !hasBak {
		t.Fatalf("expected .bak-<unix> pre-migration backup in %s", dir)
	}

	ver, err := sqliteUserVersion(store.db)
	if err != nil || ver != schemaVersion {
		t.Fatalf("user_version = %d, err=%v; want %d", ver, err, schemaVersion)
	}
	if ok, err := tableExists(store.db, "watch_items"); err != nil || !ok {
		t.Fatalf("watch_items missing: ok=%v err=%v", ok, err)
	}
	if ok, err := tableExists(store.db, "provider_subscriptions"); err != nil || !ok {
		t.Fatalf("provider_subscriptions missing: ok=%v err=%v", ok, err)
	}
	// Legacy data preserved.
	if rd, err := store.BillingResetDay(context.Background()); err != nil || rd != 12 {
		t.Fatalf("BillingResetDay = %d, err=%v; want 12", rd, err)
	}
	rules, err := store.ListMultiplierRules(context.Background())
	if err != nil || len(rules) != 1 || rules[0].Name != "hk" {
		t.Fatalf("multiplier rules lost: %+v err=%v", rules, err)
	}
	_ = store.Close()

	// Re-open must be idempotent without creating another backup.
	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-open v13: %v", err)
	}
	_ = store2.Close()
}

func TestWatchlistEvaluationOrthogonalAndPurgedAllTime(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.CreateNode(ctx, "node-a", "site-1", "cred-a", []byte("h1")); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateNode(ctx, "node-b", "site-1", "cred-b", []byte("h2")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMultiplierRule(ctx, accounting.MultiplierRule{
		Name: "HK2x", MatchType: accounting.MatchExact, Pattern: "hk-01", Factor: 2.0, Priority: 100, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	oldTS := now.Add(-20 * 24 * time.Hour)
	recentTS := now.Add(-1 * time.Hour)

	// Old batch (>14d): will be purged from *_samples after MaintainLedgers, leaving *_daily.
	if _, err := store.ApplyLedgerWithDimensionsOnce(ctx, "site-1", "node-a", "boot-1", 1, oldTS,
		[]accounting.Increment{
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 1000, Tx: 200},
		},
		[]DimensionIncrement{
			{Type: "group", Key: "AI", Rx: 1000, Tx: 200},
			{Type: "process", Key: "Cursor", Rx: 1000, Tx: 200},
			{Type: "process", Key: "Cursor-Helper", Rx: 100, Tx: 50},
		},
	); err != nil {
		t.Fatal(err)
	}
	// Recent batch: stays in *_samples AND *_daily (must not double-count!).
	if _, err := store.ApplyLedgerWithDimensionsOnce(ctx, "site-1", "node-a", "boot-1", 2, recentTS,
		[]accounting.Increment{
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 500, Tx: 100},
		},
		[]DimensionIncrement{
			{Type: "group", Key: "AI", Rx: 500, Tx: 100},
			{Type: "process", Key: "Cursor", Rx: 500, Tx: 100},
		},
	); err != nil {
		t.Fatal(err)
	}
	// Traffic on node-b that must be excluded by node-a scope filter.
	if _, err := store.ApplyLedgerWithDimensionsOnce(ctx, "site-1", "node-b", "boot-1", 1, recentTS,
		[]accounting.Increment{
			{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 9000, Tx: 9000},
		},
		[]DimensionIncrement{
			{Type: "group", Key: "AI", Rx: 9000, Tx: 9000},
			{Type: "process", Key: "Cursor", Rx: 9000, Tx: 9000},
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := store.MaintainLedgers(ctx, now); err != nil {
		t.Fatal(err)
	}

	// 1. Orthogonal group + process + node scope across all-time (none) including purged >14d daily history:
	//    In old bucket: group="AI" (1000/200), process="Cursor" (1000/200) -> min = 1000/200.
	//    In recent bucket: group="AI" (500/100), process="Cursor" (500/100) -> min = 500/100.
	//    Total = 1500/300 (neither double-counted across dims, nor missing purged old daily, nor double-counting recent daily+sample).
	item1, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "AI Cursor NodeA",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimNode:    {Type: accounting.MatchExact, Values: []string{"node-a"}},
			accounting.WatchDimGroup:   {Type: accounting.MatchExact, Values: []string{"AI"}},
			accounting.WatchDimProcess: {Type: accounting.MatchExact, Values: []string{"Cursor"}},
		},
		ResetType: accounting.ResetNone,
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	st1, err := store.EvaluateWatchItem(ctx, item1, time.Time{}, time.Time{}, 0, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if st1.Rx != 1500 || st1.Tx != 300 || st1.Total != 1800 || !st1.OverlapPossible {
		t.Fatalf("item1 eval = rx:%d tx:%d total:%d overlap:%v, want 1500/300/1800/true", st1.Rx, st1.Tx, st1.Total, st1.OverlapPossible)
	}
	// Verify status totals equal exact sum of trend series buckets.
	var sumRx, sumTx, sumAdj uint64
	for _, pt := range st1.Series {
		sumRx += pt.Rx
		sumTx += pt.Tx
		sumAdj += pt.Adjusted
	}
	if sumRx != st1.Rx || sumTx != st1.Tx || sumAdj != st1.Adjusted {
		t.Fatalf("series sum (%d/%d/%d) != status (%d/%d/%d)", sumRx, sumTx, sumAdj, st1.Rx, st1.Tx, st1.Adjusted)
	}

	// 2. Intra-dimension OR with overlapping matchers: keyword "Cursor" + exact "Cursor" must count Cursor once.
	item2, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "Cursor Any",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimNode:    {Type: accounting.MatchExact, Values: []string{"node-a"}},
			accounting.WatchDimProcess: {Type: accounting.MatchKeyword, Values: []string{"cursor", "cur"}},
		},
		ResetType: accounting.ResetNone,
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	st2, err := store.EvaluateWatchItem(ctx, item2, time.Time{}, time.Time{}, 0, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	// Cursor (1500/300) + Cursor-Helper (100/50) = 1600/350, counted once each!
	if st2.Rx != 1600 || st2.Tx != 350 {
		t.Fatalf("intra-dim OR eval = %d/%d, want 1600/350", st2.Rx, st2.Tx)
	}

	// 3. Outbound multiplier inheritance vs override_factor.
	item3, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "Outbound Inherit",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimNode:     {Type: accounting.MatchExact, Values: []string{"node-a"}},
			accounting.WatchDimOutbound: {Type: accounting.MatchExact, Values: []string{"hk-01"}},
		},
		ResetType: accounting.ResetNone,
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	st3, err := store.EvaluateWatchItem(ctx, item3, time.Time{}, time.Time{}, 0, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if st3.Total != 1800 || st3.Adjusted != 3600 {
		t.Fatalf("outbound inherit = total:%d adj:%d, want 1800/3600", st3.Total, st3.Adjusted)
	}

	ov := 0.5
	item4, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "Outbound Override",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimNode:     {Type: accounting.MatchExact, Values: []string{"node-a"}},
			accounting.WatchDimOutbound: {Type: accounting.MatchExact, Values: []string{"hk-01"}},
		},
		OverrideFactor: &ov,
		ResetType:      accounting.ResetNone,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	st4, err := store.EvaluateWatchItem(ctx, item4, time.Time{}, time.Time{}, 0, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if st4.Total != 1800 || st4.Adjusted != 900 {
		t.Fatalf("outbound override = total:%d adj:%d, want 1800/900", st4.Total, st4.Adjusted)
	}

	// 4. Revoked node historical traffic remains accessible.
	if err := store.RevokeNode(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}
	st1After, err := store.EvaluateWatchItem(ctx, item1, time.Time{}, time.Time{}, 0, time.UTC, now)
	if err != nil || st1After.Total != 1800 {
		t.Fatalf("revoked node watch total = %d, err=%v; want 1800", st1After.Total, err)
	}
}

func TestWatchlistDevicePlusOutboundAndNodeDomainPrefs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.CreateNode(ctx, "node-1", "site-1", "cred-1", []byte("h1")); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateNode(ctx, "node-2", "site-1", "cred-2", []byte("h2")); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	devA, _, err := store.ResolveIdentityAtomic("site-1", "node-1", []identity.Evidence{{Rank: identity.RankStableMAC, Value: "02:00:00:00:00:01"}}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	devB, _, err := store.ResolveIdentityAtomic("site-1", "node-1", []identity.Evidence{{Rank: identity.RankStableMAC, Value: "02:00:00:00:00:02"}}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RenameDevice(ctx, devA, "Workstation"); err != nil {
		t.Fatal(err)
	}
	if err := store.MergeIdentities(devA, devB, int(identity.RankPinned), "manual", now); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyLedgerOnce(ctx, "site-1", "node-1", "boot-1", 1, now, []accounting.Increment{
		{DeviceID: devA, Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 400, Tx: 100},
		{DeviceID: devB, Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 200, Tx: 50},
		{DeviceID: devA, Class: accounting.ClassProxyRaw, Outbound: "us-01", Rx: 999, Tx: 999},
	}); err != nil {
		t.Fatal(err)
	}

	// Exact AND between device ("Workstation" -> devA + merged devB) and outbound ("hk-01").
	devWatch, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "Workstation HK",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDevice:   {Type: accounting.MatchExact, Values: []string{"Workstation"}},
			accounting.WatchDimOutbound: {Type: accounting.MatchExact, Values: []string{"hk-01"}},
		},
		ResetType: accounting.ResetNone,
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.EvaluateWatchItem(ctx, devWatch, time.Time{}, time.Time{}, 0, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Rx != 600 || st.Tx != 150 {
		t.Fatalf("device+outbound AND = %d/%d, want 600/150", st.Rx, st.Tx)
	}

	// NodeDomainPreferences: empty when no domain watch items exist.
	col1, wd1, err := store.NodeDomainPreferences(ctx, "node-1")
	if err != nil || col1 || wd1 != nil {
		t.Fatalf("initial NodeDomainPreferences = (%v, %v, %v), want (false, nil, nil)", col1, wd1, err)
	}

	// Create domain watch scoped to node-1 + global keyword/regex domain watch.
	if _, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "ScopedExact",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimNode:   {Type: accounting.MatchExact, Values: []string{"node-1"}},
			accounting.WatchDimDomain: {Type: accounting.MatchExact, Values: []string{"API.Watched.Test."}},
		},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "GlobalKWRe",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchKeyword, Values: []string{"OpenAI"}},
		},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "GlobalRe",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchRegex, Values: []string{`^chat\.`}},
		},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	col1, wd1, err = store.NodeDomainPreferences(ctx, "node-1")
	if err != nil || col1 {
		t.Fatalf("node-1 prefs = collect:%v err:%v", col1, err)
	}
	want1 := []string{"api.watched.test", "kw:openai", `re:^chat\.`}
	if strings.Join(wd1, ",") != strings.Join(want1, ",") {
		t.Fatalf("node-1 watch_domains = %v, want %v", wd1, want1)
	}

	// node-2 must NOT receive node-1's scoped exact domain.
	col2, wd2, err := store.NodeDomainPreferences(ctx, "node-2")
	if err != nil || col2 {
		t.Fatalf("node-2 prefs = collect:%v err:%v", col2, err)
	}
	want2 := []string{"kw:openai", `re:^chat\.`}
	if strings.Join(wd2, ",") != strings.Join(want2, ",") {
		t.Fatalf("node-2 watch_domains = %v, want %v", wd2, want2)
	}
}

func TestProvidersSyncSharedDedupAndAlertsLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	if err := store.CreateNode(ctx, "n1", "site-1", "c1", []byte("h1")); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateNode(ctx, "n2", "site-1", "c2", []byte("h2")); err != nil {
		t.Fatal(err)
	}

	// Multi-node shared Mihomo provider + Compatible + ZeroTotal HTTP.
	if err := store.UpsertMihomoProviders(ctx, "n1", []protocol.ProviderSnapshot{
		{Name: "default", VehicleType: "Compatible"},
		{Name: "EmptyNonHTTP", VehicleType: "File"},
		{Name: "SharedSub", VehicleType: "HTTP", Upload: 200, Download: 800, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01", "DIRECT"}},
		{Name: "ZeroSub", VehicleType: "HTTP", Upload: 0, Download: 0, Total: 0, Expire: 0, Proxies: []string{"us-01"}},
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertMihomoProviders(ctx, "n2", []protocol.ProviderSnapshot{
		{Name: "SharedSub", VehicleType: "HTTP", Upload: 200, Download: 800, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01", "jp-01"}},
	}, now); err != nil {
		t.Fatal(err)
	}

	subs, err := store.ListProviderSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 {
		t.Fatalf("got %d subs, want 2 (Compatible & EmptyNonHTTP excluded, SharedSub merged): %+v", len(subs), subs)
	}
	if subs[0].Name != "SharedSub" || len(subs[0].LinkedNodes) != 2 || len(subs[0].Proxies) != 2 {
		t.Fatalf("merged SharedSub unexpected: %+v", subs[0])
	}

	// Preserve user customizations across subsequent Mihomo sync.
	rd := 10
	pat := `^hk-`
	en := true
	if _, err := store.UpdateProviderSubscription(ctx, subs[0].ID, ProviderInput{
		ResetDay:        &rd,
		OutboundPattern: &pat,
		Enabled:         &en,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertMihomoProviders(ctx, "n1", []protocol.ProviderSnapshot{
		{Name: "SharedSub", VehicleType: "HTTP", Upload: 300, Download: 700, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01"}},
	}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	afterSync, err := store.GetProviderSubscription(ctx, subs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSync.ResetDay != 10 || afterSync.OutboundPattern != `^hk-` {
		t.Fatalf("user-edited fields overwritten on sync: %+v", afterSync)
	}

	// Add second provider on n1 sharing same upstream subscription (total=10000, expire=1893456000).
	if err := store.UpsertMihomoProviders(ctx, "n1", []protocol.ProviderSnapshot{
		{Name: "SharedSub-Alt", VehicleType: "HTTP", Upload: 300, Download: 700, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01"}},
	}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Ingest traffic on hk-01: 1100 bytes (ratio = 0.100 vs 1000 -> no drift alert yet).
	if _, err := store.ApplyLedgerOnce(ctx, "n1-site", "n1", "boot-1", 1, now, []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 1100, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	statuses, summary, err := store.ListProviderStatuses(ctx, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 3 {
		t.Fatalf("got %d statuses, want 3", len(statuses))
	}
	// SharedSub and SharedSub-Alt share upstream total=10000 -> summary TotalBytes must be 10000, not 20000.
	if summary.TotalBytes != 10000 || summary.UsedBytes != 1000 || summary.LocalAdjusted != 1100 {
		t.Fatalf("summary dedup mismatch: %+v", summary)
	}
	for _, st := range statuses {
		if math.IsNaN(st.DriftRatio) || math.IsInf(st.DriftRatio, 0) {
			t.Fatalf("unsafe drift ratio: %+v", st)
		}
		if st.DriftAlert {
			t.Fatalf("0.10 exact boundary must not alert: %+v", st)
		}
	}

	// Bump local traffic by 1 byte -> 1101 (ratio 0.101 > 0.10 -> fires drift alert).
	if _, err := store.ApplyLedgerOnce(ctx, "n1-site", "n1", "boot-1", 2, now, []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 1, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	alerts, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var driftCount int
	for _, a := range alerts {
		if a.Kind == string(alert.KindDrift) {
			driftCount++
		}
	}
	if driftCount == 0 {
		t.Fatalf("expected provider drift alert at ratio 0.101, got %+v", alerts)
	}

	// Sync provider usage up to 1101 -> drift alert auto-resolves.
	if err := store.UpsertMihomoProviders(ctx, "n1", []protocol.ProviderSnapshot{
		{Name: "SharedSub", VehicleType: "HTTP", Upload: 300, Download: 801, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01"}},
		{Name: "SharedSub-Alt", VehicleType: "HTTP", Upload: 300, Download: 801, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01"}},
	}, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	alertsAfter, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range alertsAfter {
		if a.Kind == string(alert.KindDrift) {
			t.Fatalf("expected drift alert to auto-resolve, still open: %+v", a)
		}
	}

	// Watchlist quota alert: create watch item with 1000B quota (using 1MB=1048576? create with 1MB and ingest 2MB), ack, resolve.
	w, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "HKQuota",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimOutbound: {Type: accounting.MatchExact, Values: []string{"hk-01"}},
		},
		QuotaValue: 1,
		QuotaUnit:  "MB",
		ResetType:  accounting.ResetMonthly,
		ResetDay:   1,
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "n1-site", "n1", "boot-1", 3, now, []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 2 << 20, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	qAlerts, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var qID int64
	for _, a := range qAlerts {
		if a.Kind == string(alert.KindQuota) {
			qID = a.ID
		}
	}
	if qID == 0 {
		t.Fatalf("expected quota alert, got %+v", qAlerts)
	}
	if err := store.AckAlert(ctx, qID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	// Acked alert stays suppressed.
	qAlerts2, _ := store.ListOpenAlerts(ctx)
	for _, a := range qAlerts2 {
		if a.Kind == string(alert.KindQuota) {
			t.Fatalf("acked quota alert reappeared: %+v", a)
		}
	}
	// Increasing quota above usage resolves the underlying alert row.
	w.QuotaValue = 10
	if _, err := store.UpdateWatchItem(ctx, w.ID, w); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	var unresolvedQuota int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE kind = 'quota' AND resolved_at IS NULL`).Scan(&unresolvedQuota); err != nil {
		t.Fatal(err)
	}
	if unresolvedQuota != 0 {
		t.Fatalf("expected resolved quota alert after quota increase, got %d unresolved", unresolvedQuota)
	}

	// Disabled provider is excluded from summary and drift alerts.
	dis := false
	if _, err := store.UpdateProviderSubscription(ctx, subs[0].ID, ProviderInput{Enabled: &dis}); err != nil {
		t.Fatal(err)
	}
	_, sumDisabled, err := store.ListProviderStatuses(ctx, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if sumDisabled.ActiveCount != 2 {
		t.Fatalf("active_count = %d, want 2", sumDisabled.ActiveCount)
	}
	if err := store.DeleteWatchItem(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWatchItem(ctx, w.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("delete missing watch err = %v, want sql.ErrNoRows", err)
	}
}

func TestWatchlistSymmetricOutboundMultiplierAndManualProviderDedupAndMaxInt64(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.CreateNode(ctx, "n1", "site-1", "c1", []byte("h1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMultiplierRule(ctx, accounting.MultiplierRule{
		Name: "HK2x", MatchType: accounting.MatchExact, Pattern: "HK-1", Factor: 2.0, Priority: 100, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMultiplierRule(ctx, accounting.MultiplierRule{
		Name: "US0.5x", MatchType: accounting.MatchExact, Pattern: "US-1", Factor: 0.5, Priority: 100, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if _, err := store.ApplyLedgerWithDimensionsOnce(ctx, "site-1", "n1", "boot-1", 1, now.Add(-1*time.Minute),
		[]accounting.Increment{
			{Class: accounting.ClassProxyRaw, Outbound: "HK-1", Rx: 1000, Tx: 200},
			{Class: accounting.ClassProxyRaw, Outbound: "US-1", Rx: 800, Tx: 200},
		},
		[]DimensionIncrement{
			{Type: "process", Key: "Cursor", Rx: 600, Tx: 100},
		},
	); err != nil {
		t.Fatal(err)
	}

	// outbound: ["HK-1"] (2.0x) + process: ["Cursor"] (600/100) with override=nil -> 2.0x adjusted (1200/200 = 1400).
	wHK, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "HK Cursor",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimNode:     {Type: accounting.MatchExact, Values: []string{"n1"}},
			accounting.WatchDimOutbound: {Type: accounting.MatchExact, Values: []string{"HK-1"}},
			accounting.WatchDimProcess:  {Type: accounting.MatchExact, Values: []string{"Cursor"}},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stHK, err := store.EvaluateWatchItem(ctx, wHK, time.Time{}, time.Time{}, 0, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if stHK.Rx != 600 || stHK.Tx != 100 || stHK.Total != 700 || stHK.Adjusted != 1400 {
		t.Fatalf("HK-1 + Cursor = rx:%d tx:%d total:%d adj:%d, want 600/100/700/1400", stHK.Rx, stHK.Tx, stHK.Total, stHK.Adjusted)
	}

	// Symmetric <1.0x: outbound: ["US-1"] (0.5x) + process: ["Cursor"] -> 0.5x adjusted (300/50 = 350).
	wUS, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "US Cursor",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimNode:     {Type: accounting.MatchExact, Values: []string{"n1"}},
			accounting.WatchDimOutbound: {Type: accounting.MatchExact, Values: []string{"US-1"}},
			accounting.WatchDimProcess:  {Type: accounting.MatchExact, Values: []string{"Cursor"}},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stUS, err := store.EvaluateWatchItem(ctx, wUS, time.Time{}, time.Time{}, 0, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if stUS.Rx != 600 || stUS.Tx != 100 || stUS.Total != 700 || stUS.Adjusted != 350 {
		t.Fatalf("US-1 + Cursor = rx:%d tx:%d total:%d adj:%d, want 600/100/700/350", stUS.Rx, stUS.Tx, stUS.Total, stUS.Adjusted)
	}

	// Two manual subscriptions with equal total_bytes, expire_at=0, and 0 usage must NOT be collapsed.
	tv := 100.0
	if _, err := store.CreateProviderSubscription(ctx, ProviderInput{
		Name: "ManualA", Unit: "GB", TotalValue: &tv,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProviderSubscription(ctx, ProviderInput{
		Name: "ManualB", Unit: "GB", TotalValue: &tv,
	}); err != nil {
		t.Fatal(err)
	}
	_, summary, err := store.ListProviderStatuses(ctx, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	wantTotal := uint64(200) << 30
	if summary.TotalBytes != wantTotal {
		t.Fatalf("manual zero-usage equal-quota summary.TotalBytes = %d, want %d", summary.TotalBytes, wantTotal)
	}

	// Two manual subscriptions with equal total_bytes and matching non-zero expire_at/used must NOT be collapsed.
	tv50 := 50.0
	used10 := 10.0
	exp := int64(1893456000)
	if _, err := store.CreateProviderSubscription(ctx, ProviderInput{
		Name: "ManualExpA", Unit: "GB", TotalValue: &tv50, UsedValue: &used10, ExpireAt: &exp,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProviderSubscription(ctx, ProviderInput{
		Name: "ManualExpB", Unit: "GB", TotalValue: &tv50, UsedValue: &used10, ExpireAt: &exp,
	}); err != nil {
		t.Fatal(err)
	}
	_, summaryExp, err := store.ListProviderStatuses(ctx, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	wantTotalWithExp := uint64(300) << 30
	wantUsedWithExp := uint64(20) << 30
	if summaryExp.TotalBytes != wantTotalWithExp || summaryExp.UsedBytes != wantUsedWithExp {
		t.Fatalf("manual equal-expire summary = (total=%d, used=%d), want (%d, %d)", summaryExp.TotalBytes, summaryExp.UsedBytes, wantTotalWithExp, wantUsedWithExp)
	}

	// > math.MaxInt64 byte inputs must be rejected with ErrInvalidInput (never wrapped to negative/0).
	overMax := uint64(math.MaxInt64) + 1
	if _, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name:       "WatchOverflow",
		Matchers:   accounting.WatchMatcherMap{accounting.WatchDimProcess: {Type: accounting.MatchExact, Values: []string{"Cursor"}}},
		QuotaBytes: overMax,
		QuotaUnit:  "GB",
		Enabled:    true,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateWatchItem >MaxInt64 err = %v, want ErrInvalidInput", err)
	}
	wHK.QuotaBytes = overMax
	if _, err := store.UpdateWatchItem(ctx, wHK.ID, wHK); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateWatchItem >MaxInt64 err = %v, want ErrInvalidInput", err)
	}

	hugeTB := 9000000.0
	for name, inp := range map[string]ProviderInput{
		"total_value":    {Name: "P1", Unit: "TB", TotalValue: &hugeTB},
		"total_bytes":    {Name: "P2", Unit: "GB", TotalBytes: &overMax},
		"upload_bytes":   {Name: "P3", Unit: "GB", UploadBytes: &overMax},
		"download_bytes": {Name: "P4", Unit: "GB", DownloadBytes: &overMax},
		"used_bytes":     {Name: "P5", Unit: "GB", UsedBytes: &overMax},
	} {
		if _, err := store.CreateProviderSubscription(ctx, inp); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("CreateProviderSubscription(%s) >MaxInt64 err = %v, want ErrInvalidInput", name, err)
		}
	}
	validSub, err := store.CreateProviderSubscription(ctx, ProviderInput{Name: "ValidManual", Unit: "GB", TotalValue: &tv})
	if err != nil {
		t.Fatal(err)
	}
	for name, inp := range map[string]ProviderInput{
		"total_value":    {Unit: "TB", TotalValue: &hugeTB},
		"total_bytes":    {TotalBytes: &overMax},
		"upload_bytes":   {UploadBytes: &overMax},
		"download_bytes": {DownloadBytes: &overMax},
		"used_bytes":     {UsedBytes: &overMax},
	} {
		if _, err := store.UpdateProviderSubscription(ctx, validSub.ID, inp); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("UpdateProviderSubscription(%s) >MaxInt64 err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestSharedProviderDriftEvaluationAndResolution(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	if err := store.CreateNode(ctx, "n1", "site-1", "c1", []byte("h1")); err != nil {
		t.Fatal(err)
	}

	// (a) Split shared Mihomo providers (both te: and tud:): hk-01=500, jp-01=500 vs shared Used=1000 -> 0% drift, 0 alerts.
	if err := store.UpsertMihomoProviders(ctx, "n1", []protocol.ProviderSnapshot{
		{Name: "TE-A", VehicleType: "HTTP", Upload: 200, Download: 800, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01"}},
		{Name: "TE-B", VehicleType: "HTTP", Upload: 200, Download: 800, Total: 10000, Expire: 1893456000, Proxies: []string{"jp-01"}},
		{Name: "TUD-A", VehicleType: "HTTP", Upload: 300, Download: 700, Total: 20000, Expire: 0, Proxies: []string{"us-01"}},
		{Name: "TUD-B", VehicleType: "HTTP", Upload: 300, Download: 700, Total: 20000, Expire: 0, Proxies: []string{"sg-01"}},
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "site-1", "n1", "boot-1", 1, now, []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 500, Tx: 0},
		{Class: accounting.ClassProxyRaw, Outbound: "jp-01", Rx: 500, Tx: 0},
		{Class: accounting.ClassProxyRaw, Outbound: "us-01", Rx: 500, Tx: 0},
		{Class: accounting.ClassProxyRaw, Outbound: "sg-01", Rx: 500, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	openAlerts, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range openAlerts {
		if a.Kind == string(alert.KindDrift) {
			t.Fatalf("expected 0 drift alerts when split shared providers sum to 0%% drift, got: %+v", openAlerts)
		}
	}

	// (b) Bump TE group traffic to hk-01=950, jp-01=950 (combined 1900 vs Used=1000 -> +90% drift) -> 1 open drift alert.
	// Also seed a stale individual drift|provider|<id>|<from> alert to verify superseded cleanup.
	from, _ := BillingPeriodInLoc(now, 1, time.UTC)
	subs, err := store.ListProviderSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RaiseAlert(ctx, alert.Record{
		Kind:        alert.KindDrift,
		Message:     "stale per-provider alert",
		ObservedAt:  now,
		Fingerprint: "drift|provider|" + itoa64(subs[0].ID) + "|" + itoa64(from.Unix()),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyLedgerOnce(ctx, "site-1", "n1", "boot-1", 2, now, []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 450, Tx: 0},
		{Class: accounting.ClassProxyRaw, Outbound: "jp-01", Rx: 450, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	openAlerts, err = store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var driftAlerts []Alert
	for _, a := range openAlerts {
		if a.Kind == string(alert.KindDrift) {
			driftAlerts = append(driftAlerts, a)
		}
	}
	if len(driftAlerts) != 1 {
		t.Fatalf("expected exactly 1 shared-group drift alert at +90%% drift, got %d: %+v", len(driftAlerts), driftAlerts)
	}

	// Resolve via provider usage catching up to 1900.
	if err := store.UpsertMihomoProviders(ctx, "n1", []protocol.ProviderSnapshot{
		{Name: "TE-A", VehicleType: "HTTP", Upload: 400, Download: 1500, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01"}},
		{Name: "TE-B", VehicleType: "HTTP", Upload: 400, Download: 1500, Total: 10000, Expire: 1893456000, Proxies: []string{"jp-01"}},
	}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	openAlerts, err = store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range openAlerts {
		if a.Kind == string(alert.KindDrift) {
			t.Fatalf("expected shared drift alert to resolve when usage caught up, got: %+v", a)
		}
	}

	// Re-trigger TE group drift (+500 on hk-01 -> 2400 vs 1900), then resolve by disabling group providers.
	if _, err := store.ApplyLedgerOnce(ctx, "site-1", "n1", "boot-1", 3, now, []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "hk-01", Rx: 500, Tx: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	openAlerts, _ = store.ListOpenAlerts(ctx)
	var teIDs []int64
	for _, sItem := range subs {
		if sItem.Name == "TE-A" || sItem.Name == "TE-B" {
			teIDs = append(teIDs, sItem.ID)
		}
	}
	if len(openAlerts) != 1 || openAlerts[0].Kind != string(alert.KindDrift) {
		t.Fatalf("expected 1 drift alert after re-trigger, got: %+v", openAlerts)
	}

	dis := false
	for _, id := range teIDs {
		if _, err := store.UpdateProviderSubscription(ctx, id, ProviderInput{Enabled: &dis}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	openAlerts, _ = store.ListOpenAlerts(ctx)
	for _, a := range openAlerts {
		if a.Kind == string(alert.KindDrift) {
			t.Fatalf("expected drift alert to resolve when shared providers disabled, got: %+v", a)
		}
	}

	// Re-enable TE providers, verify drift alert returns, then delete both TE providers and verify alert resolves cleanly.
	en := true
	for _, id := range teIDs {
		if _, err := store.UpdateProviderSubscription(ctx, id, ProviderInput{Enabled: &en}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	openAlerts, _ = store.ListOpenAlerts(ctx)
	if len(openAlerts) != 1 || openAlerts[0].Kind != string(alert.KindDrift) {
		t.Fatalf("expected 1 drift alert after re-enabling TE providers, got: %+v", openAlerts)
	}
	for _, id := range teIDs {
		if err := store.DeleteProviderSubscription(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	openAlerts, _ = store.ListOpenAlerts(ctx)
	for _, a := range openAlerts {
		if a.Kind == string(alert.KindDrift) {
			t.Fatalf("expected drift alert to resolve immediately on deleting last group member, got: %+v", a)
		}
	}
	// Delete TUD providers too before testing manual providers.
	for _, sItem := range subs {
		if sItem.Name == "TUD-A" || sItem.Name == "TUD-B" {
			if err := store.DeleteProviderSubscription(ctx, sItem.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}

	// (c) Non-shared/manual providers continue evaluating individually even with identical Total/Used/Expire.
	exp := int64(1893456000)
	total100k := uint64(100000)
	used1000 := uint64(1000)
	manA, err := store.CreateProviderSubscription(ctx, ProviderInput{
		Name: "ManualA", TotalBytes: &total100k, UsedBytes: &used1000, ExpireAt: &exp, Proxies: &[]string{"man-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	manB, err := store.CreateProviderSubscription(ctx, ProviderInput{
		Name: "ManualB", TotalBytes: &total100k, UsedBytes: &used1000, ExpireAt: &exp, Proxies: &[]string{"man-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "site-1", "n1", "boot-1", 4, now, []accounting.Increment{
		{Class: accounting.ClassProxyRaw, Outbound: "man-a", Rx: 1500, Tx: 0}, // +50% drift
		{Class: accounting.ClassProxyRaw, Outbound: "man-b", Rx: 1500, Tx: 0}, // +50% drift
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	openAlerts, _ = store.ListOpenAlerts(ctx)
	var manualDrifts int
	for _, a := range openAlerts {
		if a.Kind == string(alert.KindDrift) {
			manualDrifts++
		}
	}
	if manualDrifts != 2 {
		t.Fatalf("expected 2 individual drift alerts for ManualA and ManualB, got %d: %+v", manualDrifts, openAlerts)
	}

	// Bring ManualA within threshold -> only ManualB drift alert stays open.
	used1500 := uint64(1500)
	if _, err := store.UpdateProviderSubscription(ctx, manA.ID, ProviderInput{UsedBytes: &used1500}); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	openAlerts, _ = store.ListOpenAlerts(ctx)
	driftAlerts = nil
	for _, a := range openAlerts {
		if a.Kind == string(alert.KindDrift) {
			driftAlerts = append(driftAlerts, a)
		}
	}
	if len(driftAlerts) != 1 || !strings.Contains(driftAlerts[0].Message, manB.Name) {
		t.Fatalf("expected only ManualB drift alert remaining, got: %+v", driftAlerts)
	}

	// (d) Empty SharedGroup fallback safety in providerDriftFingerprint.
	emptyGrpFP := providerDriftFingerprint(providerGroupStatus{
		sharedGroup: "",
		cycleFrom:   from,
		members:     []ProviderStatus{{ProviderSubscription: ProviderSubscription{ID: 42, Name: "EmptyGroup"}}},
	})
	if emptyGrpFP != "drift|provider|42|"+itoa64(from.Unix()) {
		t.Fatalf("empty SharedGroup fallback fingerprint = %q", emptyGrpFP)
	}
}

func TestSetBillingRejectsMaxInt64Overflow(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	if err := store.SetBilling(ctx, Billing{ResetDay: 5, ProviderBytes: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBilling(ctx, Billing{ResetDay: 1, ProviderBytes: uint64(math.MaxInt64) + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SetBilling(>MaxInt64) err = %v, want ErrInvalidInput", err)
	}
	st, err := store.BillingStatus(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.ResetDay != 5 || st.ProviderBytes != 1000 {
		t.Fatalf("rejected SetBilling mutated stored billing: %+v", st)
	}
	if err := store.SetBilling(ctx, Billing{ResetDay: 5, ProviderBytes: math.MaxInt64}); err != nil {
		t.Fatalf("SetBilling(MaxInt64) err = %v", err)
	}
	stMax, err := store.BillingStatus(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if stMax.ProviderBytes != math.MaxInt64 {
		t.Fatalf("BillingStatus after MaxInt64 = %d, want %d", stMax.ProviderBytes, uint64(math.MaxInt64))
	}
}

func itoa64(n int64) string {
	return strings.TrimSpace(strconv.FormatInt(n, 10))
}
