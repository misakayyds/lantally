package identity_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/identity"
	"github.com/misakayyds/lantally/internal/protocol"
	"github.com/misakayyds/lantally/internal/store/sqlite"
)

var observedAt = time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)

func openResolver(t *testing.T) (*sqlite.Store, *identity.Resolver) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, identity.NewResolver(store, "node-a")
}

func observation(ip, mac string) protocol.DeviceDelta {
	return protocol.DeviceDelta{
		ObsIP:  ip,
		ObsMAC: mac,
		Source: protocol.SourceNeigh,
	}
}

func resolve(t *testing.T, resolver *identity.Resolver, obs protocol.DeviceDelta) string {
	t.Helper()
	deviceID, _, err := resolver.Resolve("site-a", obs, observedAt)
	if err != nil {
		t.Fatal(err)
	}
	return deviceID
}

func TestResolveSameExactMACAcrossBatches(t *testing.T) {
	_, resolver := openResolver(t)

	first := resolve(t, resolver, observation("203.0.113.10", "02:00:00:00:00:01"))
	second := resolve(t, resolver, observation("203.0.113.11", "02:00:00:00:00:01"))

	if first != second {
		t.Fatalf("same exact MAC resolved to %q and %q", first, second)
	}
}

func TestResolveDoesNotMergeDifferentRandomizedMACs(t *testing.T) {
	_, resolver := openResolver(t)

	first := resolve(t, resolver, observation("203.0.113.20", "02:00:00:00:00:02"))
	second := resolve(t, resolver, observation("203.0.113.21", "02:00:00:00:00:03"))

	if first == second {
		t.Fatalf("different locally administered MACs merged as %q", first)
	}
}

func TestResolveDoesNotMergeIPOnlyObservationWithRandomizedMAC(t *testing.T) {
	_, resolver := openResolver(t)

	withMAC := resolve(t, resolver, observation("203.0.113.30", "02:00:00:00:00:04"))
	ipOnly := resolve(t, resolver, observation("203.0.113.30", ""))

	if withMAC == ipOnly {
		t.Fatalf("IP-only observation merged with randomized MAC as %q", withMAC)
	}
}

func TestResolveScopesIPOnlyIdentityToTimeWindow(t *testing.T) {
	_, resolver := openResolver(t)
	obs := observation("203.0.113.31", "")

	first, _, err := resolver.Resolve("site-a", obs, observedAt)
	if err != nil {
		t.Fatal(err)
	}
	later, _, err := resolver.Resolve("site-a", obs, observedAt.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if first == later {
		t.Fatalf("IP-only observations outside the time window merged as %q", first)
	}
}

func TestConflictingPinsNeverMerge(t *testing.T) {
	_, resolver := openResolver(t)
	first := resolve(t, resolver, observation("203.0.113.40", "02:00:00:00:00:05"))
	second := resolve(t, resolver, observation("203.0.113.41", "02:00:00:00:00:06"))

	if err := resolver.Pin("site-a", first, "pinned-alpha"); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Pin("site-a", second, "pinned-beta"); err != nil {
		t.Fatal(err)
	}
	err := resolver.Merge(first, second, identity.Evidence{
		Rank:  identity.RankDHCPNeigh,
		Value: "synthetic association",
	})
	if !errors.Is(err, identity.ErrPinnedConflict) {
		t.Fatalf("merge error = %v, want ErrPinnedConflict", err)
	}
}

func TestUnmergeRestoresTwoIdentities(t *testing.T) {
	_, resolver := openResolver(t)
	firstObs := observation("203.0.113.50", "02:00:00:00:00:07")
	secondObs := observation("203.0.113.51", "02:00:00:00:00:08")
	first := resolve(t, resolver, firstObs)
	second := resolve(t, resolver, secondObs)

	if err := resolver.Merge(first, second, identity.Evidence{
		Rank:  identity.RankDHCPNeigh,
		Value: "synthetic association",
	}); err != nil {
		t.Fatal(err)
	}
	if got := resolve(t, resolver, secondObs); got != first {
		t.Fatalf("merged device resolved to %q, want %q", got, first)
	}

	if err := resolver.Unmerge(first); err != nil {
		t.Fatal(err)
	}
	restoredFirst := resolve(t, resolver, firstObs)
	restoredSecond := resolve(t, resolver, secondObs)
	if restoredFirst == restoredSecond {
		t.Fatalf("unmerge left one identity %q", restoredFirst)
	}
	if restoredFirst != first || restoredSecond != second {
		t.Fatalf("restored IDs = (%q, %q), want (%q, %q)",
			restoredFirst, restoredSecond, first, second)
	}
}

func TestResolvePersistsEvidenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lantally.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	resolver := identity.NewResolver(store, "node-a")
	first := resolve(t, resolver, observation("203.0.113.60", "02:00:00:00:00:09"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second := resolve(t, identity.NewResolver(reopened, "node-a"),
		observation("203.0.113.61", "02:00:00:00:00:09"))
	if first != second {
		t.Fatalf("persisted MAC evidence resolved to %q and %q", first, second)
	}
}

func TestResolveSurfacesConflictingEvidence(t *testing.T) {
	store, resolver := openResolver(t)
	mac := "02:00:00:00:00:0a"
	first := resolve(t, resolver, observation("203.0.113.70", mac))
	second := resolve(t, resolver, observation("203.0.113.71", "02:00:00:00:00:0b"))

	if err := store.AttachIdentityEvidence(
		"site-a",
		second,
		int(identity.RankStableMAC),
		mac,
		"node-a",
		observedAt,
	); err != nil {
		t.Fatal(err)
	}
	count, err := store.IdentityEvidenceCount("site-a", int(identity.RankStableMAC), mac)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("conflicting evidence count = %d, want 2", count)
	}

	_, limitation, err := resolver.Resolve("site-a", observation("203.0.113.72", mac), observedAt)
	if !errors.Is(err, identity.ErrConflictingEvidence) {
		t.Fatalf("resolve error = %v, want ErrConflictingEvidence", err)
	}
	if limitation != "conflicting identity evidence; identities were not auto-merged" {
		t.Fatalf("limitation = %q", limitation)
	}
	if first == second {
		t.Fatalf("setup created identical identities %q", first)
	}
}

func TestResolveUsesStableMACBeforeScopedIP(t *testing.T) {
	_, resolver := openResolver(t)
	mac := "02:00:00:00:00:0c"
	macIdentity := resolve(t, resolver, observation("203.0.113.80", mac))
	ipOnly := resolve(t, resolver, observation("203.0.113.80", ""))

	got := resolve(t, resolver, observation("203.0.113.80", mac))
	if got != macIdentity {
		t.Fatalf("stable MAC resolve = %q, want %q", got, macIdentity)
	}
	if got == ipOnly {
		t.Fatalf("scoped IP identity %q won over stable MAC", ipOnly)
	}
}

func TestPackageLevelResolveMergeUnmerge(t *testing.T) {
	_, resolver := openResolver(t)
	identity.SetResolver(resolver)
	t.Cleanup(func() { identity.SetResolver(nil) })

	firstObs := observation("203.0.113.90", "02:00:00:00:00:0d")
	secondObs := observation("203.0.113.91", "02:00:00:00:00:0e")
	first, _, err := identity.Resolve("site-a", firstObs, observedAt)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := identity.Resolve("site-a", secondObs, observedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.Merge(first, second, identity.Evidence{
		Rank:  identity.RankDHCPNeigh,
		Value: "synthetic association",
	}); err != nil {
		t.Fatal(err)
	}
	if err := identity.Unmerge(first); err != nil {
		t.Fatal(err)
	}
	restoredFirst, _, err := identity.Resolve("site-a", firstObs, observedAt)
	if err != nil {
		t.Fatal(err)
	}
	restoredSecond, _, err := identity.Resolve("site-a", secondObs, observedAt)
	if err != nil {
		t.Fatal(err)
	}
	if restoredFirst == restoredSecond {
		t.Fatalf("package helpers left one identity %q", restoredFirst)
	}
}
