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
