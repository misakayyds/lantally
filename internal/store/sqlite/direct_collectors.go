package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/misakayyds/lantally/internal/redact"
)

const (
	defaultDirectPollIntervalMS = 1000
	minDirectPollIntervalMS     = 200
	maxDirectPollIntervalMS     = 300000
)

type DirectCollector struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	SiteID         string `json:"site_id"`
	NodeID         string `json:"node_id"`
	URL            string `json:"url"`
	RawURL         string `json:"-"`
	HasSecret      bool   `json:"has_secret"`
	Secret         string `json:"secret,omitempty"`
	SecretPlain    string `json:"-"`
	Enabled        bool   `json:"enabled"`
	CollectDomains bool   `json:"collect_domains"`
	PollIntervalMS int    `json:"poll_interval_ms"`
	LastSeenAt     string `json:"last_seen_at"`
	LastStatus     string `json:"last_status,omitempty"`
	LastError      string `json:"last_error"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type DirectCollectorInput struct {
	Name           *string `json:"name"`
	SiteID         *string `json:"site_id"`
	NodeID         *string `json:"node_id"`
	URL            *string `json:"url"`
	Secret         *string `json:"secret"`
	ClearSecret    bool    `json:"clear_secret"`
	Enabled        *bool   `json:"enabled"`
	CollectDomains *bool   `json:"collect_domains"`
	PollIntervalMS *int    `json:"poll_interval_ms"`
}

type directCollectorRow struct {
	id             int64
	name           string
	siteID         string
	nodeID         string
	rawURL         string
	secretEnc      []byte
	enabled        int
	collectDomains int
	pollIntervalMS int
	lastSeenAt     string
	lastError      string
	createdAt      string
	updatedAt      string
}

func (r directCollectorRow) toPublic() DirectCollector {
	hasSecret := len(r.secretEnc) > 0
	secretMask := ""
	if hasSecret {
		secretMask = "********"
	}
	redactedErr := redact.String(r.lastError)
	status := ""
	if redactedErr != "" {
		status = "error"
	} else if strings.TrimSpace(r.lastSeenAt) != "" {
		status = "ok"
	}
	return DirectCollector{
		ID:             r.id,
		Name:           r.name,
		SiteID:         r.siteID,
		NodeID:         r.nodeID,
		URL:            redact.URL(r.rawURL),
		HasSecret:      hasSecret,
		Secret:         secretMask,
		Enabled:        r.enabled != 0,
		CollectDomains: r.collectDomains != 0,
		PollIntervalMS: r.pollIntervalMS,
		LastSeenAt:     r.lastSeenAt,
		LastStatus:     status,
		LastError:      redactedErr,
		CreatedAt:      r.createdAt,
		UpdatedAt:      r.updatedAt,
	}
}

// ValidateDirectCollectorURL checks that rawURL is a valid http/https/ws/wss URL with a host.
func ValidateDirectCollectorURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("%w: url is required", ErrInvalidInput)
	}
	if strings.Contains(strings.ToLower(trimmed), "[redacted]") {
		return "", fmt.Errorf("%w: url contains redacted placeholder; provide full url", ErrInvalidInput)
	}
	for _, r := range trimmed {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", fmt.Errorf("%w: url contains whitespace or control characters", ErrInvalidInput)
		}
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		var uErr *url.Error
		if errors.As(err, &uErr) {
			return "", fmt.Errorf("%w: invalid url: %v", ErrInvalidInput, uErr.Err)
		}
		return "", fmt.Errorf("%w: invalid url", ErrInvalidInput)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss":
	default:
		return "", fmt.Errorf("%w: unsupported url scheme %q", ErrInvalidInput, u.Scheme)
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return "", fmt.Errorf("%w: url host is required", ErrInvalidInput)
	}
	if portStr := u.Port(); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("%w: invalid url port %q", ErrInvalidInput, portStr)
		}
	}
	return trimmed, nil
}

func validateDirectName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if len(name) > 128 {
		return "", fmt.Errorf("%w: name must be <= 128 characters", ErrInvalidInput)
	}
	return name, nil
}

func validateDirectSiteID(raw string) (string, error) {
	siteID := strings.TrimSpace(raw)
	if siteID == "" {
		return "", fmt.Errorf("%w: site_id cannot be empty", ErrInvalidInput)
	}
	if len(siteID) > 128 {
		return "", fmt.Errorf("%w: site_id must be <= 128 characters", ErrInvalidInput)
	}
	return siteID, nil
}

func validateDirectNodeID(raw string) (string, error) {
	nodeID := strings.TrimSpace(raw)
	if nodeID == "" {
		return "", fmt.Errorf("%w: node_id cannot be empty", ErrInvalidInput)
	}
	if len(nodeID) > 128 || strings.ContainsAny(nodeID, " \t\r\n") {
		return "", fmt.Errorf("%w: invalid node_id", ErrInvalidInput)
	}
	return nodeID, nil
}

func validateDirectPollIntervalMS(val *int, fallback int) (int, error) {
	if val == nil {
		return fallback, nil
	}
	if *val == 0 {
		return defaultDirectPollIntervalMS, nil
	}
	if *val < minDirectPollIntervalMS || *val > maxDirectPollIntervalMS {
		return 0, fmt.Errorf(
			"%w: poll_interval_ms must be between %d and %d",
			ErrInvalidInput,
			minDirectPollIntervalMS,
			maxDirectPollIntervalMS,
		)
	}
	return *val, nil
}

func generateDirectNodeID(name string) (string, error) {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
		if b.Len() >= 24 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	suffix := hex.EncodeToString(rnd[:])
	if slug == "" {
		return "dc-" + suffix, nil
	}
	return "dc-" + slug + "-" + suffix[:4], nil
}

func scanDirectCollectorRow(scanner interface{ Scan(...any) error }) (directCollectorRow, error) {
	var r directCollectorRow
	err := scanner.Scan(
		&r.id,
		&r.name,
		&r.siteID,
		&r.nodeID,
		&r.rawURL,
		&r.secretEnc,
		&r.enabled,
		&r.collectDomains,
		&r.pollIntervalMS,
		&r.lastSeenAt,
		&r.lastError,
		&r.createdAt,
		&r.updatedAt,
	)
	if err == nil && r.secretEnc == nil {
		r.secretEnc = []byte{}
	}
	return r, err
}

func (s *Store) ListDirectCollectors(ctx context.Context) ([]DirectCollector, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, site_id, node_id, url, secret_enc, enabled, collect_domains,
		        poll_interval_ms, last_seen_at, last_error, created_at, updated_at
		 FROM direct_collectors
		 ORDER BY id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DirectCollector{}
	for rows.Next() {
		r, err := scanDirectCollectorRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r.toPublic())
	}
	return out, rows.Err()
}

func (s *Store) GetDirectCollector(ctx context.Context, id int64) (DirectCollector, error) {
	if id <= 0 {
		return DirectCollector{}, fmt.Errorf("%w: invalid direct collector id", ErrInvalidInput)
	}
	r, err := scanDirectCollectorRow(s.db.QueryRowContext(
		ctx,
		`SELECT id, name, site_id, node_id, url, secret_enc, enabled, collect_domains,
		        poll_interval_ms, last_seen_at, last_error, created_at, updated_at
		 FROM direct_collectors
		 WHERE id = ?`,
		id,
	))
	if err != nil {
		return DirectCollector{}, err
	}
	return r.toPublic(), nil
}

func (s *Store) ListEnabledDirectCollectorsRuntime(ctx context.Context) ([]DirectCollector, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, site_id, node_id, url, secret_enc, enabled, collect_domains,
		        poll_interval_ms, last_seen_at, last_error, created_at, updated_at
		 FROM direct_collectors
		 WHERE enabled = 1
		 ORDER BY id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []directCollectorRow
	for rows.Next() {
		r, err := scanDirectCollectorRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]DirectCollector, 0, len(items))
	for _, r := range items {
		plain, decErr := decryptSecret(s.secretKey, r.secretEnc)
		if decErr != nil {
			_ = s.UpdateDirectCollectorStatus(ctx, r.id, time.Time{}, "secret decryption failed: key mismatch; re-enter collector secret")
			continue
		}
		dc := r.toPublic()
		dc.RawURL = r.rawURL
		dc.SecretPlain = plain
		out = append(out, dc)
	}
	return out, nil
}

func (s *Store) GetDirectCollectorRuntime(ctx context.Context, id int64) (DirectCollector, error) {
	if id <= 0 {
		return DirectCollector{}, fmt.Errorf("%w: invalid direct collector id", ErrInvalidInput)
	}
	r, err := scanDirectCollectorRow(s.db.QueryRowContext(
		ctx,
		`SELECT id, name, site_id, node_id, url, secret_enc, enabled, collect_domains,
		        poll_interval_ms, last_seen_at, last_error, created_at, updated_at
		 FROM direct_collectors
		 WHERE id = ?`,
		id,
	))
	if err != nil {
		return DirectCollector{}, err
	}
	plain, decErr := decryptSecret(s.secretKey, r.secretEnc)
	if decErr != nil {
		return DirectCollector{}, fmt.Errorf("secret decryption failed: key mismatch; re-enter collector secret")
	}
	dc := r.toPublic()
	dc.RawURL = r.rawURL
	dc.SecretPlain = plain
	return dc, nil
}

func ensureDirectNodeOwnershipTx(ctx context.Context, tx *sql.Tx, nodeID string) error {
	var mode string
	var revoked int
	err := tx.QueryRowContext(ctx, `SELECT mode, revoked FROM nodes WHERE id = ?`, nodeID).Scan(&mode, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if mode == "agent" && revoked == 0 {
		return fmt.Errorf("%w: node_id %q is already enrolled as an active agent", ErrInvalidInput, nodeID)
	}
	return nil
}

func upsertDirectNodeTx(ctx context.Context, tx *sql.Tx, nodeID, siteID string, collectDomains bool) error {
	collectInt := 0
	if collectDomains {
		collectInt = 1
	}
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO nodes (id, site_id, credential_id, token_hash, revoked, collect_domains, mode)
		 VALUES (?, ?, ?, X'', 0, ?, 'direct')
		 ON CONFLICT(id) DO UPDATE SET
		   site_id = excluded.site_id,
		   credential_id = excluded.credential_id,
		   token_hash = excluded.token_hash,
		   revoked = 0,
		   collect_domains = excluded.collect_domains,
		   mode = 'direct'`,
		nodeID,
		siteID,
		"direct:"+nodeID,
		collectInt,
	)
	return err
}

func (s *Store) CreateDirectCollector(ctx context.Context, input DirectCollectorInput) (DirectCollector, error) {
	if input.Name == nil {
		return DirectCollector{}, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	name, err := validateDirectName(*input.Name)
	if err != nil {
		return DirectCollector{}, err
	}

	siteID := "default"
	if input.SiteID != nil {
		siteID, err = validateDirectSiteID(*input.SiteID)
		if err != nil {
			return DirectCollector{}, err
		}
	}

	var nodeID string
	if input.NodeID != nil {
		nodeID, err = validateDirectNodeID(*input.NodeID)
		if err != nil {
			return DirectCollector{}, err
		}
	} else {
		nodeID, err = generateDirectNodeID(name)
		if err != nil {
			return DirectCollector{}, err
		}
	}

	if input.URL == nil {
		return DirectCollector{}, fmt.Errorf("%w: url is required", ErrInvalidInput)
	}
	rawURL, err := ValidateDirectCollectorURL(*input.URL)
	if err != nil {
		return DirectCollector{}, err
	}

	pollMS, err := validateDirectPollIntervalMS(input.PollIntervalMS, defaultDirectPollIntervalMS)
	if err != nil {
		return DirectCollector{}, err
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	collectDomains := false
	if input.CollectDomains != nil {
		collectDomains = *input.CollectDomains
	}

	var secretPlain string
	if input.Secret != nil {
		secretPlain = *input.Secret
	}
	secretEnc, err := encryptSecret(s.secretKey, secretPlain)
	if err != nil {
		return DirectCollector{}, err
	}
	if secretEnc == nil {
		secretEnc = []byte{}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	collectInt := 0
	if collectDomains {
		collectInt = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DirectCollector{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = ensureDirectNodeOwnershipTx(ctx, tx, nodeID); err != nil {
		return DirectCollector{}, err
	}
	if err = upsertDirectNodeTx(ctx, tx, nodeID, siteID, collectDomains); err != nil {
		return DirectCollector{}, err
	}

	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO direct_collectors
			(name, site_id, node_id, url, secret_enc, enabled, collect_domains, poll_interval_ms, last_seen_at, last_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)`,
		name,
		siteID,
		nodeID,
		rawURL,
		secretEnc,
		enabledInt,
		collectInt,
		pollMS,
		now,
		now,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			err = fmt.Errorf("%w: direct collector for node_id %q already exists", ErrInvalidInput, nodeID)
		}
		return DirectCollector{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return DirectCollector{}, err
	}
	if err = tx.Commit(); err != nil {
		return DirectCollector{}, err
	}

	return directCollectorRow{
		id:             id,
		name:           name,
		siteID:         siteID,
		nodeID:         nodeID,
		rawURL:         rawURL,
		secretEnc:      secretEnc,
		enabled:        enabledInt,
		collectDomains: collectInt,
		pollIntervalMS: pollMS,
		createdAt:      now,
		updatedAt:      now,
	}.toPublic(), nil
}

func (s *Store) UpdateDirectCollector(ctx context.Context, id int64, input DirectCollectorInput) (DirectCollector, error) {
	if id <= 0 {
		return DirectCollector{}, fmt.Errorf("%w: invalid direct collector id", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DirectCollector{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	cur, err := scanDirectCollectorRow(tx.QueryRowContext(
		ctx,
		`SELECT id, name, site_id, node_id, url, secret_enc, enabled, collect_domains,
		        poll_interval_ms, last_seen_at, last_error, created_at, updated_at
		 FROM direct_collectors
		 WHERE id = ?`,
		id,
	))
	if err != nil {
		return DirectCollector{}, err
	}

	name := cur.name
	if input.Name != nil {
		name, err = validateDirectName(*input.Name)
		if err != nil {
			return DirectCollector{}, err
		}
	}

	siteID := cur.siteID
	if input.SiteID != nil {
		siteID, err = validateDirectSiteID(*input.SiteID)
		if err != nil {
			return DirectCollector{}, err
		}
	}

	nodeID := cur.nodeID
	if input.NodeID != nil {
		nodeID, err = validateDirectNodeID(*input.NodeID)
		if err != nil {
			return DirectCollector{}, err
		}
	}

	rawURL := cur.rawURL
	if input.URL != nil {
		trimmedURL := strings.TrimSpace(*input.URL)
		if trimmedURL != "" && trimmedURL == redact.URL(cur.rawURL) {
			rawURL = cur.rawURL
		} else {
			rawURL, err = ValidateDirectCollectorURL(*input.URL)
			if err != nil {
				return DirectCollector{}, err
			}
		}
	}

	pollMS, err := validateDirectPollIntervalMS(input.PollIntervalMS, cur.pollIntervalMS)
	if err != nil {
		return DirectCollector{}, err
	}

	enabled := cur.enabled != 0
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	collectDomains := cur.collectDomains != 0
	if input.CollectDomains != nil {
		collectDomains = *input.CollectDomains
	}

	secretEnc := cur.secretEnc
	switch {
	case input.ClearSecret:
		secretEnc = []byte{}
	case input.Secret == nil || *input.Secret == "********":
		// Preserve existing encrypted secret.
	case *input.Secret == "":
		secretEnc = []byte{}
	default:
		secretEnc, err = encryptSecret(s.secretKey, *input.Secret)
		if err != nil {
			return DirectCollector{}, err
		}
	}
	if secretEnc == nil {
		secretEnc = []byte{}
	}

	if err = ensureDirectNodeOwnershipTx(ctx, tx, nodeID); err != nil {
		return DirectCollector{}, err
	}
	if err = upsertDirectNodeTx(ctx, tx, nodeID, siteID, collectDomains); err != nil {
		return DirectCollector{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	collectInt := 0
	if collectDomains {
		collectInt = 1
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE direct_collectors
		 SET name = ?, site_id = ?, node_id = ?, url = ?, secret_enc = ?,
		     enabled = ?, collect_domains = ?, poll_interval_ms = ?, updated_at = ?
		 WHERE id = ?`,
		name,
		siteID,
		nodeID,
		rawURL,
		secretEnc,
		enabledInt,
		collectInt,
		pollMS,
		now,
		id,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			err = fmt.Errorf("%w: direct collector for node_id %q already exists", ErrInvalidInput, nodeID)
		}
		return DirectCollector{}, err
	}

	if nodeID != cur.nodeID {
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE nodes SET revoked = 1 WHERE id = ? AND mode = 'direct'`,
			cur.nodeID,
		); err != nil {
			return DirectCollector{}, err
		}
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE alerts SET resolved_at = ? WHERE fingerprint = ? AND resolved_at IS NULL`,
			now,
			"silence|"+cur.nodeID,
		); err != nil {
			return DirectCollector{}, err
		}
	}
	if !enabled {
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE alerts SET resolved_at = ? WHERE fingerprint = ? AND resolved_at IS NULL`,
			now,
			"silence|"+nodeID,
		); err != nil {
			return DirectCollector{}, err
		}
	}

	if err = tx.Commit(); err != nil {
		return DirectCollector{}, err
	}

	cur.name = name
	cur.siteID = siteID
	cur.nodeID = nodeID
	cur.rawURL = rawURL
	cur.secretEnc = secretEnc
	cur.enabled = enabledInt
	cur.collectDomains = collectInt
	cur.pollIntervalMS = pollMS
	cur.updatedAt = now
	return cur.toPublic(), nil
}

func (s *Store) ToggleDirectCollector(ctx context.Context, id int64, explicit *bool) (DirectCollector, error) {
	if id <= 0 {
		return DirectCollector{}, fmt.Errorf("%w: invalid direct collector id", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DirectCollector{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	cur, err := scanDirectCollectorRow(tx.QueryRowContext(
		ctx,
		`SELECT id, name, site_id, node_id, url, secret_enc, enabled, collect_domains,
		        poll_interval_ms, last_seen_at, last_error, created_at, updated_at
		 FROM direct_collectors
		 WHERE id = ?`,
		id,
	))
	if err != nil {
		return DirectCollector{}, err
	}

	next := cur.enabled == 0
	if explicit != nil {
		next = *explicit
	}
	nextInt := 0
	if next {
		nextInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if _, err = tx.ExecContext(
		ctx,
		`UPDATE direct_collectors SET enabled = ?, updated_at = ? WHERE id = ?`,
		nextInt,
		now,
		id,
	); err != nil {
		return DirectCollector{}, err
	}
	if next {
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE nodes SET revoked = 0 WHERE id = ? AND mode = 'direct'`,
			cur.nodeID,
		); err != nil {
			return DirectCollector{}, err
		}
	} else {
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE alerts SET resolved_at = ? WHERE fingerprint = ? AND resolved_at IS NULL`,
			now,
			"silence|"+cur.nodeID,
		); err != nil {
			return DirectCollector{}, err
		}
	}

	if err = tx.Commit(); err != nil {
		return DirectCollector{}, err
	}

	cur.enabled = nextInt
	cur.updatedAt = now
	return cur.toPublic(), nil
}

func (s *Store) DeleteDirectCollector(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: invalid direct collector id", ErrInvalidInput)
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

	var nodeID string
	err = tx.QueryRowContext(ctx, `SELECT node_id FROM direct_collectors WHERE id = ?`, id).Scan(&nodeID)
	if err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM direct_collectors WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(
		ctx,
		`UPDATE nodes SET revoked = 1 WHERE id = ? AND mode = 'direct'`,
		nodeID,
	); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(
		ctx,
		`UPDATE alerts SET resolved_at = ? WHERE fingerprint = ? AND resolved_at IS NULL`,
		now,
		"silence|"+nodeID,
	); err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

func (s *Store) UpdateDirectCollectorStatus(ctx context.Context, id int64, seenAt time.Time, lastErr string) error {
	if id <= 0 {
		return fmt.Errorf("%w: invalid direct collector id", ErrInvalidInput)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if !seenAt.IsZero() {
		seenStr := seenAt.UTC().Format(time.RFC3339Nano)
		if _, err := s.db.ExecContext(
			ctx,
			`UPDATE direct_collectors SET last_seen_at = ?, last_error = '', updated_at = ? WHERE id = ?`,
			seenStr,
			now,
			id,
		); err != nil {
			return err
		}
		_, err := s.db.ExecContext(
			ctx,
			`UPDATE nodes
			 SET last_seen_at = ?,
			     last_interval_ms = COALESCE((SELECT poll_interval_ms FROM direct_collectors WHERE id = ?), last_interval_ms)
			 WHERE id = (SELECT node_id FROM direct_collectors WHERE id = ?)`,
			seenStr,
			id,
			id,
		)
		return err
	}

	cleanErr := strings.TrimSpace(redact.String(lastErr))
	if len(cleanErr) > 512 {
		cleanErr = cleanErr[:512]
	}
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE direct_collectors SET last_error = ?, updated_at = ? WHERE id = ?`,
		cleanErr,
		now,
		id,
	)
	return err
}
