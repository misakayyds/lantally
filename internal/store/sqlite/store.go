package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/enroll"
	_ "modernc.org/sqlite"
)

var (
	ErrSequenceRegression = errors.New("sequence regression")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrInvalidInput       = errors.New("invalid input")
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db           *sql.DB
	databasePath string
	secretKey    []byte
}

const schemaVersion = 14

type RxTxTotal struct {
	Rx    uint64 `json:"rx"`
	Tx    uint64 `json:"tx"`
	Total uint64 `json:"total"`
}

type Node struct {
	ID             string `json:"id"`
	SiteID         string `json:"site_id"`
	Mode           string `json:"mode,omitempty"`
	LastSeenAt     string `json:"last_seen_at,omitempty"`
	LastBootID     string `json:"last_boot_id,omitempty"`
	CollectDomains bool   `json:"collect_domains"`
}

type DeviceLedger struct {
	ID        string               `json:"id"`
	SiteID    string               `json:"site_id"`
	Name      string               `json:"name"`
	IP        string               `json:"ip,omitempty"`
	MAC       string               `json:"mac,omitempty"`
	Rx        uint64               `json:"rx"`
	Tx        uint64               `json:"tx"`
	Total     uint64               `json:"total"`
	Bytes     map[string]uint64    `json:"bytes"`
	Breakdown map[string]RxTxTotal `json:"breakdown,omitempty"`
}

type OutboundLedger struct {
	Name            string  `json:"name"`
	Rx              uint64  `json:"rx"`
	Tx              uint64  `json:"tx"`
	Total           uint64  `json:"total"`
	Raw             uint64  `json:"raw"`
	RawRx           uint64  `json:"raw_rx"`
	RawTx           uint64  `json:"raw_tx"`
	Factor          float64 `json:"factor"`
	Adjusted        uint64  `json:"adjusted"`
	AdjustedRx      uint64  `json:"adjusted_rx"`
	AdjustedTx      uint64  `json:"adjusted_tx"`
	Unadjusted      uint64  `json:"unadjusted"`
	Configured      bool    `json:"configured"`
	MatchSource     string  `json:"match_source"`
	MatchedRule     string  `json:"matched_rule,omitempty"`
	MatchedRuleID   int64   `json:"matched_rule_id,omitempty"`
	MatchedRuleName string  `json:"matched_rule_name,omitempty"`
	MatchedType     string  `json:"matched_type,omitempty"`
	Share           float64 `json:"share"`
}

type ProxyNodeCatalogEntry struct {
	Name         string
	ProviderName string
}

func Open(dsn string) (*Store, error) {
	existing := fileDSNSize(dsn) > 0
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, databasePath: dsn}
	if err := store.Ping(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 10000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if existing {
		version, err := sqliteUserVersion(db)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		if version < schemaVersion {
			if _, err := store.Backup(); err != nil {
				_ = db.Close()
				return nil, err
			}
		}
	}
	if err := configureSQLitePragmas(db, dsn); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !isMemoryDSN(dsn) {
		_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	secretKey, err := loadOrCreateSecretKey(dsn)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store.secretKey = secretKey
	return store, nil
}

func configureSQLitePragmas(db *sql.DB, dsn string) error {
	pragmas := []string{
		`PRAGMA busy_timeout = 10000`,
		`PRAGMA temp_store = MEMORY`,
	}
	if !isMemoryDSN(dsn) {
		pragmas = append(pragmas,
			`PRAGMA journal_mode = WAL`,
			`PRAGMA synchronous = NORMAL`,
			`PRAGMA cache_size = -16000`,
			`PRAGMA wal_autocheckpoint = 2000`,
		)
	}
	for _, stmt := range pragmas {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("apply sqlite pragma %q: %w", stmt, err)
		}
	}
	return nil
}

func fileDSNSize(dsn string) int64 {
	if isMemoryDSN(dsn) {
		return 0
	}
	info, err := os.Stat(dsn)
	if err != nil {
		return 0
	}
	return info.Size()
}

func sqliteUserVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow(`PRAGMA user_version`).Scan(&version)
	return version, err
}

// applyMigrations applies the numbered schema steps needed by both new and
// pre-credential-id databases.
func applyMigrations(db *sql.DB) error {
	schema, err := migrations.ReadFile("migrations/0001_init.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(schema)); err != nil {
		return err
	}

	hasCredentialID, err := tableHasColumn(db, "nodes", "credential_id")
	if err != nil {
		return err
	}
	if !hasCredentialID {
		migration, err := migrations.ReadFile("migrations/0002_add_node_credential_id.sql")
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(migration)); err != nil {
			return err
		}
	}
	_, err = db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS nodes_credential_id_uq
		ON nodes(credential_id)
	`)
	if err != nil {
		return err
	}

	devicesMigration, err := migrations.ReadFile("migrations/0003_devices.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(string(devicesMigration))
	if err != nil {
		return err
	}

	adminMigration, err := migrations.ReadFile("migrations/0004_admin.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(adminMigration)); err != nil {
		return err
	}

	ledgerMigration, err := migrations.ReadFile("migrations/0005_ledgers.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(ledgerMigration)); err != nil {
		return err
	}

	sampleMigration, err := migrations.ReadFile("migrations/0006_ledger_samples.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(sampleMigration)); err != nil {
		return err
	}

	hasOutbound, err := tableHasColumn(db, "ledger_totals", "outbound")
	if err != nil {
		return err
	}
	if !hasOutbound {
		outboundMigration, err := migrations.ReadFile("migrations/0007_ledger_outbound.sql")
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(outboundMigration)); err != nil {
			return err
		}
	}

	dailyMigration, err := migrations.ReadFile("migrations/0008_ledger_daily.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(dailyMigration)); err != nil {
		return err
	}

	multipliersMigration, err := migrations.ReadFile("migrations/0009_multipliers_devices.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(multipliersMigration)); err != nil {
		return err
	}
	hasDisplayName, err := tableHasColumn(db, "devices", "display_name")
	if err != nil {
		return err
	}
	if !hasDisplayName {
		if _, err := db.Exec(`ALTER TABLE devices ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	alertsMigration, err := migrations.ReadFile("migrations/0010_alerts_billing.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(alertsMigration)); err != nil {
		return err
	}
	if err := addNodeColumn(db, "last_seen_at", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := addNodeColumn(db, "last_interval_ms", `INTEGER NOT NULL DEFAULT 15000`); err != nil {
		return err
	}
	if err := addNodeColumn(db, "last_boot_id", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	claimsMigration, err := migrations.ReadFile("migrations/0011_claim_codes.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(claimsMigration)); err != nil {
		return err
	}

	hasMultiplierRules, err := tableExists(db, "multiplier_rules")
	if err != nil {
		return err
	}
	dimRulesMigration, err := migrations.ReadFile("migrations/0012_dimensions_rules.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(dimRulesMigration)); err != nil {
		return err
	}
	if !hasMultiplierRules {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO multiplier_rules
				(name, match_type, pattern, factor, priority, enabled, updated_at)
			SELECT name, 'exact', name, factor, 100, 1, updated_at
			FROM outbound_multipliers
			WHERE factor > 0
		`); err != nil {
			return err
		}
	}
	if err := addNodeColumn(db, "collect_domains", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	watchProvidersMigration, err := migrations.ReadFile("migrations/0013_watch_providers.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(watchProvidersMigration)); err != nil {
		return err
	}
	directCollectorsMigration, err := migrations.ReadFile("migrations/0014_direct_collectors.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(directCollectorsMigration)); err != nil {
		return err
	}
	if err := addNodeColumn(db, "mode", `TEXT NOT NULL DEFAULT 'agent'`); err != nil {
		return err
	}
	return nil
}

func tableExists(db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&count)
	return count > 0, err
}

func addNodeColumn(db *sql.DB, name, decl string) error {
	has, err := tableHasColumn(db, "nodes", name)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE nodes ADD COLUMN ` + name + ` ` + decl)
	return err
}

func tableHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			primaryKey   int
		)
		if err := rows.Scan(
			&cid,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if !isMemoryDSN(s.databasePath) {
		_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	// Drop idle connections so Windows can unlink the TempDir database.
	s.db.SetMaxIdleConns(0)
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) CreateNode(
	ctx context.Context,
	id, siteID, credentialID string,
	tokenHash []byte,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO nodes (id, site_id, credential_id, token_hash) VALUES (?, ?, ?, ?)`,
		id,
		siteID,
		credentialID,
		tokenHash,
	)
	return err
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, site_id, mode, last_seen_at, last_boot_id, collect_domains FROM nodes WHERE revoked = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var node Node
		var collectInt int
		if err := rows.Scan(&node.ID, &node.SiteID, &node.Mode, &node.LastSeenAt, &node.LastBootID, &collectInt); err != nil {
			return nil, err
		}
		node.CollectDomains = collectInt != 0
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *Store) GetNode(ctx context.Context, id string) (Node, error) {
	var node Node
	var collectInt int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, site_id, mode, last_seen_at, last_boot_id, collect_domains FROM nodes WHERE id = ? AND revoked = 0`,
		id,
	).Scan(&node.ID, &node.SiteID, &node.Mode, &node.LastSeenAt, &node.LastBootID, &collectInt)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, err
	}
	node.CollectDomains = collectInt != 0
	return node, err
}

func (s *Store) SetNodeCollectDomains(ctx context.Context, id string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `UPDATE nodes SET collect_domains = ? WHERE id = ? AND revoked = 0`, val, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		err = sql.ErrNoRows
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(
		ctx,
		`UPDATE direct_collectors SET collect_domains = ?, updated_at = ? WHERE node_id = ?`,
		val,
		now,
		id,
	); err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

func (s *Store) NodeDomainPreferences(ctx context.Context, nodeID string) (bool, []string, error) {
	var collectInt int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT collect_domains FROM nodes WHERE id = ? AND revoked = 0`,
		nodeID,
	).Scan(&collectInt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	watchDomains, err := s.WatchDomainsForNode(ctx, nodeID)
	if err != nil {
		return false, nil, err
	}
	return collectInt != 0, watchDomains, nil
}

func (s *Store) WatchDomainsForNode(ctx context.Context, nodeID string) ([]string, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT matchers_json FROM watch_items WHERE enabled = 1 ORDER BY id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	domainSet := map[string]struct{}{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var matchers accounting.WatchMatcherMap
		if err := json.Unmarshal([]byte(raw), &matchers); err != nil {
			continue
		}
		if nodeMatcher, hasNode := matchers[accounting.WatchDimNode]; hasNode {
			cm, err := accounting.CompileDimensionMatcher(accounting.WatchDimNode, nodeMatcher)
			if err != nil || !cm.Match(nodeID) {
				continue
			}
		}
		domMatcher, hasDomain := matchers[accounting.WatchDimDomain]
		if !hasDomain {
			continue
		}
		for _, v := range domMatcher.Values {
			switch domMatcher.Type {
			case accounting.MatchExact:
				norm := strings.ToLower(strings.TrimRight(strings.TrimSpace(v), "."))
				if norm == "" {
					continue
				}
				if _, ipErr := netip.ParseAddr(norm); ipErr == nil {
					continue
				}
				domainSet[norm] = struct{}{}
			case accounting.MatchKeyword:
				kw := strings.ToLower(strings.TrimSpace(v))
				if kw != "" {
					domainSet["kw:"+kw] = struct{}{}
				}
			case accounting.MatchRegex:
				if strings.TrimSpace(v) != "" {
					domainSet["re:"+v] = struct{}{}
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var watchDomains []string
	if len(domainSet) > 0 {
		watchDomains = make([]string, 0, len(domainSet))
		for d := range domainSet {
			watchDomains = append(watchDomains, d)
		}
		sort.Strings(watchDomains)
	}
	return watchDomains, nil
}

func (s *Store) UpsertProxyNodeCatalog(ctx context.Context, nodeID string, entries []ProxyNodeCatalogEntry, now time.Time) error {
	if len(entries) == 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ts := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO proxy_node_catalog (node_id, name, provider_name, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(node_id, name) DO UPDATE SET
			provider_name = CASE WHEN excluded.provider_name != '' THEN excluded.provider_name ELSE proxy_node_catalog.provider_name END,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		if _, err = stmt.ExecContext(ctx, nodeID, name, strings.TrimSpace(entry.ProviderName), ts); err != nil {
			return err
		}
	}
	err = tx.Commit()
	return err
}

func (s *Store) ListProxyNodeNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT name FROM proxy_node_catalog ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *Store) RevokeNode(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `UPDATE nodes SET revoked = 1 WHERE id = ?`, id); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(
		ctx,
		`UPDATE direct_collectors SET enabled = 0, updated_at = ? WHERE node_id = ?`,
		now,
		id,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(
		ctx,
		`UPDATE alerts SET resolved_at = ? WHERE fingerprint = ? AND resolved_at IS NULL`,
		now,
		"silence|"+id,
	); err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

func (s *Store) Authenticate(ctx context.Context, token string) (Node, error) {
	credentialID, ok := enroll.CredentialID(token)
	if !ok {
		return Node{}, ErrUnauthorized
	}

	var node Node
	var tokenHash []byte
	var revoked bool
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, site_id, token_hash, revoked FROM nodes WHERE credential_id = ?`,
		credentialID,
	).Scan(&node.ID, &node.SiteID, &tokenHash, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrUnauthorized
	}
	if err != nil {
		return Node{}, err
	}
	if revoked || !enroll.VerifyToken(token, tokenHash) {
		return Node{}, ErrUnauthorized
	}
	return node, nil
}

func (s *Store) InsertBatch(
	ctx context.Context,
	nodeID, bootID string,
	seq uint64,
	raw []byte,
) (inserted bool, err error) {
	if seq > math.MaxInt64 {
		return false, fmt.Errorf("sequence exceeds SQLite INTEGER: %d", seq)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var exists int
	err = tx.QueryRowContext(
		ctx,
		`SELECT EXISTS(
			SELECT 1 FROM ingest_batches
			WHERE node_id = ? AND boot_id = ? AND sequence = ?
		)`,
		nodeID,
		bootID,
		seq,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	if exists == 1 {
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}

	var maxSequence sql.NullInt64
	err = tx.QueryRowContext(
		ctx,
		`SELECT MAX(sequence) FROM ingest_batches WHERE node_id = ? AND boot_id = ?`,
		nodeID,
		bootID,
	).Scan(&maxSequence)
	if err != nil {
		return false, err
	}
	if maxSequence.Valid && seq < uint64(maxSequence.Int64) {
		return false, ErrSequenceRegression
	}

	sum := sha256.Sum256(raw)
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO ingest_batches
			(node_id, boot_id, sequence, received_at, payload_sha256)
		 VALUES (?, ?, ?, ?, ?)`,
		nodeID,
		bootID,
		seq,
		time.Now().UTC().Format(time.RFC3339Nano),
		hex.EncodeToString(sum[:]),
	)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) BatchCount(ctx context.Context) (int, error) {
	var ingestCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ingest_batches`).Scan(&ingestCount); err != nil {
		return 0, err
	}
	if ingestCount == 0 {
		var appliedCount int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger_applied`).Scan(&appliedCount)
		return appliedCount, err
	}
	var directOnly int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM ledger_applied la
		 WHERE NOT EXISTS (
			SELECT 1 FROM ingest_batches ib
			WHERE ib.node_id = la.node_id AND ib.boot_id = la.boot_id AND ib.sequence = la.sequence
		 )`,
	).Scan(&directOnly); err != nil {
		return 0, err
	}
	return ingestCount + directOnly, nil
}

func (s *Store) BatchPayloadHash(
	ctx context.Context,
	nodeID, bootID string,
	seq uint64,
) (string, error) {
	var hash string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT payload_sha256 FROM ingest_batches
		 WHERE node_id = ? AND boot_id = ? AND sequence = ?`,
		nodeID,
		bootID,
		seq,
	).Scan(&hash)
	return hash, err
}
