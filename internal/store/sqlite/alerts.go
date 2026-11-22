package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
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
		return fmt.Errorf("%w: reset_day must be 1-28", ErrInvalidInput)
	}
	if billing.ProviderBytes > math.MaxInt64 {
		return fmt.Errorf("%w: provider_bytes exceeds int64 range", ErrInvalidInput)
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

func (s *Store) BillingResetDay(ctx context.Context) (int, error) {
	var resetDay int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT reset_day FROM billing_checkpoints WHERE id = 1`,
	).Scan(&resetDay)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 1, err
	}
	if resetDay < 1 || resetDay > 28 {
		return 1, nil
	}
	return resetDay, nil
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
	local, err := s.periodAdjustedProxyBytes(ctx, from, to)
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
	if err := s.evaluateDrift(ctx, now); err != nil {
		return err
	}
	if err := s.evaluateProviderDrift(ctx, now); err != nil {
		return err
	}
	return s.evaluateWatchQuotas(ctx, now)
}

func (s *Store) evaluateSilence(ctx context.Context, now time.Time) error {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT n.id, n.site_id, n.last_seen_at, n.last_interval_ms
		 FROM nodes n
		 LEFT JOIN direct_collectors dc ON dc.node_id = n.id
		 WHERE n.revoked = 0
		   AND (n.mode != 'direct' OR COALESCE(dc.enabled, 0) = 1)`,
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

func (s *Store) evaluateProviderDrift(ctx context.Context, now time.Time) error {
	_, _, groups, err := s.listProviderStatusesWithGroups(ctx, now, time.UTC)
	if err != nil {
		return err
	}
	firing := map[string]struct{}{}
	for _, grp := range groups {
		if grp.usedBytes == 0 || len(grp.members) == 0 {
			continue
		}
		rec, fired := alert.EvalDrift(float64(grp.localAdjusted), float64(grp.usedBytes), alert.DefaultDriftConfig())
		if !fired {
			continue
		}
		fp := providerDriftFingerprint(grp)
		rec.ObservedAt = now
		rec.Fingerprint = fp
		rec.Message = providerDriftMessage(grp)
		if err := s.RaiseAlert(ctx, rec); err != nil {
			return err
		}
		firing[fp] = struct{}{}
	}
	return s.resolveStaleFingerprints(ctx, "drift|provider|%", firing, now)
}

func providerDriftFingerprint(grp providerGroupStatus) string {
	sKey := strings.TrimSpace(grp.sharedGroup)
	if sKey == "" && len(grp.members) > 0 {
		sKey = fmt.Sprintf("id:%d", grp.members[0].ID)
	}
	if len(grp.members) == 1 && strings.HasPrefix(sKey, "id:") {
		return fmt.Sprintf("drift|provider|%d|%d", grp.members[0].ID, grp.cycleFrom.Unix())
	}
	return fmt.Sprintf("drift|provider|group|%s|%d", sKey, grp.cycleFrom.Unix())
}

func providerDriftMessage(grp providerGroupStatus) string {
	if len(grp.members) == 1 {
		return fmt.Sprintf("provider %q drift exceeds configured target", grp.members[0].Name)
	}
	names := make([]string, 0, len(grp.members))
	for _, m := range grp.members {
		names = append(names, fmt.Sprintf("%q", m.Name))
	}
	return fmt.Sprintf("shared provider group (%s) drift exceeds configured target", strings.Join(names, ", "))
}

func (s *Store) evaluateWatchQuotas(ctx context.Context, now time.Time) error {
	items, err := s.ListWatchItems(ctx)
	if err != nil {
		return err
	}
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return err
	}
	firing := map[string]struct{}{}
	for _, item := range items {
		cycleFrom, cycleTo, _, _, cycleKey := watchCycleWindow(item, now, time.UTC)
		fp := fmt.Sprintf("quota|watch|%d|%s", item.ID, cycleKey)
		if !item.Enabled || item.QuotaBytes == 0 {
			if err := s.ResolveFingerprint(ctx, fp, now); err != nil {
				return err
			}
			continue
		}
		st, err := s.evaluateWatchItemWithRules(ctx, item, cycleFrom, cycleTo, 0, time.UTC, now, rules)
		if err != nil {
			return err
		}
		rec, fired := alert.EvalQuota(float64(st.UsedBytes), float64(item.QuotaBytes), item.Name)
		if !fired {
			if err := s.ResolveFingerprint(ctx, fp, now); err != nil {
				return err
			}
			continue
		}
		rec.ObservedAt = now
		rec.Fingerprint = fp
		if err := s.RaiseAlert(ctx, rec); err != nil {
			return err
		}
		firing[fp] = struct{}{}
	}
	return s.resolveStaleFingerprints(ctx, "quota|watch|%", firing, now)
}

func (s *Store) resolveStaleFingerprints(
	ctx context.Context,
	likePattern string,
	keep map[string]struct{},
	now time.Time,
) error {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT fingerprint FROM alerts WHERE fingerprint LIKE ? AND resolved_at IS NULL`,
		likePattern,
	)
	if err != nil {
		return err
	}
	var toResolve []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			_ = rows.Close()
			return err
		}
		if _, ok := keep[fp]; !ok {
			toResolve = append(toResolve, fp)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, fp := range toResolve {
		if err := s.ResolveFingerprint(ctx, fp, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) periodAdjustedProxyBytes(ctx context.Context, from, to time.Time) (uint64, error) {
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return 0, err
	}

	fromUnix := from.UTC().Unix()
	toUnix := to.UTC().Unix()
	fromDay := (fromUnix / daySeconds) * daySeconds

	var rawSeen, adjSum, legAdjSum uint64

	accumulate := func(rows *sql.Rows) error {
		for rows.Next() {
			var class, outbound string
			var rx, tx int64
			if err := rows.Scan(&class, &outbound, &rx, &tx); err != nil {
				return err
			}
			uRx, uTx := clampUint64(rx), clampUint64(tx)
			switch class {
			case accounting.ClassProxyRaw:
				rawSeen += uRx + uTx
				m := rules.Match(outbound)
				adjSum += accounting.ScaleBytes(uRx, m.Factor) + accounting.ScaleBytes(uTx, m.Factor)
			case accounting.ClassProxyAdjusted:
				legAdjSum += uRx + uTx
			}
		}
		return rows.Err()
	}

	sampleRows, err := s.db.QueryContext(
		ctx,
		`SELECT class, outbound, SUM(rx), SUM(tx)
		 FROM ledger_samples
		 WHERE device_id = '' AND class IN (?, ?)
		   AND sampled_at >= ? AND sampled_at <= ?
		 GROUP BY class, outbound`,
		accounting.ClassProxyRaw,
		accounting.ClassProxyAdjusted,
		fromUnix,
		toUnix,
	)
	if err != nil {
		return 0, err
	}
	if err := accumulate(sampleRows); err != nil {
		_ = sampleRows.Close()
		return 0, err
	}
	if err := sampleRows.Close(); err != nil {
		return 0, err
	}

	dailyRows, err := s.db.QueryContext(
		ctx,
		`SELECT class, outbound, SUM(rx), SUM(tx)
		 FROM ledger_daily
		 WHERE device_id = '' AND class IN (?, ?)
		   AND day >= ? AND day <= ?
		   AND day NOT IN (SELECT DISTINCT (sampled_at / ?) * ? FROM ledger_samples)
		 GROUP BY class, outbound`,
		accounting.ClassProxyRaw,
		accounting.ClassProxyAdjusted,
		fromDay,
		toUnix,
		daySeconds,
		daySeconds,
	)
	if err != nil {
		return 0, err
	}
	if err := accumulate(dailyRows); err != nil {
		_ = dailyRows.Close()
		return 0, err
	}
	if err := dailyRows.Close(); err != nil {
		return 0, err
	}

	if rawSeen == 0 && legAdjSum > 0 {
		return legAdjSum, nil
	}
	return adjSum, nil
}

func BillingPeriodInLoc(now time.Time, resetDay int, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	localNow := now.In(loc)
	if resetDay < 1 || resetDay > 28 {
		resetDay = 1
	}
	year, month, day := localNow.Date()
	from := time.Date(year, month, resetDay, 0, 0, 0, 0, loc)
	if day < resetDay {
		from = from.AddDate(0, -1, 0)
	}
	return from.UTC(), now.UTC()
}

func billingPeriod(now time.Time, resetDay int) (time.Time, time.Time) {
	return BillingPeriodInLoc(now, resetDay, time.UTC)
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
