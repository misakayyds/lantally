package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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
