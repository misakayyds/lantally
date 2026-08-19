package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/misakayyds/lantally/internal/identity"
)

func (s *Store) ResolveIdentityAtomic(
	siteID, nodeID string,
	evidence []identity.Evidence,
	limitation string,
	now time.Time,
) (deviceID string, resolvedLimitation string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	resolvedLimitation = limitation
	for _, candidateEvidence := range evidence {
		candidates, findErr := findIdentitiesTx(
			tx,
			siteID,
			int(candidateEvidence.Rank),
			candidateEvidence.Value,
		)
		if findErr != nil {
			return "", "", findErr
		}
		if len(candidates) == 1 {
			deviceID, err = canonicalIdentityTx(tx, candidates[0])
			if err != nil {
				return "", "", err
			}
			break
		}
		if len(candidates) > 1 {
			return "", "conflicting identity evidence; identities were not auto-merged", identity.ErrConflictingEvidence
		}
	}

	if deviceID == "" {
		deviceID, err = newDeviceID()
		if err != nil {
			return "", "", err
		}
		if err = createIdentityTx(tx, siteID, deviceID, now); err != nil {
			return "", "", err
		}
	}
	for _, item := range evidence {
		if err = addIdentityEvidenceTx(
			tx,
			siteID,
			deviceID,
			int(item.Rank),
			item.Value,
			nodeID,
			now,
		); err != nil {
			return "", "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", "", err
	}
	return deviceID, resolvedLimitation, nil
}

func createIdentityTx(tx *sql.Tx, siteID, deviceID string, now time.Time) error {
	_, err := tx.Exec(
		`INSERT INTO devices (id, site_id, created_at) VALUES (?, ?, ?)`,
		deviceID,
		siteID,
		now.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func findIdentitiesTx(tx *sql.Tx, siteID string, rank int, value string) ([]string, error) {
	rows, err := tx.Query(
		`SELECT DISTINCT device_id
		 FROM identity_evidence
		 WHERE site_id = ? AND evidence_rank = ? AND evidence_value = ?
		 ORDER BY device_id`,
		siteID,
		rank,
		value,
	)
	if err != nil {
		return nil, err
	}

	var rawIdentities []string
	for rows.Next() {
		var deviceID string
		if err := rows.Scan(&deviceID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		rawIdentities = append(rawIdentities, deviceID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var identities []string
	for _, deviceID := range rawIdentities {
		canonical, err := canonicalIdentityTx(tx, deviceID)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		identities = append(identities, canonical)
	}
	return identities, nil
}

func addIdentityEvidenceTx(
	tx *sql.Tx,
	siteID, deviceID string,
	rank int,
	value, nodeID string,
	observedAt time.Time,
) error {
	_, err := tx.Exec(
		`INSERT OR IGNORE INTO identity_evidence
			(site_id, device_id, evidence_rank, evidence_value, node_id, observed_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		siteID,
		deviceID,
		rank,
		value,
		nodeID,
		observedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) AttachIdentityEvidence(
	siteID, deviceID string,
	rank int,
	value, nodeID string,
	observedAt time.Time,
) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO identity_evidence
			(site_id, device_id, evidence_rank, evidence_value, node_id, observed_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		siteID,
		deviceID,
		rank,
		value,
		nodeID,
		observedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) IdentityEvidenceCount(
	siteID string,
	rank int,
	value string,
) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(DISTINCT device_id)
		 FROM identity_evidence
		 WHERE site_id = ? AND evidence_rank = ? AND evidence_value = ?`,
		siteID,
		rank,
		value,
	).Scan(&count)
	return count, err
}

func (s *Store) CanonicalIdentity(deviceID string) (string, error) {
	return canonicalIdentityTx(s.db, deviceID)
}

func canonicalIdentityTx(db queryRowContext, deviceID string) (string, error) {
	seen := make(map[string]struct{})
	current := deviceID
	for {
		if _, exists := seen[current]; exists {
			return "", errors.New("identity merge cycle")
		}
		seen[current] = struct{}{}

		var canonical sql.NullString
		err := db.QueryRow(
			`SELECT canonical_id FROM devices WHERE id = ?`,
			current,
		).Scan(&canonical)
		if err != nil {
			return "", err
		}
		if !canonical.Valid || canonical.String == "" {
			return current, nil
		}
		current = canonical.String
	}
}

type queryRowContext interface {
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Store) IdentityPins(deviceID string) ([]string, error) {
	rows, err := s.db.Query(
		`WITH RECURSIVE identity_group(id) AS (
			SELECT id FROM devices WHERE id = ?
			UNION ALL
			SELECT devices.id
			FROM devices
			JOIN identity_group ON devices.canonical_id = identity_group.id
		)
		SELECT pin_id
		FROM device_pins
		WHERE device_id IN identity_group
		ORDER BY pin_id`,
		deviceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pins []string
	for rows.Next() {
		var pin string
		if err := rows.Scan(&pin); err != nil {
			return nil, err
		}
		pins = append(pins, pin)
	}
	return pins, rows.Err()
}

func (s *Store) PinIdentity(
	siteID, deviceID, pin string,
	now time.Time,
) (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var storedSite string
	if err = tx.QueryRow(
		`SELECT site_id FROM devices WHERE id = ?`,
		deviceID,
	).Scan(&storedSite); err != nil {
		return err
	}
	if storedSite != siteID {
		return errors.New("identity site mismatch")
	}

	var existingDevice string
	err = tx.QueryRow(
		`SELECT device_id FROM device_pins WHERE site_id = ? AND pin_id = ?`,
		siteID,
		pin,
	).Scan(&existingDevice)
	if err == nil && existingDevice != deviceID {
		return errors.New("pin already belongs to another identity")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec(
			`INSERT INTO device_pins (site_id, pin_id, device_id, created_at)
			 VALUES (?, ?, ?, ?)`,
			siteID,
			pin,
			deviceID,
			now.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			`INSERT OR IGNORE INTO identity_evidence
				(site_id, device_id, evidence_rank, evidence_value, node_id, observed_at)
			 VALUES (?, ?, 0, ?, '', ?)`,
			siteID,
			deviceID,
			pin,
			now.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MergeIdentities(
	a, b string,
	rank int,
	value string,
	now time.Time,
) (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var siteA, siteB string
	if err = tx.QueryRow(`SELECT site_id FROM devices WHERE id = ?`, a).Scan(&siteA); err != nil {
		return err
	}
	if err = tx.QueryRow(`SELECT site_id FROM devices WHERE id = ?`, b).Scan(&siteB); err != nil {
		return err
	}
	if siteA != siteB {
		return errors.New("cannot merge identities from different sites")
	}

	var active int
	err = tx.QueryRow(
		`SELECT EXISTS(
			SELECT 1 FROM identity_merges
			WHERE winner_device_id = ? AND loser_device_id = ? AND active = 1
		)`,
		a,
		b,
	).Scan(&active)
	if err != nil {
		return err
	}
	if active == 1 {
		return tx.Commit()
	}

	result, err := tx.Exec(
		`UPDATE devices SET canonical_id = ? WHERE id = ? AND canonical_id IS NULL`,
		a,
		b,
	)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("identity %q is already merged", b)
	}
	_, err = tx.Exec(
		`INSERT INTO identity_merges
			(winner_device_id, loser_device_id, evidence_rank, evidence_value, merged_at)
		 VALUES (?, ?, ?, ?, ?)`,
		a,
		b,
		rank,
		value,
		now.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnmergeIdentity(deviceID string) (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var mergeID int64
	var loser string
	err = tx.QueryRow(
		`SELECT id, loser_device_id
		 FROM identity_merges
		 WHERE active = 1
		   AND (winner_device_id = ? OR loser_device_id = ?)
		 ORDER BY id DESC
		 LIMIT 1`,
		deviceID,
		deviceID,
	).Scan(&mergeID, &loser)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(
		`UPDATE devices SET canonical_id = NULL WHERE id = ?`,
		loser,
	); err != nil {
		return err
	}
	var winner string
	if err = tx.QueryRow(
		`SELECT winner_device_id FROM identity_merges WHERE id = ?`,
		mergeID,
	).Scan(&winner); err != nil {
		return err
	}
	if _, err = tx.Exec(
		`DELETE FROM identity_evidence AS winner_evidence
		 WHERE winner_evidence.device_id = ?
		   AND EXISTS (
			SELECT 1
			FROM identity_evidence AS loser_evidence
			WHERE loser_evidence.device_id = ?
			  AND loser_evidence.evidence_rank = winner_evidence.evidence_rank
			  AND loser_evidence.evidence_value = winner_evidence.evidence_value
		   )`,
		winner,
		loser,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		`UPDATE identity_merges SET active = 0 WHERE id = ?`,
		mergeID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func newDeviceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate device ID: %w", err)
	}
	return "dev_" + hex.EncodeToString(raw[:]), nil
}
