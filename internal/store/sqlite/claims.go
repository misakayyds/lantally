package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

const claimTTL = 10 * time.Minute

var (
	ErrClaimNotFound = errors.New("claim not found")
	ErrClaimUsed     = errors.New("claim already redeemed")
	ErrClaimExpired  = errors.New("claim expired")
)

type Claim struct {
	Code      string
	SiteID    string
	NodeID    string
	Local     bool
	ExpiresAt time.Time
}

func IssueClaimCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i, b := range raw {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func (s *Store) CreateClaim(ctx context.Context, siteID, nodeID string, local bool, now time.Time) (Claim, error) {
	code, err := IssueClaimCode()
	if err != nil {
		return Claim{}, err
	}
	expires := now.UTC().Add(claimTTL)
	localMode := 0
	if local {
		localMode = 1
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO claim_codes (code, site_id, node_id, local_mode, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		code,
		siteID,
		nodeID,
		localMode,
		expires.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Claim{}, err
	}
	return Claim{Code: code, SiteID: siteID, NodeID: nodeID, Local: local, ExpiresAt: expires}, nil
}

func (s *Store) RedeemClaim(ctx context.Context, code string, now time.Time) (Claim, error) {
	nowUTC := now.UTC()
	nowStr := nowUTC.Format(time.RFC3339Nano)
	var claim Claim
	var localMode int
	var expires string
	err := s.db.QueryRowContext(
		ctx,
		`UPDATE claim_codes
		 SET redeemed_at = ?
		 WHERE code = ? AND redeemed_at IS NULL AND expires_at > ?
		 RETURNING code, site_id, node_id, local_mode, expires_at`,
		nowStr,
		code,
		nowStr,
	).Scan(&claim.Code, &claim.SiteID, &claim.NodeID, &localMode, &expires)
	if err == nil {
		claim.Local = localMode == 1
		claim.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		return claim, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Claim{}, err
	}

	var redeemed sql.NullString
	err = s.db.QueryRowContext(
		ctx,
		`SELECT expires_at, redeemed_at FROM claim_codes WHERE code = ?`,
		code,
	).Scan(&expires, &redeemed)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, ErrClaimNotFound
	}
	if err != nil {
		return Claim{}, err
	}
	if redeemed.Valid {
		return Claim{}, ErrClaimUsed
	}
	expiresAt, _ := time.Parse(time.RFC3339Nano, expires)
	if !expiresAt.After(nowUTC) {
		return Claim{}, ErrClaimExpired
	}
	return Claim{}, ErrClaimUsed
}

func (s *Store) UpsertNodeToken(ctx context.Context, id, siteID, credentialID string, tokenHash []byte) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO nodes (id, site_id, credential_id, token_hash, revoked)
		 VALUES (?, ?, ?, ?, 0)
		 ON CONFLICT(id) DO UPDATE SET
		   site_id = excluded.site_id,
		   credential_id = excluded.credential_id,
		   token_hash = excluded.token_hash,
		   revoked = 0`,
		id,
		siteID,
		credentialID,
		tokenHash,
	)
	return err
}
