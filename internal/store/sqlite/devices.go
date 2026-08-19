package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/identity"
)

func (s *Store) SetMultiplier(ctx context.Context, name string, factor float64) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("outbound name is required")
	}
	if factor <= 0 {
		return errors.New("factor must be greater than zero")
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO outbound_multipliers (name, factor, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET factor = excluded.factor, updated_at = excluded.updated_at`,
		name,
		factor,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ListMultipliers(ctx context.Context) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, factor FROM outbound_multipliers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]float64{}
	for rows.Next() {
		var name string
		var factor float64
		if err := rows.Scan(&name, &factor); err != nil {
			return nil, err
		}
		out[name] = factor
	}
	return out, rows.Err()
}

func (s *Store) ListOutboundLedgers(ctx context.Context) ([]OutboundLedger, error) {
	multipliers, err := s.ListMultipliers(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT outbound,
		        SUM(CASE WHEN class = ? THEN rx + tx ELSE 0 END),
		        SUM(CASE WHEN class = ? THEN rx + tx ELSE 0 END),
		        SUM(CASE WHEN class = ? THEN rx + tx ELSE 0 END)
		 FROM ledger_totals
		 WHERE device_id = '' AND outbound != ''
		 GROUP BY outbound
		 ORDER BY outbound`,
		accounting.ClassProxyRaw,
		accounting.ClassProxyAdjusted,
		accounting.ClassProxyUnadjusted,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ledgers []OutboundLedger
	var rawTotal uint64
	for rows.Next() {
		var name string
		var raw, adjusted, unadjusted int64
		if err := rows.Scan(&name, &raw, &adjusted, &unadjusted); err != nil {
			return nil, err
		}
		if raw < 0 {
			raw = 0
		}
		if adjusted < 0 {
			adjusted = 0
		}
		if unadjusted < 0 {
			unadjusted = 0
		}
		factor, configured := multipliers[name]
		ledgers = append(ledgers, OutboundLedger{
			Name:       name,
			Raw:        uint64(raw),
			Factor:     factor,
			Adjusted:   uint64(adjusted),
			Unadjusted: uint64(unadjusted),
			Configured: configured,
		})
		rawTotal += uint64(raw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rawTotal > 0 {
		for i := range ledgers {
			ledgers[i].Share = float64(ledgers[i].Raw) / float64(rawTotal)
		}
	}
	if ledgers == nil {
		ledgers = []OutboundLedger{}
	}
	return ledgers, nil
}

func (s *Store) RenameDevice(ctx context.Context, id, name string) error {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		return errors.New("device id is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE devices SET display_name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetDevice(ctx context.Context, id string) (DeviceLedger, []string, error) {
	devices, err := s.ListDeviceLedgers(ctx)
	if err != nil {
		return DeviceLedger{}, nil, err
	}
	for _, device := range devices {
		if device.ID == id {
			merged, err := s.mergedFrom(ctx, id)
			return device, merged, err
		}
	}
	return DeviceLedger{}, nil, sql.ErrNoRows
}

func (s *Store) mergedFrom(ctx context.Context, winnerID string) ([]string, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT loser_device_id FROM identity_merges
		 WHERE winner_device_id = ? AND active = 1
		 ORDER BY loser_device_id`,
		winnerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, rows.Err()
}

func (s *Store) attachDeviceEvidence(ctx context.Context, devices []DeviceLedger) error {
	if len(devices) == 0 {
		return nil
	}
	index := map[string]int{}
	args := make([]any, 0, len(devices))
	placeholders := make([]string, 0, len(devices))
	for i, device := range devices {
		index[device.ID] = i
		args = append(args, device.ID)
		placeholders = append(placeholders, "?")
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT device_id, evidence_rank, evidence_value
		 FROM identity_evidence
		 WHERE device_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY observed_at DESC, id DESC`,
		args...,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var deviceID, value string
		var rank int
		if err := rows.Scan(&deviceID, &rank, &value); err != nil {
			return err
		}
		pos, ok := index[deviceID]
		if !ok {
			continue
		}
		ip, mac := parseEvidence(identity.EvidenceRank(rank), value)
		if devices[pos].IP == "" && ip != "" {
			devices[pos].IP = ip
		}
		if devices[pos].MAC == "" && mac != "" {
			devices[pos].MAC = mac
		}
	}
	return rows.Err()
}

func parseEvidence(rank identity.EvidenceRank, value string) (ip, mac string) {
	switch rank {
	case identity.RankStableMAC:
		return "", value
	case identity.RankDHCPNeigh:
		ip, mac, _ = strings.Cut(value, "|")
		return ip, mac
	case identity.RankScopedIP:
		parts := strings.Split(value, "|")
		if len(parts) == 0 {
			return "", ""
		}
		return parts[len(parts)-1], ""
	default:
		return "", ""
	}
}
