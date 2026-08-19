package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/alert"
)

type Alert struct {
	ID             int64      `json:"id"`
	Kind           string     `json:"kind"`
	SiteID         string     `json:"site_id"`
	NodeID         string     `json:"node_id"`
	DeviceID       string     `json:"device_id,omitempty"`
	Message        string     `json:"message"`
	ObservedAt     time.Time  `json:"observed_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

type Billing struct {
	ResetDay      int    `json:"reset_day"`
	ProviderBytes uint64 `json:"provider_bytes"`
}

type BillingStatus struct {
	ResetDay      int       `json:"reset_day"`
	ProviderBytes uint64    `json:"provider_bytes"`
	LocalBytes    uint64    `json:"local_bytes"`
	Delta         int64     `json:"delta"`
	Ratio         float64   `json:"ratio"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
}

func (s *Store) TouchNode(ctx context.Context, nodeID, bootID string, interval time.Duration, seenAt time.Time) error {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE nodes SET last_seen_at = ?, last_interval_ms = ?, last_boot_id = ? WHERE id = ?`,
		seenAt.UTC().Format(time.RFC3339Nano),
		interval.Milliseconds(),
		bootID,
		nodeID,
	)
	return err
}

func (s *Store) LastBootID(ctx context.Context, nodeID string) (string, error) {
	var boot string
	err := s.db.QueryRowContext(ctx, `SELECT last_boot_id FROM nodes WHERE id = ?`, nodeID).Scan(&boot)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return boot, err
}

func (s *Store) RaiseAlert(ctx context.Context, rec alert.Record) error {
	if rec.ObservedAt.IsZero() {
		rec.ObservedAt = time.Now().UTC()
	}
	fingerprint := alertFingerprint(rec)
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO alerts (fingerprint, kind, site_id, node_id, device_id, message, observed_at)
		 SELECT ?, ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (
			SELECT 1 FROM alerts WHERE fingerprint = ? AND resolved_at IS NULL
		 )`,
		fingerprint,
		string(rec.Kind),
		rec.SiteID,
		rec.NodeID,
		rec.DeviceID,
		rec.Message,
		rec.ObservedAt.UTC().Format(time.RFC3339Nano),
		fingerprint,
	)
	return err
}

func (s *Store) ResolveFingerprint(ctx context.Context, fingerprint string, now time.Time) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE alerts SET resolved_at = ? WHERE fingerprint = ? AND resolved_at IS NULL`,
		now.UTC().Format(time.RFC3339Nano),
		fingerprint,
	)
	return err
}

func (s *Store) AckAlert(ctx context.Context, id int64, now time.Time) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE alerts SET acknowledged_at = ? WHERE id = ? AND acknowledged_at IS NULL AND resolved_at IS NULL`,
		now.UTC().Format(time.RFC3339Nano),
		id,
	)
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

func (s *Store) ListOpenAlerts(ctx context.Context) ([]Alert, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, kind, site_id, node_id, device_id, message, observed_at, acknowledged_at
		 FROM alerts
		 WHERE acknowledged_at IS NULL AND resolved_at IS NULL
		 ORDER BY observed_at DESC, id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var item Alert
		var observed string
		var acked sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.Kind,
			&item.SiteID,
			&item.NodeID,
			&item.DeviceID,
			&item.Message,
			&observed,
			&acked,
		); err != nil {
			return nil, err
		}
		item.ObservedAt, _ = time.Parse(time.RFC3339Nano, observed)
		if acked.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, acked.String)
			if err == nil {
				item.AcknowledgedAt = &parsed
			}
		}
		out = append(out, item)
	}
	if out == nil {
		out = []Alert{}
	}
	return out, rows.Err()
}

func (s *Store) SetBilling(ctx context.Context, billing Billing) error {
	if billing.ResetDay < 1 || billing.ResetDay > 28 {
		return errors.New("reset_day must be 1-28")
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO billing_checkpoints (id, reset_day, provider_bytes, updated_at)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   reset_day = excluded.reset_day,
		   provider_bytes = excluded.provider_bytes,
		   updated_at = excluded.updated_at`,
		billing.ResetDay,
		int64(billing.ProviderBytes),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) BillingStatus(ctx context.Context, now time.Time) (BillingStatus, error) {
	var status BillingStatus
	var resetDay int
	var provider int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT reset_day, provider_bytes FROM billing_checkpoints WHERE id = 1`,
	).Scan(&resetDay, &provider)
	if errors.Is(err, sql.ErrNoRows) {
		from, to := billingPeriod(now, 1)
		status.ResetDay = 1
		status.From = from
		status.To = to
		return status, nil
	}
	if err != nil {
		return BillingStatus{}, err
	}
	from, to := billingPeriod(now, resetDay)
	local, err := s.periodClassBytes(ctx, accounting.ClassProxyAdjusted, from, to)
	if err != nil {
		return BillingStatus{}, err
	}
	status = BillingStatus{
		ResetDay:      resetDay,
		ProviderBytes: uint64(max64(provider, 0)),
		LocalBytes:    local,
		From:          from,
		To:            to,
	}
	status.Delta = int64(status.LocalBytes) - int64(status.ProviderBytes)
	if status.ProviderBytes > 0 {
		diff := status.LocalBytes
		if status.LocalBytes < status.ProviderBytes {
			diff = status.ProviderBytes - status.LocalBytes
		} else {
			diff = status.LocalBytes - status.ProviderBytes
		}
		status.Ratio = float64(diff) / float64(status.ProviderBytes)
	}
	return status, nil
}

func (s *Store) EvaluateAlerts(ctx context.Context, now time.Time) error {
	now = now.UTC()
	if err := s.evaluateSilence(ctx, now); err != nil {
		return err
	}
	if err := s.evaluateGrowth(ctx, now); err != nil {
		return err
	}
	return s.evaluateDrift(ctx, now)
}

func (s *Store) evaluateSilence(ctx context.Context, now time.Time) error {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, site_id, last_seen_at, last_interval_ms FROM nodes WHERE revoked = 0`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	type nodeRow struct {
		id, site, seen string
		intervalMS     int64
	}
	var nodes []nodeRow
	for rows.Next() {
		var row nodeRow
		if err := rows.Scan(&row.id, &row.site, &row.seen, &row.intervalMS); err != nil {
			return err
		}
		nodes = append(nodes, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, node := range nodes {
		fingerprint := "silence|" + node.id
		if strings.TrimSpace(node.seen) == "" {
			continue
		}
		seenAt, err := time.Parse(time.RFC3339Nano, node.seen)
		if err != nil {
			seenAt, err = time.Parse(time.RFC3339, node.seen)
			if err != nil {
				continue
			}
		}
		interval := time.Duration(node.intervalMS) * time.Millisecond
		rec, fired := alert.EvalSilence(seenAt, now, interval)
		if !fired {
			if err := s.ResolveFingerprint(ctx, fingerprint, now); err != nil {
				return err
			}
			continue
		}
		rec.SiteID = node.site
		rec.NodeID = node.id
		rec.ObservedAt = now
		rec.Fingerprint = fingerprint
		if err := s.RaiseAlert(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) evaluateGrowth(ctx context.Context, now time.Time) error {
	from := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -7)
	to := now.UTC()
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT node_id, day, SUM(rx + tx)
		 FROM ledger_daily
		 WHERE device_id = '' AND class = ? AND day >= ? AND day <= ?
		 GROUP BY node_id, day`,
		accounting.ClassTotal,
		from.Unix(),
		to.Unix(),
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	byNode := map[string]map[int64]uint64{}
	for rows.Next() {
		var nodeID string
		var day, bytes int64
		if err := rows.Scan(&nodeID, &day, &bytes); err != nil {
			return err
		}
		if byNode[nodeID] == nil {
			byNode[nodeID] = map[int64]uint64{}
		}
		if bytes < 0 {
			bytes = 0
		}
		byNode[nodeID][day] += uint64(bytes)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	today := now.UTC().Truncate(24 * time.Hour).Unix()
	for nodeID, days := range byNode {
		var prior []float64
		for day, bytes := range days {
			if day == today {
				continue
			}
			prior = append(prior, float64(bytes))
		}
		if len(prior) == 0 {
			continue
		}
		rec, fired := alert.EvalGrowth(float64(days[today]), median(prior), alert.DefaultGrowthConfig())
		fingerprint := "growth|" + nodeID + "|" + strconv.FormatInt(today, 10)
		if !fired {
			continue
		}
		rec.NodeID = nodeID
		rec.ObservedAt = now
		rec.Fingerprint = fingerprint
		if err := s.RaiseAlert(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) evaluateDrift(ctx context.Context, now time.Time) error {
	status, err := s.BillingStatus(ctx, now)
	if err != nil {
		return err
	}
	fingerprint := "drift|" + strconv.FormatInt(status.From.Unix(), 10)
	if status.ProviderBytes == 0 {
		return s.ResolveFingerprint(ctx, fingerprint, now)
	}
	rec, fired := alert.EvalDrift(float64(status.LocalBytes), float64(status.ProviderBytes), alert.DefaultDriftConfig())
	if !fired {
		return s.ResolveFingerprint(ctx, fingerprint, now)
	}
	rec.ObservedAt = now
	rec.Fingerprint = fingerprint
	return s.RaiseAlert(ctx, rec)
}

func (s *Store) periodClassBytes(ctx context.Context, class string, from, to time.Time) (uint64, error) {
	var sum sql.NullInt64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT SUM(rx + tx) FROM ledger_daily
		 WHERE device_id = '' AND class = ? AND day >= ? AND day <= ?`,
		class,
		from.UTC().Unix(),
		to.UTC().Unix(),
	).Scan(&sum)
	if err != nil {
		return 0, err
	}
	if !sum.Valid || sum.Int64 < 0 {
		return 0, nil
	}
	return uint64(sum.Int64), nil
}

func billingPeriod(now time.Time, resetDay int) (time.Time, time.Time) {
	now = now.UTC()
	if resetDay < 1 || resetDay > 28 {
		resetDay = 1
	}
	year, month, day := now.Date()
	from := time.Date(year, month, resetDay, 0, 0, 0, 0, time.UTC)
	if day < resetDay {
		from = from.AddDate(0, -1, 0)
	}
	return from, now
}

func alertFingerprint(rec alert.Record) string {
	if rec.Fingerprint != "" {
		return rec.Fingerprint
	}
	switch rec.Kind {
	case alert.KindSilence:
		return "silence|" + rec.NodeID
	case alert.KindGrowth:
		day := rec.ObservedAt.UTC().Truncate(24 * time.Hour).Unix()
		return "growth|" + rec.NodeID + "|" + strconv.FormatInt(day, 10)
	case alert.KindDrift:
		return "drift|" + rec.ObservedAt.UTC().Truncate(24*time.Hour).Format("2006-01")
	case alert.KindReset:
		return "reset|" + rec.NodeID + "|" + rec.Message
	default:
		return string(rec.Kind) + "|" + rec.NodeID
	}
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func max64(v int64, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}
