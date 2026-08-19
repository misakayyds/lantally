package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

func TestSeedSyntheticIsIdempotentAndAccountsTraffic(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	if err := SeedSynthetic(ctx, store, now); err != nil {
		t.Fatal(err)
	}
	if err := SeedSynthetic(ctx, store, now); err != nil {
		t.Fatal(err)
	}
	count, err := store.BatchCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != demoSeqs {
		t.Fatalf("batches = %d, want %d after idempotent seed", count, demoSeqs)
	}
	totals, err := store.LedgerTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if totals["total"] == 0 {
		t.Fatalf("demo seed produced no total bytes: %+v", totals)
	}
}
