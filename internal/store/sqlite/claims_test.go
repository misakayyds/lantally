package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRedeemClaimOnceAndRejectsExpired(t *testing.T) {
	store, err := Open("file:r5-claims?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	claim, err := store.CreateClaim(ctx, "home", "laptop", true, now)
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.RedeemClaim(ctx, claim.Code, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != "laptop" || !got.Local {
		t.Fatalf("redeemed = %+v", got)
	}
	if _, err := store.RedeemClaim(ctx, claim.Code, now.Add(2*time.Minute)); !errors.Is(err, ErrClaimUsed) {
		t.Fatalf("second redeem = %v, want used", err)
	}

	expired, err := store.CreateClaim(ctx, "home", "stale", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RedeemClaim(ctx, expired.Code, now.Add(11*time.Minute)); !errors.Is(err, ErrClaimExpired) {
		t.Fatalf("expired redeem = %v, want expired", err)
	}
	if _, err := store.RedeemClaim(ctx, "NOPECODE", now); !errors.Is(err, ErrClaimNotFound) {
		t.Fatalf("missing redeem = %v, want not found", err)
	}
}

func TestRedeemClaimSubsecondBoundary(t *testing.T) {
	store, err := Open("file:r5-claims-subsec?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	// CreateClaim adds 10m -> expires at 08:10:00.500000000Z.
	createdAt := time.Date(2026, 8, 19, 8, 0, 0, 500_000_000, time.UTC)
	claim, err := store.CreateClaim(ctx, "home", "subsec-node", false, createdAt)
	if err != nil {
		t.Fatal(err)
	}

	// Redeem at exact whole second 08:10:00.000000000Z (0.5s before expiry):
	// RFC3339Nano lexicographic comparison used to fail here because '.' (46) < 'Z' (90).
	wholeSecondNow := claim.ExpiresAt.Truncate(time.Second)
	got, err := store.RedeemClaim(ctx, claim.Code, wholeSecondNow)
	if err != nil {
		t.Fatalf("RedeemClaim at whole-second boundary failed: %v", err)
	}
	if got.NodeID != "subsec-node" {
		t.Fatalf("redeemed NodeID = %q, want subsec-node", got.NodeID)
	}

	// Also verify legacy variable-length RFC3339Nano row in SQLite still works.
	legacyExpires := time.Date(2026, 8, 19, 9, 0, 0, 500_000_000, time.UTC)
	if _, err := store.db.ExecContext(
		ctx,
		`INSERT INTO claim_codes (code, site_id, node_id, local_mode, expires_at) VALUES (?, ?, ?, 0, ?)`,
		"LEGACY01", "home", "legacy-node", legacyExpires.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	if gotLegacy, err := store.RedeemClaim(ctx, "LEGACY01", legacyExpires.Truncate(time.Second)); err != nil || gotLegacy.NodeID != "legacy-node" {
		t.Fatalf("legacy subsecond claim failed: got=%+v err=%v", gotLegacy, err)
	}
}
