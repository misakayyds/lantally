package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/misakayyds/lantally/internal/enroll"
	_ "modernc.org/sqlite"
)

var (
	ErrSequenceRegression = errors.New("sequence regression")
	ErrUnauthorized       = errors.New("unauthorized")
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db *sql.DB
}

type Node struct {
	ID     string
	SiteID string
}

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.Ping(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	schema, err := migrations.ReadFile("migrations/0001_init.sql")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(string(schema)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
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

func (s *Store) RevokeNode(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET revoked = 1 WHERE id = ?`, id)
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
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ingest_batches`).Scan(&count)
	return count, err
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
