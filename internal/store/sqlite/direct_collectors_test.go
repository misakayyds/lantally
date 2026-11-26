package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/enroll"
)

func strPtr(v string) *string { return &v }
func boolPtr(v bool) *bool    { return &v }
func intPtr(v int) *int       { return &v }

func TestOpenMigratesV13ToV14WithBackupAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "lantally.db")

	// Create a v13 schema database manually.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mig := range []string{
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
		"migrations/0013_watch_providers.sql",
	} {
		raw, readErr := migrations.ReadFile(mig)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := db.Exec(string(raw)); execErr != nil {
			t.Fatalf("%s: %v", mig, execErr)
		}
	}
	if _, err := db.Exec(`INSERT INTO nodes (id, site_id, credential_id, token_hash) VALUES ('agent-v13', 'site-a', 'cred-v13', X'00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 13`); err != nil {
		t.Fatal(err)
	}
	db.SetMaxIdleConns(0)
	_ = db.Close()

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open v13->v14: %v", err)
	}

	baks, err := filepath.Glob(filepath.Join(dir, "lantally.db.bak-*"))
	if err != nil || len(baks) != 1 {
		t.Fatalf("expected 1 pre-migration backup, got %v (err=%v)", baks, err)
	}
	ver, err := sqliteUserVersion(store.db)
	if err != nil || ver != 14 {
		t.Fatalf("user_version = %d, err=%v; want 14", ver, err)
	}
	if ok, err := tableExists(store.db, "direct_collectors"); err != nil || !ok {
		t.Fatalf("direct_collectors missing: ok=%v err=%v", ok, err)
	}
	node, err := store.GetNode(context.Background(), "agent-v13")
	if err != nil {
		t.Fatalf("GetNode legacy v13 node: %v", err)
	}
	if node.Mode != "agent" {
		t.Fatalf("legacy node mode = %q, want agent", node.Mode)
	}
	_ = store.Close()

	// Idempotent reopen should not create a second backup.
	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen v14: %v", err)
	}
	_ = store2.Close()
	baks2, _ := filepath.Glob(filepath.Join(dir, "lantally.db.bak-*"))
	if len(baks2) != 1 {
		t.Fatalf("expected still 1 backup after reopen, got %v", baks2)
	}
}

func TestDirectCollectorSecretsEncryptionAndKeyPersistence(t *testing.T) {
	ctx := context.Background()

	// 1. In-memory DB uses ephemeral key without creating disk files.
	memStore, err := Open("file:dc-mem-secret?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer memStore.Close()

	created, err := memStore.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:   strPtr("MemDC"),
		SiteID: strPtr("site-a"),
		NodeID: strPtr("dc-mem-1"),
		URL:    strPtr("http://127.0.0.1:9090?token=url-secret-1"),
		Secret: strPtr("bearer-secret-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.HasSecret || created.Secret != "********" {
		t.Fatalf("expected masked secret, got has=%v secret=%q", created.HasSecret, created.Secret)
	}
	if strings.Contains(created.URL, "url-secret-1") {
		t.Fatalf("URL not redacted in CreateDirectCollector: %q", created.URL)
	}

	// Verify DB column holds encrypted envelope, not plaintext.
	var blob []byte
	if err := memStore.db.QueryRow(`SELECT secret_enc FROM direct_collectors WHERE id = ?`, created.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if len(blob) < 29 || strings.Contains(string(blob), "bearer-secret-1") {
		t.Fatalf("secret_enc invalid or contains plaintext: len=%d", len(blob))
	}
	rt, err := memStore.GetDirectCollectorRuntime(ctx, created.ID)
	if err != nil || rt.SecretPlain != "bearer-secret-1" || !strings.Contains(rt.RawURL, "url-secret-1") {
		t.Fatalf("runtime decrypt mismatch: rt=%+v err=%v", rt, err)
	}

	// 2. File-backed DB creates .key sidecar and persists across Close/Open without env var.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "lantally.db")
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	dc1, err := s1.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:   strPtr("FileDC"),
		URL:    strPtr("http://127.0.0.1:9091"),
		Secret: strPtr("persist-secret-xyz"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	keyInfo, err := os.Stat(dbPath + ".key")
	if err != nil {
		t.Fatalf("expected sidecar key file: %v", err)
	}
	if keyInfo.Size() != secretKeySize {
		t.Fatalf("key file size = %d, want %d", keyInfo.Size(), secretKeySize)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rt2, err := s2.GetDirectCollectorRuntime(ctx, dc1.ID)
	if err != nil || rt2.SecretPlain != "persist-secret-xyz" {
		t.Fatalf("persisted decrypt failed: rt=%+v err=%v", rt2, err)
	}

	// 3. LANTALLY_SECRET_KEY overrides deterministically via HKDF.
	t.Setenv("LANTALLY_SECRET_KEY", "synthetic-kdf-master-passphrase")
	envDB := filepath.Join(t.TempDir(), "env.db")
	se1, err := Open(envDB)
	if err != nil {
		t.Fatal(err)
	}
	dcEnv, err := se1.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:   strPtr("EnvDC"),
		URL:    strPtr("ws://127.0.0.1:9092"),
		Secret: strPtr("env-secret-abc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = se1.Close()
	if _, err := os.Stat(envDB + ".key"); !os.IsNotExist(err) {
		t.Fatalf("expected no .key file when LANTALLY_SECRET_KEY is set, got err=%v", err)
	}
	se2, err := Open(envDB)
	if err != nil {
		t.Fatal(err)
	}
	defer se2.Close()
	rtEnv, err := se2.GetDirectCollectorRuntime(ctx, dcEnv.ID)
	if err != nil || rtEnv.SecretPlain != "env-secret-abc" {
		t.Fatalf("env KDF decrypt failed: rt=%+v err=%v", rtEnv, err)
	}
}

func TestDirectCollectorCRUDValidationNodeSyncAndSilenceFilter(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Enroll an active agent node to verify collision protection.
	agentTok := tokenFor("agent-cred", 'a')
	if err := store.CreateNode(ctx, "agent-1", "site-a", "agent-cred", enroll.HashToken(agentTok)); err != nil {
		t.Fatal(err)
	}

	// Validation errors -> ErrInvalidInput.
	badInputs := []DirectCollectorInput{
		{Name: strPtr(""), SiteID: strPtr("site-a"), NodeID: strPtr("dc-1"), URL: strPtr("http://127.0.0.1:9090")},
		{Name: strPtr("DC"), SiteID: strPtr(""), NodeID: strPtr("dc-1"), URL: strPtr("http://127.0.0.1:9090")},
		{Name: strPtr("DC"), SiteID: strPtr("site-a"), NodeID: strPtr(""), URL: strPtr("http://127.0.0.1:9090")},
		{Name: strPtr("DC"), SiteID: strPtr("site-a"), NodeID: strPtr("dc-1"), URL: strPtr("not-a-url")},
		{Name: strPtr("DC"), SiteID: strPtr("site-a"), NodeID: strPtr("dc-1"), URL: strPtr("file:///etc/passwd")},
		{Name: strPtr("DC"), SiteID: strPtr("site-a"), NodeID: strPtr("dc-1"), URL: strPtr("http://127.0.0.1:9090"), PollIntervalMS: intPtr(50)},
		{Name: strPtr("DC"), SiteID: strPtr("site-a"), NodeID: strPtr("agent-1"), URL: strPtr("http://127.0.0.1:9090")},
	}
	for i, in := range badInputs {
		if _, err := store.CreateDirectCollector(ctx, in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("bad input #%d err=%v, want ErrInvalidInput", i, err)
		}
	}

	// Omitted site_id/node_id generates defaults cleanly.
	dc, err := store.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:   strPtr("Router Alpha"),
		URL:    strPtr("http://127.0.0.1:9090?token=tok-a"),
		Secret: strPtr("sec-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dc.SiteID != "default" || !strings.HasPrefix(dc.NodeID, "dc-router-alpha-") {
		t.Fatalf("unexpected generated defaults: site=%q node=%q", dc.SiteID, dc.NodeID)
	}
	if dc.PollIntervalMS != 15000 {
		t.Fatalf("default poll_interval_ms = %d, want 15000", dc.PollIntervalMS)
	}

	// Verify synced node row appears in ListNodes with mode='direct' and fails Authenticate.
	node, err := store.GetNode(ctx, dc.NodeID)
	if err != nil || node.Mode != "direct" {
		t.Fatalf("GetNode direct node = %+v, err=%v", node, err)
	}
	if _, err := store.Authenticate(ctx, "lt_direct:"+dc.NodeID+"_"+strings.Repeat("x", 32)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for direct node auth, got %v", err)
	}

	// Update omitting Secret and CollectDomains preserves both.
	updated, err := store.UpdateDirectCollector(ctx, dc.ID, DirectCollectorInput{
		Name: strPtr("Router Beta"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.HasSecret || updated.Name != "Router Beta" {
		t.Fatalf("unexpected update result: %+v", updated)
	}
	rt, err := store.GetDirectCollectorRuntime(ctx, dc.ID)
	if err != nil || rt.SecretPlain != "sec-a" {
		t.Fatalf("secret not preserved across omitted update: rt=%+v err=%v", rt, err)
	}

	// Update with "********" also preserves secret.
	if _, err := store.UpdateDirectCollector(ctx, dc.ID, DirectCollectorInput{Secret: strPtr("********")}); err != nil {
		t.Fatal(err)
	}
	rt, _ = store.GetDirectCollectorRuntime(ctx, dc.ID)
	if rt.SecretPlain != "sec-a" {
		t.Fatalf("secret overwritten by mask: %q", rt.SecretPlain)
	}

	// Bidirectional collect_domains sync: SetNodeCollectDomains -> direct_collectors.
	if err := store.SetNodeCollectDomains(ctx, dc.NodeID, true); err != nil {
		t.Fatal(err)
	}
	gotDC, err := store.GetDirectCollector(ctx, dc.ID)
	if err != nil || !gotDC.CollectDomains {
		t.Fatalf("SetNodeCollectDomains did not sync to direct_collectors: %+v, err=%v", gotDC, err)
	}

	// UpdateDirectCollector collect_domains=false -> nodes.
	if _, err := store.UpdateDirectCollector(ctx, dc.ID, DirectCollectorInput{CollectDomains: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	node, _ = store.GetNode(ctx, dc.NodeID)
	if node.CollectDomains {
		t.Fatalf("UpdateDirectCollector did not sync collect_domains=false to nodes")
	}

	// Status update updates last_seen_at on both direct_collectors and nodes, and redacts errors.
	now := time.Now().UTC()
	if err := store.UpdateDirectCollectorStatus(ctx, dc.ID, now, ""); err != nil {
		t.Fatal(err)
	}
	node, _ = store.GetNode(ctx, dc.NodeID)
	if node.LastSeenAt == "" {
		t.Fatal("expected nodes.last_seen_at to be updated")
	}
	if err := store.UpdateDirectCollectorStatus(ctx, dc.ID, time.Time{}, `Get "http://127.0.0.1:9090?token=tok-a": Bearer sec-a failed`); err != nil {
		t.Fatal(err)
	}
	gotDC, _ = store.GetDirectCollector(ctx, dc.ID)
	if strings.Contains(gotDC.LastError, "tok-a") || strings.Contains(gotDC.LastError, "sec-a") {
		t.Fatalf("last_error leaked secrets: %q", gotDC.LastError)
	}
	if gotDC.LastSeenAt == "" {
		t.Fatal("expected error update to preserve previous last_seen_at")
	}

	// Disabled direct collector must NOT raise silence alerts even when last_seen_at is stale.
	if _, err := store.ToggleDirectCollector(ctx, dc.ID, boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateAlerts(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	alerts, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, al := range alerts {
		if al.Kind == "silence" && al.NodeID == dc.NodeID {
			t.Fatalf("disabled direct collector raised false silence alert: %+v", al)
		}
	}

	// Clear secret via "" and Delete revokes node row.
	cleared, err := store.UpdateDirectCollector(ctx, dc.ID, DirectCollectorInput{Secret: strPtr("")})
	if err != nil || cleared.HasSecret {
		t.Fatalf("expected cleared secret, got %+v err=%v", cleared, err)
	}
	if err := store.DeleteDirectCollector(ctx, dc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetDirectCollector(ctx, dc.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows after delete, got %v", err)
	}
	if _, err := store.GetNode(ctx, dc.NodeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected direct node to be marked revoked after delete, got %v", err)
	}
}

func TestValidateDirectCollectorURLDoesNotLeakMalformedURLSecrets(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	badURL := "wss://admin:LeakPass111@127.0.0.1:badport?token=LeakTok222&secret=LeakQSec333"
	secrets := []string{"LeakPass111", "LeakTok222", "LeakQSec333"}

	if _, err := ValidateDirectCollectorURL(badURL); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ValidateDirectCollectorURL err=%v, want ErrInvalidInput", err)
	} else {
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Fatalf("ValidateDirectCollectorURL leaked %q in %q", s, err.Error())
			}
		}
	}

	if _, err := store.CreateDirectCollector(ctx, DirectCollectorInput{
		Name: strPtr("BadURL"),
		URL:  strPtr(badURL),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateDirectCollector err=%v, want ErrInvalidInput", err)
	} else {
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Fatalf("CreateDirectCollector leaked %q in %q", s, err.Error())
			}
		}
	}

	good, err := store.CreateDirectCollector(ctx, DirectCollectorInput{
		Name: strPtr("GoodURL"),
		URL:  strPtr("http://127.0.0.1:9090"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDirectCollector(ctx, good.ID, DirectCollectorInput{
		URL: strPtr(badURL),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateDirectCollector err=%v, want ErrInvalidInput", err)
	} else {
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Fatalf("UpdateDirectCollector leaked %q in %q", s, err.Error())
			}
		}
	}
}

func TestToggleDirectCollectorRestoresRevokedDirectNode(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	dc, err := store.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:           strPtr("RevokableDC"),
		SiteID:         strPtr("site-a"),
		NodeID:         strPtr("dc-rev-1"),
		URL:            strPtr("http://127.0.0.1:9090"),
		Enabled:        boolPtr(true),
		CollectDomains: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RevokeNode(ctx, dc.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNode(ctx, dc.NodeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected revoked node hidden from GetNode, got %v", err)
	}
	if cd, _, err := store.NodeDomainPreferences(ctx, dc.NodeID); err != nil || cd {
		t.Fatalf("expected revoked node NodeDomainPreferences=false, got cd=%v err=%v", cd, err)
	}

	// Re-enabling via ToggleDirectCollector must restore nodes.revoked = 0.
	toggled, err := store.ToggleDirectCollector(ctx, dc.ID, boolPtr(true))
	if err != nil {
		t.Fatal(err)
	}
	if !toggled.Enabled {
		t.Fatalf("expected enabled=true after toggle")
	}

	node, err := store.GetNode(ctx, dc.NodeID)
	if err != nil || node.Mode != "direct" || !node.CollectDomains {
		t.Fatalf("expected restored direct node in GetNode, got %+v err=%v", node, err)
	}
	nodes, err := store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == dc.NodeID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected restored direct node %q in ListNodes: %+v", dc.NodeID, nodes)
	}
	if cd, _, err := store.NodeDomainPreferences(ctx, dc.NodeID); err != nil || !cd {
		t.Fatalf("expected NodeDomainPreferences=true after restore, got cd=%v err=%v", cd, err)
	}
}

func TestFileMemoryDSNAndSecretKeyMismatchRecovery(t *testing.T) {
	ctx := context.Background()

	// file::memory: DSN must be treated as in-memory and create no .key file.
	workDir := t.TempDir()
	t.Chdir(workDir)
	memStore, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	_ = memStore.Close()
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected zero files created in cwd for file::memory: DSN, got %v", entries)
	}

	// Key mismatch recovery: Open + ListEnabledDirectCollectorsRuntime must succeed,
	// record last_error on the mismatched collector, and keep other collectors usable.
	dbPath := filepath.Join(t.TempDir(), "mismatch.db")
	t.Setenv("LANTALLY_SECRET_KEY", "master-key-v1")
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	withSecret, err := s1.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:   strPtr("WithSecret"),
		NodeID: strPtr("dc-mm-sec"),
		URL:    strPtr("http://127.0.0.1:9090"),
		Secret: strPtr("secret-v1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutSecret, err := s1.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:   strPtr("NoSecret"),
		NodeID: strPtr("dc-mm-nosec"),
		URL:    strPtr("http://127.0.0.1:9091"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	t.Setenv("LANTALLY_SECRET_KEY", "master-key-v2")
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after key rotation failed: %v", err)
	}
	defer s2.Close()

	runtimeList, err := s2.ListEnabledDirectCollectorsRuntime(ctx)
	if err != nil {
		t.Fatalf("ListEnabledDirectCollectorsRuntime failed on key mismatch: %v", err)
	}
	if len(runtimeList) != 1 || runtimeList[0].ID != withoutSecret.ID {
		t.Fatalf("expected only secretless collector in runtime list, got %+v", runtimeList)
	}

	pub, err := s2.GetDirectCollector(ctx, withSecret.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pub.LastError, "key mismatch") {
		t.Fatalf("expected key mismatch in last_error, got %q", pub.LastError)
	}

	// Re-entering secret under new key recovers the collector.
	if _, err := s2.UpdateDirectCollector(ctx, withSecret.ID, DirectCollectorInput{
		Secret: strPtr("secret-v2"),
	}); err != nil {
		t.Fatal(err)
	}
	rtRecovered, err := s2.GetDirectCollectorRuntime(ctx, withSecret.ID)
	if err != nil || rtRecovered.SecretPlain != "secret-v2" {
		t.Fatalf("recovered decrypt failed: rt=%+v err=%v", rtRecovered, err)
	}
}

func TestUpdateDirectCollectorWithoutSecretAndAfterClearSecret(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// 1. Secret-less collector created with Secret: nil.
	noSec, err := store.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:   strPtr("NoSecretDC"),
		SiteID: strPtr("site-a"),
		NodeID: strPtr("dc-nosec-1"),
		URL:    strPtr("http://127.0.0.1:9090"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if noSec.HasSecret {
		t.Fatalf("expected HasSecret=false, got %+v", noSec)
	}
	updatedNoSec, err := store.UpdateDirectCollector(ctx, noSec.ID, DirectCollectorInput{
		Name: strPtr("RenamedNoSec"),
	})
	if err != nil {
		t.Fatalf("UpdateDirectCollector on secret-less collector failed: %v", err)
	}
	if updatedNoSec.HasSecret || updatedNoSec.Name != "RenamedNoSec" {
		t.Fatalf("unexpected update result: %+v", updatedNoSec)
	}
	rtNoSec, err := store.GetDirectCollectorRuntime(ctx, noSec.ID)
	if err != nil || rtNoSec.SecretPlain != "" {
		t.Fatalf("runtime secret-less = %+v err=%v", rtNoSec, err)
	}

	// 2. Collector with secret -> ClearSecret: true -> subsequent update without secret.
	withSec, err := store.CreateDirectCollector(ctx, DirectCollectorInput{
		Name:   strPtr("ClearedDC"),
		SiteID: strPtr("site-a"),
		NodeID: strPtr("dc-clear-1"),
		URL:    strPtr("http://127.0.0.1:9091?token=raw-tok-1"),
		Secret: strPtr("sec-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDirectCollector(ctx, withSec.ID, DirectCollectorInput{
		ClearSecret: true,
	}); err != nil {
		t.Fatalf("ClearSecret update failed: %v", err)
	}
	afterClear, err := store.UpdateDirectCollector(ctx, withSec.ID, DirectCollectorInput{
		Name: strPtr("AfterClear"),
		URL:  strPtr("http://127.0.0.1:9091?token=[redacted]"),
	})
	if err != nil {
		t.Fatalf("UpdateDirectCollector after ClearSecret failed: %v", err)
	}
	if afterClear.HasSecret || afterClear.Name != "AfterClear" {
		t.Fatalf("unexpected afterClear result: %+v", afterClear)
	}
	rtClear, err := store.GetDirectCollectorRuntime(ctx, withSec.ID)
	if err != nil || rtClear.SecretPlain != "" || rtClear.RawURL != "http://127.0.0.1:9091?token=raw-tok-1" {
		t.Fatalf("runtime afterClear mismatch: rt=%+v err=%v", rtClear, err)
	}

	// 3. Different URL containing literal [redacted] is rejected with ErrInvalidInput.
	if _, err := store.UpdateDirectCollector(ctx, withSec.ID, DirectCollectorInput{
		URL: strPtr("http://127.0.0.1:9999?token=[redacted]"),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for tampered [redacted] URL, got %v", err)
	}
}
