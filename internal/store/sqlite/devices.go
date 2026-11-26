package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"
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
	if math.IsNaN(factor) || math.IsInf(factor, 0) || factor <= 0 {
		return errors.New("factor must be greater than zero")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO multiplier_rules (name, match_type, pattern, factor, priority, enabled, updated_at)
		 VALUES (?, 'exact', ?, ?, 100, 1, ?)
		 ON CONFLICT(match_type, pattern) WHERE match_type = 'exact'
		 DO UPDATE SET name = excluded.name, factor = excluded.factor, priority = 100, enabled = 1, updated_at = excluded.updated_at`,
		name,
		name,
		factor,
		now,
	)
	return err
}

func (s *Store) ListMultipliers(ctx context.Context) (map[string]float64, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT pattern, factor
		 FROM multiplier_rules
		 WHERE enabled = 1 AND match_type = 'exact'
		 ORDER BY pattern`,
	)
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
	return s.ListOutboundLedgersInRange(ctx, time.Time{}, time.Time{}, "")
}

func (s *Store) ListOutboundLedgersInRange(
	ctx context.Context,
	from, to time.Time,
	nodeID string,
) ([]OutboundLedger, error) {
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return nil, err
	}

	var args []any
	var where []string
	where = append(where, "device_id = ''", "outbound != ''")
	if strings.TrimSpace(nodeID) != "" {
		where = append(where, "node_id = ?")
		args = append(args, strings.TrimSpace(nodeID))
	}
	baseWhere := strings.Join(where, " AND ")

	fromSQL := `ledger_totals`
	whereSQL := baseWhere
	var rangeArgs []any
	if !from.IsZero() && !to.IsZero() {
		if to.Before(from) {
			return []OutboundLedger{}, nil
		}
		fromUnix := from.UTC().Unix()
		toUnix := to.UTC().Unix()
		fromDay := (fromUnix / daySeconds) * daySeconds
		if to.Sub(from) > range7d {
			fromSQL = `ledger_daily`
			whereSQL = baseWhere + ` AND day >= ? AND day <= ?`
			rangeArgs = append(rangeArgs, args...)
			rangeArgs = append(rangeArgs, fromDay, toUnix)
		} else {
			fromSQL = `(
				SELECT outbound, class, rx, tx
				FROM ledger_samples
				WHERE ` + baseWhere + ` AND sampled_at >= ? AND sampled_at <= ?
				UNION ALL
				SELECT outbound, class, rx, tx
				FROM ledger_daily d
				WHERE ` + baseWhere + ` AND d.day >= ? AND d.day <= ?
				  AND NOT EXISTS (
					SELECT 1 FROM ledger_samples s
					WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
				  )
			)`
			whereSQL = `1=1`
			rangeArgs = append(rangeArgs, args...)
			rangeArgs = append(rangeArgs, fromUnix, toUnix)
			rangeArgs = append(rangeArgs, args...)
			rangeArgs = append(rangeArgs, fromDay, toUnix, daySeconds)
		}
	} else {
		rangeArgs = append(rangeArgs, args...)
	}

	queryArgs := append([]any{
		accounting.ClassProxyRaw, accounting.ClassProxyRaw,
		accounting.ClassProxyAdjusted, accounting.ClassProxyAdjusted,
		accounting.ClassProxyUnadjusted, accounting.ClassProxyUnadjusted,
	}, rangeArgs...)

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT outbound,
		        SUM(CASE WHEN class = ? THEN rx ELSE 0 END),
		        SUM(CASE WHEN class = ? THEN tx ELSE 0 END),
		        SUM(CASE WHEN class = ? THEN rx ELSE 0 END),
		        SUM(CASE WHEN class = ? THEN tx ELSE 0 END),
		        SUM(CASE WHEN class = ? THEN rx ELSE 0 END),
		        SUM(CASE WHEN class = ? THEN tx ELSE 0 END)
		 FROM `+fromSQL+`
		 WHERE `+whereSQL+`
		 GROUP BY outbound
		 ORDER BY outbound`,
		queryArgs...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ledgers []OutboundLedger
	var rawTotal uint64
	for rows.Next() {
		var name string
		var rawRx, rawTx, legAdjRx, legAdjTx, legUnadjRx, legUnadjTx int64
		if err := rows.Scan(&name, &rawRx, &rawTx, &legAdjRx, &legAdjTx, &legUnadjRx, &legUnadjTx); err != nil {
			return nil, err
		}
		uRawRx := clampUint64(rawRx)
		uRawTx := clampUint64(rawTx)
		uRaw := uRawRx + uRawTx

		m := rules.Match(name)
		adjRx := accounting.ScaleBytes(uRawRx, m.Factor)
		adjTx := accounting.ScaleBytes(uRawTx, m.Factor)
		var unadj uint64
		if m.MatchSource == accounting.SourceDefault {
			unadj = uRaw
		}
		if uRaw == 0 {
			uLegAdjRx := clampUint64(legAdjRx)
			uLegAdjTx := clampUint64(legAdjTx)
			uLegUnadj := clampUint64(legUnadjRx) + clampUint64(legUnadjTx)
			if uLegAdjRx+uLegAdjTx == 0 && uLegUnadj == 0 {
				continue
			}
			adjRx = uLegAdjRx
			adjTx = uLegAdjTx
			unadj = uLegUnadj
		}

		ledgers = append(ledgers, OutboundLedger{
			Name:            name,
			Rx:              uRawRx,
			Tx:              uRawTx,
			Total:           uRaw,
			Raw:             uRaw,
			RawRx:           uRawRx,
			RawTx:           uRawTx,
			Factor:          m.Factor,
			Adjusted:        adjRx + adjTx,
			AdjustedRx:      adjRx,
			AdjustedTx:      adjTx,
			Unadjusted:      unadj,
			Configured:      m.MatchSource == accounting.SourceRule,
			MatchSource:     string(m.MatchSource),
			MatchedRule:     m.MatchedRuleName,
			MatchedRuleID:   m.MatchedRuleID,
			MatchedRuleName: m.MatchedRuleName,
			MatchedType:     string(m.MatchedType),
		})
		rawTotal += uRaw
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

func clampUint64(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
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
	return s.GetDeviceInRange(ctx, id, time.Time{}, time.Time{})
}

func (s *Store) GetDeviceInRange(ctx context.Context, id string, from, to time.Time) (DeviceLedger, []string, error) {
	devices, err := s.ListDeviceLedgersInRange(ctx, from, to)
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
	index := make(map[string]int, len(devices))
	for i, device := range devices {
		index[device.ID] = i
	}
	const chunkSize = 500
	for start := 0; start < len(devices); start += chunkSize {
		end := start + chunkSize
		if end > len(devices) {
			end = len(devices)
		}
		chunk := devices[start:end]
		args := make([]any, 0, len(chunk))
		placeholders := make([]string, 0, len(chunk))
		for _, device := range chunk {
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
		for rows.Next() {
			var deviceID, value string
			var rank int
			if err := rows.Scan(&deviceID, &rank, &value); err != nil {
				_ = rows.Close()
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
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
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
