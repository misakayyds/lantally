package ingest

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/misakayyds/lantally/internal/collector/sim"
	"github.com/misakayyds/lantally/internal/enroll"
	"github.com/misakayyds/lantally/internal/protocol"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

const (
	demoSiteID = "home"
	demoNodeID = "demo-gateway"
	demoBootID = "demo-boot"
	demoSeqs   = 24
)

// SeedSynthetic enrolls a documentation-only node and writes simulated
// batches so README screenshots and local previews have charts.
func SeedSynthetic(ctx context.Context, store *sqlitestore.Store, now time.Time) error {
	_, err := store.GetNode(ctx, demoNodeID)
	if err == nil {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	token, credentialID, err := enroll.IssueToken()
	if err != nil {
		return err
	}
	if err := store.CreateNode(ctx, demoNodeID, demoSiteID, credentialID, enroll.HashToken(token)); err != nil {
		return err
	}
	handler := NewHandler(store)
	for i := 1; i <= demoSeqs; i++ {
		batch := sim.Snapshot(uint64(i), demoBootID)
		batch.SiteID = demoSiteID
		batch.NodeID = demoNodeID
		batch.SampledAt = now.UTC().Add(-time.Duration(demoSeqs-i) * 3 * time.Hour)
		raw, err := protocol.Encode(batch)
		if err != nil {
			return err
		}
		inserted, err := store.InsertBatch(ctx, batch.NodeID, batch.BootID, batch.Sequence, raw)
		if err != nil {
			return err
		}
		if err := handler.account(ctx, batch); err != nil {
			return err
		}
		if err := handler.observeNode(ctx, batch, inserted); err != nil {
			return err
		}
	}
	return nil
}
