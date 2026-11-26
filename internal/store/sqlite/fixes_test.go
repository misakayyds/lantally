package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/identity"
	"github.com/misakayyds/lantally/internal/protocol"
)

func TestOpenEnablesWALAndBusyTimeoutOnFileDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal-check.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var timeout int
	if err := store.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if timeout < 5000 {
		t.Fatalf("busy_timeout = %d, want >= 5000", timeout)
	}
}

func TestBatchCountIncludesDirectCollectorLedgerAppliedWithoutDoubleCountingAgents(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "batch-count.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// 1. Direct collector writes only ledger_applied (not ingest_batches).
	applied, err := store.ApplyLedgerOnce(ctx, "site-a", "dc-1", "boot-dc", 1, now, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 100, Tx: 50},
	})
	if err != nil || !applied {
		t.Fatalf("direct ApplyLedgerOnce applied=%v err=%v", applied, err)
	}
	if got, err := store.BatchCount(ctx); err != nil || got != 1 {
		t.Fatalf("BatchCount after direct batch = %d err=%v, want 1", got, err)
	}

	// 2. Agent writes both ingest_batches and ledger_applied with identical tuple -> counted once.
	createTestNode(t, store, "agent-1", "cred-ag", 'a')
	if _, err := store.InsertBatch(ctx, "agent-1", "boot-ag", 1, []byte("raw")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyLedgerOnce(ctx, "site-a", "agent-1", "boot-ag", 1, now, []accounting.Increment{
		{Class: accounting.ClassTotal, Rx: 200, Tx: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.BatchCount(ctx); err != nil || got != 2 {
		t.Fatalf("BatchCount with direct + agent = %d err=%v, want 2", got, err)
	}
}

func TestDeviceTrafficSeriesFallsBackToMihomoDirectAndProxyRawAndSortsDescending(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "device-series.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	resolver := identity.NewResolver(store, "dc-router")

	devSmall, _, err := resolver.Resolve("site-a", protocol.DeviceDelta{
		ObsIP: "192.0.2.10", Source: protocol.SourceMihomo, Outbound: "DIRECT",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	devBig, _, err := resolver.Resolve("site-a", protocol.DeviceDelta{
		ObsIP: "192.0.2.20", Source: protocol.SourceMihomo, Outbound: "HK-01",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	devNlbw, _, err := resolver.Resolve("site-a", protocol.DeviceDelta{
		ObsIP: "192.0.2.30", ObsMAC: "02:00:00:00:00:30", Source: protocol.SourceNlbwmon,
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	// devSmall & devBig only have Mihomo direct/proxy_raw (no class='total').
	// devNlbw has BOTH nlbwmon total (5000) and Mihomo proxy_raw (1000) -> must prefer total (5000) without double counting.
	if _, err := store.ApplyLedgerOnce(ctx, "site-a", "dc-router", "boot-1", 1, now, []accounting.Increment{
		{DeviceID: devSmall, Class: accounting.ClassDirect, Rx: 100, Tx: 50},
		{DeviceID: devBig, Class: accounting.ClassDirect, Rx: 400, Tx: 100},
		{DeviceID: devBig, Class: accounting.ClassProxyRaw, Outbound: "HK-01", Rx: 1500, Tx: 500},
		{DeviceID: devNlbw, Class: accounting.ClassTotal, Rx: 4000, Tx: 1000},
		{DeviceID: devNlbw, Class: accounting.ClassProxyRaw, Outbound: "HK-01", Rx: 800, Tx: 200},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify TrafficSeries(group="device", class="total") includes Mihomo-only devices and prefers nlbwmon total.
	series, err := store.TrafficSeries(ctx, TrafficQuery{
		From:  now.Add(-time.Hour),
		To:    now.Add(time.Minute),
		Group: "device",
		Class: accounting.ClassTotal,
	})
	if err != nil {
		t.Fatalf("TrafficSeries device: %v", err)
	}
	if got := series.Totals[devSmall]; got != 150 {
		t.Fatalf("devSmall series total = %d, want 150", got)
	}
	if got := series.Totals[devBig]; got != 2500 {
		t.Fatalf("devBig series total = %d, want 2500", got)
	}
	if got := series.Totals[devNlbw]; got != 5000 {
		t.Fatalf("devNlbw series total = %d, want 5000 (no double count)", got)
	}

	// Verify ListDeviceLedgersInRange sorts by Total descending: devNlbw (5000) > devBig (2500) > devSmall (150).
	devs, err := store.ListDeviceLedgersInRange(ctx, now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListDeviceLedgersInRange: %v", err)
	}
	if len(devs) != 3 || devs[0].ID != devNlbw || devs[1].ID != devBig || devs[2].ID != devSmall {
		t.Fatalf("unexpected device order: %+v", devs)
	}
}
