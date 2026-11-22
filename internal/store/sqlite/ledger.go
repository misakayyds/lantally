package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
)

type DimensionIncrement struct {
	Type string
	Key  string
	Rx   uint64
	Tx   uint64
}

func (s *Store) ApplyLedgerOnce(
	ctx context.Context,
	siteID, nodeID, bootID string,
	seq uint64,
	sampledAt time.Time,
	increments []accounting.Increment,
) (applied bool, err error) {
	return s.ApplyLedgerWithDimensionsOnce(ctx, siteID, nodeID, bootID, seq, sampledAt, increments, nil)
}

func (s *Store) ApplyLedgerWithDimensionsOnce(
	ctx context.Context,
	siteID, nodeID, bootID string,
	seq uint64,
	sampledAt time.Time,
	increments []accounting.Increment,
	dimensions []DimensionIncrement,
) (applied bool, err error) {
	if seq > math.MaxInt64 {
		return false, ErrSequenceRegression
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

	result, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO ledger_applied (node_id, boot_id, sequence) VALUES (?, ?, ?)`,
		nodeID,
		bootID,
		seq,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}

	if sampledAt.IsZero() {
		sampledAt = time.Now().UTC()
	}
	sampledUnix := sampledAt.UTC().Unix()
	dayUnix := (sampledUnix / daySeconds) * daySeconds

	hasIfaceFromIncrements := false
	for _, item := range increments {
		if item.Rx == 0 && item.Tx == 0 {
			continue
		}
		if item.Class == accounting.ClassIface || item.Iface != "" {
			hasIfaceFromIncrements = true
			if ifaceName := strings.TrimSpace(item.Iface); ifaceName != "" {
				if err = insertDimensionSampleAndDaily(ctx, tx, sampledUnix, dayUnix, siteID, nodeID, "iface", ifaceName, item.Rx, item.Tx); err != nil {
					return false, err
				}
			}
			continue
		}
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO ledger_totals (site_id, node_id, device_id, class, outbound, rx, tx)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(site_id, node_id, device_id, class, outbound)
			 DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx`,
			siteID,
			nodeID,
			item.DeviceID,
			item.Class,
			item.Outbound,
			int64(item.Rx),
			int64(item.Tx),
		)
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO ledger_samples (sampled_at, site_id, node_id, device_id, class, outbound, rx, tx)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sampledUnix,
			siteID,
			nodeID,
			item.DeviceID,
			item.Class,
			item.Outbound,
			int64(item.Rx),
			int64(item.Tx),
		)
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO ledger_daily (day, site_id, node_id, device_id, class, outbound, rx, tx)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(day, site_id, node_id, device_id, class, outbound)
			 DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx`,
			dayUnix,
			siteID,
			nodeID,
			item.DeviceID,
			item.Class,
			item.Outbound,
			int64(item.Rx),
			int64(item.Tx),
		)
		if err != nil {
			return false, err
		}
	}

	for _, dim := range dimensions {
		if dim.Rx == 0 && dim.Tx == 0 {
			continue
		}
		key := strings.TrimSpace(dim.Key)
		if key == "" {
			continue
		}
		if !validDimensionType(dim.Type) {
			return false, fmt.Errorf("unknown dimension type: %q", dim.Type)
		}
		if dim.Type == "iface" && hasIfaceFromIncrements {
			continue
		}
		if err = insertDimensionSampleAndDaily(ctx, tx, sampledUnix, dayUnix, siteID, nodeID, dim.Type, key, dim.Rx, dim.Tx); err != nil {
			return false, err
		}
	}

	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func insertDimensionSampleAndDaily(
	ctx context.Context,
	tx *sql.Tx,
	sampledUnix, dayUnix int64,
	siteID, nodeID, dimType, dimKey string,
	rx, txBytes uint64,
) error {
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO dimension_samples (sampled_at, site_id, node_id, dim_type, dim_key, rx, tx)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sampledUnix,
		siteID,
		nodeID,
		dimType,
		dimKey,
		int64(rx),
		int64(txBytes),
	); err != nil {
		return err
	}
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO dimension_daily (day, site_id, node_id, dim_type, dim_key, rx, tx)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(day, site_id, node_id, dim_type, dim_key)
		 DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx`,
		dayUnix,
		siteID,
		nodeID,
		dimType,
		dimKey,
		int64(rx),
		int64(txBytes),
	)
	return err
}

func validDimensionType(t string) bool {
	switch t {
	case "group", "process", "domain", "iface":
		return true
	default:
		return false
	}
}

func emptyBreakdown() map[string]RxTxTotal {
	return map[string]RxTxTotal{
		accounting.ClassTotal:           {},
		accounting.ClassDirect:          {},
		accounting.ClassProxyRaw:        {},
		accounting.ClassProxyAdjusted:   {},
		accounting.ClassProxyUnadjusted: {},
	}
}

func addRxTx(m map[string]RxTxTotal, key string, rx, tx uint64) {
	cur := m[key]
	cur.Rx += rx
	cur.Tx += tx
	cur.Total = cur.Rx + cur.Tx
	m[key] = cur
}

func (s *Store) LedgerTotals(ctx context.Context) (map[string]uint64, error) {
	breakdown, err := s.LedgerBreakdown(ctx, time.Time{}, time.Time{}, "", "")
	if err != nil {
		return nil, err
	}
	return flattenBreakdown(breakdown), nil
}

func (s *Store) NodeLedgerTotals(ctx context.Context, siteID, nodeID string) (map[string]uint64, error) {
	breakdown, err := s.LedgerBreakdown(ctx, time.Time{}, time.Time{}, siteID, nodeID)
	if err != nil {
		return nil, err
	}
	return flattenBreakdown(breakdown), nil
}

func flattenBreakdown(breakdown map[string]RxTxTotal) map[string]uint64 {
	out := make(map[string]uint64, len(breakdown))
	for k, v := range breakdown {
		out[k] = v.Total
	}
	return out
}

func (s *Store) LedgerBreakdown(
	ctx context.Context,
	from, to time.Time,
	siteID, nodeID string,
) (map[string]RxTxTotal, error) {
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return nil, err
	}

	var where []string
	var baseArgs []any
	where = append(where, "device_id = ''")
	if strings.TrimSpace(siteID) != "" {
		where = append(where, "site_id = ?")
		baseArgs = append(baseArgs, strings.TrimSpace(siteID))
	}
	if strings.TrimSpace(nodeID) != "" {
		where = append(where, "node_id = ?")
		baseArgs = append(baseArgs, strings.TrimSpace(nodeID))
	}
	baseWhere := strings.Join(where, " AND ")

	fromSQL := `ledger_totals`
	whereSQL := baseWhere
	var args []any
	if !from.IsZero() && !to.IsZero() {
		if to.Before(from) {
			return emptyBreakdown(), nil
		}
		fromUnix := from.UTC().Unix()
		toUnix := to.UTC().Unix()
		fromDay := (fromUnix / daySeconds) * daySeconds
		if to.Sub(from) > range7d {
			fromSQL = `ledger_daily`
			whereSQL = baseWhere + ` AND day >= ? AND day <= ?`
			args = append(args, baseArgs...)
			args = append(args, fromDay, toUnix)
		} else {
			fromSQL = `(
				SELECT class, outbound, rx, tx
				FROM ledger_samples
				WHERE ` + baseWhere + ` AND sampled_at >= ? AND sampled_at <= ?
				UNION ALL
				SELECT class, outbound, rx, tx
				FROM ledger_daily d
				WHERE ` + baseWhere + ` AND d.day >= ? AND d.day <= ?
				  AND NOT EXISTS (
					SELECT 1 FROM ledger_samples s
					WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
				  )
			)`
			whereSQL = `1=1`
			args = append(args, baseArgs...)
			args = append(args, fromUnix, toUnix)
			args = append(args, baseArgs...)
			args = append(args, fromDay, toUnix, daySeconds)
		}
	} else {
		args = append(args, baseArgs...)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT class, outbound, SUM(rx), SUM(tx)
		 FROM `+fromSQL+`
		 WHERE `+whereSQL+`
		 GROUP BY class, outbound`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := emptyBreakdown()
	var legAdjRx, legAdjTx, legUnadjRx, legUnadjTx uint64
	for rows.Next() {
		var class, outbound string
		var rx, tx int64
		if err := rows.Scan(&class, &outbound, &rx, &tx); err != nil {
			return nil, err
		}
		uRx, uTx := clampUint64(rx), clampUint64(tx)
		switch class {
		case accounting.ClassTotal, accounting.ClassDirect:
			addRxTx(totals, class, uRx, uTx)
		case accounting.ClassProxyRaw:
			addRxTx(totals, accounting.ClassProxyRaw, uRx, uTx)
			m := rules.Match(outbound)
			addRxTx(totals, accounting.ClassProxyAdjusted, accounting.ScaleBytes(uRx, m.Factor), accounting.ScaleBytes(uTx, m.Factor))
			if m.MatchSource == accounting.SourceDefault {
				addRxTx(totals, accounting.ClassProxyUnadjusted, uRx, uTx)
			}
		case accounting.ClassProxyAdjusted:
			legAdjRx += uRx
			legAdjTx += uTx
		case accounting.ClassProxyUnadjusted:
			legUnadjRx += uRx
			legUnadjTx += uTx
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if totals[accounting.ClassProxyRaw].Total == 0 && (legAdjRx+legAdjTx > 0 || legUnadjRx+legUnadjTx > 0) {
		addRxTx(totals, accounting.ClassProxyAdjusted, legAdjRx, legAdjTx)
		addRxTx(totals, accounting.ClassProxyUnadjusted, legUnadjRx, legUnadjTx)
	}
	return totals, nil
}

func (s *Store) ListDeviceLedgers(ctx context.Context) ([]DeviceLedger, error) {
	return s.ListDeviceLedgersInRange(ctx, time.Time{}, time.Time{})
}

func (s *Store) ListDeviceLedgersInRange(ctx context.Context, from, to time.Time) ([]DeviceLedger, error) {
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return nil, err
	}

	devRows, err := s.db.QueryContext(
		ctx,
		`SELECT d.id, d.site_id, IFNULL(d.display_name, '')
		 FROM devices d
		 WHERE d.canonical_id IS NULL OR d.canonical_id = d.id
		 ORDER BY d.site_id, d.id`,
	)
	if err != nil {
		return nil, err
	}
	defer devRows.Close()

	index := map[string]int{}
	var devices []DeviceLedger
	for devRows.Next() {
		var id, siteID, name string
		if err := devRows.Scan(&id, &siteID, &name); err != nil {
			return nil, err
		}
		index[siteID+"|"+id] = len(devices)
		devices = append(devices, DeviceLedger{
			ID:        id,
			SiteID:    siteID,
			Name:      name,
			Bytes:     map[string]uint64{},
			Breakdown: map[string]RxTxTotal{},
		})
	}
	if err := devRows.Err(); err != nil {
		return nil, err
	}
	if err := devRows.Close(); err != nil {
		return nil, err
	}

	fromSQL := `ledger_totals`
	whereSQL := `device_id != ''`
	var args []any
	if !from.IsZero() && !to.IsZero() {
		if to.Before(from) {
			if err := s.attachDeviceEvidence(ctx, devices); err != nil {
				return nil, err
			}
			return devices, nil
		}
		fromUnix := from.UTC().Unix()
		toUnix := to.UTC().Unix()
		fromDay := (fromUnix / daySeconds) * daySeconds
		if to.Sub(from) > range7d {
			fromSQL = `ledger_daily`
			whereSQL = `device_id != '' AND day >= ? AND day <= ?`
			args = append(args, fromDay, toUnix)
		} else {
			fromSQL = `(
				SELECT site_id, device_id, class, outbound, rx, tx
				FROM ledger_samples
				WHERE device_id != '' AND sampled_at >= ? AND sampled_at <= ?
				UNION ALL
				SELECT site_id, device_id, class, outbound, rx, tx
				FROM ledger_daily d
				WHERE device_id != '' AND d.day >= ? AND d.day <= ?
				  AND NOT EXISTS (
					SELECT 1 FROM ledger_samples s
					WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
				  )
			)`
			whereSQL = `1=1`
			args = append(args, fromUnix, toUnix, fromDay, toUnix, daySeconds)
		}
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT site_id, device_id, class, outbound, SUM(rx), SUM(tx)
		 FROM `+fromSQL+`
		 WHERE `+whereSQL+`
		 GROUP BY site_id, device_id, class, outbound`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type legAccum struct {
		adjRx, adjTx, unadjRx, unadjTx uint64
	}
	legacy := map[int]legAccum{}
	for rows.Next() {
		var siteID, deviceID, class, outbound string
		var rx, tx int64
		if err := rows.Scan(&siteID, &deviceID, &class, &outbound, &rx, &tx); err != nil {
			return nil, err
		}
		pos, ok := index[siteID+"|"+deviceID]
		if !ok {
			continue
		}
		uRx, uTx := clampUint64(rx), clampUint64(tx)
		switch class {
		case accounting.ClassTotal, accounting.ClassDirect:
			addRxTx(devices[pos].Breakdown, class, uRx, uTx)
		case accounting.ClassProxyRaw:
			addRxTx(devices[pos].Breakdown, accounting.ClassProxyRaw, uRx, uTx)
			m := rules.Match(outbound)
			addRxTx(devices[pos].Breakdown, accounting.ClassProxyAdjusted, accounting.ScaleBytes(uRx, m.Factor), accounting.ScaleBytes(uTx, m.Factor))
			if m.MatchSource == accounting.SourceDefault {
				addRxTx(devices[pos].Breakdown, accounting.ClassProxyUnadjusted, uRx, uTx)
			}
		case accounting.ClassProxyAdjusted:
			cur := legacy[pos]
			cur.adjRx += uRx
			cur.adjTx += uTx
			legacy[pos] = cur
		case accounting.ClassProxyUnadjusted:
			cur := legacy[pos]
			cur.unadjRx += uRx
			cur.unadjTx += uTx
			legacy[pos] = cur
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for pos := range devices {
		if devices[pos].Breakdown[accounting.ClassProxyRaw].Total == 0 {
			if leg, ok := legacy[pos]; ok && (leg.adjRx+leg.adjTx > 0 || leg.unadjRx+leg.unadjTx > 0) {
				if leg.adjRx+leg.adjTx > 0 {
					addRxTx(devices[pos].Breakdown, accounting.ClassProxyAdjusted, leg.adjRx, leg.adjTx)
				}
				if leg.unadjRx+leg.unadjTx > 0 {
					addRxTx(devices[pos].Breakdown, accounting.ClassProxyUnadjusted, leg.unadjRx, leg.unadjTx)
				}
			}
		}
		for class, v := range devices[pos].Breakdown {
			if v.Total > 0 {
				devices[pos].Bytes[class] = v.Total
			}
		}
		if tot, ok := devices[pos].Breakdown[accounting.ClassTotal]; ok && tot.Total > 0 {
			devices[pos].Rx = tot.Rx
			devices[pos].Tx = tot.Tx
			devices[pos].Total = tot.Total
		} else {
			d := devices[pos].Breakdown[accounting.ClassDirect]
			p := devices[pos].Breakdown[accounting.ClassProxyRaw]
			devices[pos].Rx = d.Rx + p.Rx
			devices[pos].Tx = d.Tx + p.Tx
			devices[pos].Total = devices[pos].Rx + devices[pos].Tx
		}
	}

	if err := s.attachDeviceEvidence(ctx, devices); err != nil {
		return nil, err
	}
	return devices, nil
}

func deviceScope(group string) string {
	if group == "device" {
		return "1=1"
	}
	return "device_id = ''"
}

const (
	SampleRetentionDays = 14
	sampleRetention     = SampleRetentionDays * 24 * time.Hour
	daySeconds          = 86400
	bucket30m           = 1800
	bucket2h            = 7200
	range72h            = 72 * time.Hour
	range7d             = 7 * 24 * time.Hour
)

func (s *Store) hasUnrepresentedDaily(
	ctx context.Context,
	sampleTable, dailyTable string,
	from, to time.Time,
) (hasSamples bool, hasDailyOnly bool, err error) {
	if to.Before(from) {
		return false, false, nil
	}
	fromUnix := from.UTC().Unix()
	toUnix := to.UTC().Unix()
	var sExists int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM `+sampleTable+` WHERE sampled_at >= ? AND sampled_at <= ?)`,
		fromUnix,
		toUnix,
	).Scan(&sExists); err != nil {
		return false, false, err
	}
	fromDay := (fromUnix / daySeconds) * daySeconds
	var dExists int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT EXISTS(
			SELECT 1 FROM `+dailyTable+` d
			WHERE d.day >= ? AND d.day <= ?
			  AND NOT EXISTS (
				SELECT 1 FROM `+sampleTable+` s
				WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
			  )
		)`,
		fromDay,
		toUnix,
		daySeconds,
	).Scan(&dExists); err != nil {
		return false, false, err
	}
	return sExists == 1, dExists == 1, nil
}

func DefaultBucketSeconds(from, to time.Time) int {
	if to.Before(from) {
		from, to = to, from
	}
	delta := to.Sub(from)
	switch {
	case delta <= range72h:
		return bucket30m
	case delta <= range7d:
		return bucket2h
	default:
		return daySeconds
	}
}

func (s *Store) MaintainLedgers(ctx context.Context, now time.Time) error {
	if err := s.RollupDaily(ctx, now); err != nil {
		return err
	}
	if err := s.RollupDimensionsDaily(ctx, now); err != nil {
		return err
	}
	if err := s.PurgeSamples(ctx, now); err != nil {
		return err
	}
	return s.PurgeDimensionSamples(ctx, now)
}

func (s *Store) RollupDaily(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := samplePurgeCutoffDay(now)
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO ledger_daily (day, site_id, node_id, device_id, class, outbound, rx, tx)
		 SELECT (sampled_at / ?) * ?, site_id, node_id, device_id, class, outbound, SUM(rx), SUM(tx)
		 FROM ledger_samples
		 WHERE sampled_at >= ?
		 GROUP BY 1, site_id, node_id, device_id, class, outbound
		 ON CONFLICT(day, site_id, node_id, device_id, class, outbound)
		 DO UPDATE SET rx = excluded.rx, tx = excluded.tx`,
		daySeconds,
		daySeconds,
		cutoff,
	); err != nil {
		return err
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO ledger_daily (day, site_id, node_id, device_id, class, outbound, rx, tx)
		 SELECT (sampled_at / ?) * ?, site_id, node_id, device_id, class, outbound, SUM(rx), SUM(tx)
		 FROM ledger_samples
		 WHERE sampled_at < ?
		 GROUP BY 1, site_id, node_id, device_id, class, outbound
		 ON CONFLICT(day, site_id, node_id, device_id, class, outbound)
		 DO NOTHING`,
		daySeconds,
		daySeconds,
		cutoff,
	)
	return err
}

func (s *Store) RollupDimensionsDaily(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := samplePurgeCutoffDay(now)
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO dimension_daily (day, site_id, node_id, dim_type, dim_key, rx, tx)
		 SELECT (sampled_at / ?) * ?, site_id, node_id, dim_type, dim_key, SUM(rx), SUM(tx)
		 FROM dimension_samples
		 WHERE sampled_at >= ?
		 GROUP BY 1, site_id, node_id, dim_type, dim_key
		 ON CONFLICT(day, site_id, node_id, dim_type, dim_key)
		 DO UPDATE SET rx = excluded.rx, tx = excluded.tx`,
		daySeconds,
		daySeconds,
		cutoff,
	); err != nil {
		return err
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO dimension_daily (day, site_id, node_id, dim_type, dim_key, rx, tx)
		 SELECT (sampled_at / ?) * ?, site_id, node_id, dim_type, dim_key, SUM(rx), SUM(tx)
		 FROM dimension_samples
		 WHERE sampled_at < ?
		 GROUP BY 1, site_id, node_id, dim_type, dim_key
		 ON CONFLICT(day, site_id, node_id, dim_type, dim_key)
		 DO NOTHING`,
		daySeconds,
		daySeconds,
		cutoff,
	)
	return err
}

func samplePurgeCutoffDay(now time.Time) int64 {
	return now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -SampleRetentionDays).Unix()
}

func (s *Store) PurgeSamples(ctx context.Context, now time.Time) error {
	cutoff := samplePurgeCutoffDay(now)
	_, err := s.db.ExecContext(ctx, `DELETE FROM ledger_samples WHERE sampled_at < ?`, cutoff)
	return err
}

func (s *Store) PurgeDimensionSamples(ctx context.Context, now time.Time) error {
	cutoff := samplePurgeCutoffDay(now)
	_, err := s.db.ExecContext(ctx, `DELETE FROM dimension_samples WHERE sampled_at < ?`, cutoff)
	return err
}

type TrafficQuery struct {
	From          time.Time
	To            time.Time
	BucketSeconds int
	Class         string
	Group         string
	SiteID        string
	NodeID        string
	DeviceID      string
	Range         string
}

type TrafficPoint struct {
	Bucket   time.Time         `json:"t"`
	Values   map[string]uint64 `json:"values"`
	RxValues map[string]uint64 `json:"rx_values,omitempty"`
	TxValues map[string]uint64 `json:"tx_values,omitempty"`
}

type TrafficSeries struct {
	From          time.Time            `json:"from,omitempty"`
	To            time.Time            `json:"to,omitempty"`
	BucketSeconds int                  `json:"bucket_seconds"`
	Keys          []string             `json:"keys"`
	Totals        map[string]uint64    `json:"totals"`
	RxTotals      map[string]uint64    `json:"rx_totals,omitempty"`
	TxTotals      map[string]uint64    `json:"tx_totals,omitempty"`
	Breakdown     map[string]RxTxTotal `json:"breakdown,omitempty"`
	Points        []TrafficPoint       `json:"points"`
}

func (s *Store) TrafficSeries(ctx context.Context, query TrafficQuery) (TrafficSeries, error) {
	if query.To.IsZero() {
		query.To = time.Now().UTC()
	}
	if query.From.IsZero() {
		query.From = query.To.Add(-72 * time.Hour)
	}
	if query.Group == "" {
		query.Group = "node"
	}
	if query.Class == "" {
		query.Class = accounting.ClassTotal
	}
	if query.Class == "proxy_weighted" {
		query.Class = accounting.ClassProxyAdjusted
	}

	isDimGroup := query.Group == "group" || query.Group == "process" || query.Group == "domain" || query.Group == "iface"
	sampleTbl, dailyTbl := "ledger_samples", "ledger_daily"
	if isDimGroup {
		sampleTbl, dailyTbl = "dimension_samples", "dimension_daily"
	}
	pureDaily := query.To.Sub(query.From) > range7d
	if query.BucketSeconds <= 0 {
		query.BucketSeconds = DefaultBucketSeconds(query.From, query.To)
		if !pureDaily {
			hasSamples, hasDailyOnly, err := s.hasUnrepresentedDaily(ctx, sampleTbl, dailyTbl, query.From, query.To)
			if err != nil {
				return TrafficSeries{}, err
			}
			if !hasSamples && hasDailyOnly && query.BucketSeconds < daySeconds {
				query.BucketSeconds = daySeconds
			}
		}
	}

	bs := int64(query.BucketSeconds)
	fromUnix := query.From.UTC().Unix()
	toUnix := query.To.UTC().Unix()
	fromDay := (fromUnix / daySeconds) * daySeconds
	fromSec := (fromUnix / bs) * bs
	lastBucket := (toUnix / bs) * bs

	pointsByBucket := map[int64]map[string]uint64{}
	rxByBucket := map[int64]map[string]uint64{}
	txByBucket := map[int64]map[string]uint64{}
	totals := map[string]uint64{}
	rxTotals := map[string]uint64{}
	txTotals := map[string]uint64{}
	keySet := map[string]struct{}{}

	addPoint := func(bucket int64, key string, rx, tx uint64) {
		if bucket < fromSec {
			bucket = fromSec
		}
		if pointsByBucket[bucket] == nil {
			pointsByBucket[bucket] = map[string]uint64{}
			rxByBucket[bucket] = map[string]uint64{}
			txByBucket[bucket] = map[string]uint64{}
		}
		pointsByBucket[bucket][key] += rx + tx
		rxByBucket[bucket][key] += rx
		txByBucket[bucket][key] += tx
		totals[key] += rx + tx
		rxTotals[key] += rx
		txTotals[key] += tx
		keySet[key] = struct{}{}
	}

	if !query.To.Before(query.From) {
		switch query.Group {
		case "group", "process", "domain", "iface":
			if strings.TrimSpace(query.DeviceID) != "" {
				return TrafficSeries{}, fmt.Errorf("%w: device filter is not supported for dimension groups", ErrInvalidInput)
			}
			var filters []string
			var filterArgs []any
			if query.SiteID != "" {
				filters = append(filters, "site_id = ?")
				filterArgs = append(filterArgs, query.SiteID)
			}
			if query.NodeID != "" {
				filters = append(filters, "node_id = ?")
				filterArgs = append(filterArgs, query.NodeID)
			}
			extra := "1=1"
			if len(filters) > 0 {
				extra = strings.Join(filters, " AND ")
			}

			var querySQL string
			var args []any
			if pureDaily {
				querySQL = `SELECT (day / ?) * ? AS bucket, dim_key, SUM(rx), SUM(tx)
				 FROM dimension_daily
				 WHERE dim_type = ? AND ` + extra + `
				   AND day >= ? AND day <= ?
				 GROUP BY bucket, dim_key
				 ORDER BY bucket, dim_key`
				args = append(args, query.BucketSeconds, query.BucketSeconds, query.Group)
				args = append(args, filterArgs...)
				args = append(args, fromDay, toUnix)
			} else {
				querySQL = `SELECT bucket, dim_key, SUM(rx), SUM(tx)
				 FROM (
					SELECT (sampled_at / ?) * ? AS bucket, dim_key, rx, tx
					FROM dimension_samples
					WHERE dim_type = ? AND ` + extra + `
					  AND sampled_at >= ? AND sampled_at <= ?
					UNION ALL
					SELECT (d.day / ?) * ? AS bucket, d.dim_key, d.rx, d.tx
					FROM dimension_daily d
					WHERE d.dim_type = ? AND ` + extra + `
					  AND d.day >= ? AND d.day <= ?
					  AND NOT EXISTS (
						SELECT 1 FROM dimension_samples s
						WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
					  )
				 )
				 GROUP BY bucket, dim_key
				 ORDER BY bucket, dim_key`
				args = append(args, query.BucketSeconds, query.BucketSeconds, query.Group)
				args = append(args, filterArgs...)
				args = append(args, fromUnix, toUnix)
				args = append(args, query.BucketSeconds, query.BucketSeconds, query.Group)
				args = append(args, filterArgs...)
				args = append(args, fromDay, toUnix, daySeconds)
			}

			rows, err := s.db.QueryContext(ctx, querySQL, args...)
			if err != nil {
				return TrafficSeries{}, err
			}
			defer rows.Close()
			for rows.Next() {
				var bucket int64
				var key string
				var rx, tx int64
				if err := rows.Scan(&bucket, &key, &rx, &tx); err != nil {
					return TrafficSeries{}, err
				}
				addPoint(bucket, key, clampUint64(rx), clampUint64(tx))
			}
			if err := rows.Err(); err != nil {
				return TrafficSeries{}, err
			}

		case "node", "device", "outbound", "class":
			dynamicEval := query.Group == "class" ||
				query.Class == accounting.ClassProxyAdjusted ||
				query.Class == accounting.ClassProxyUnadjusted

			var filters []string
			var filterArgs []any
			if query.SiteID != "" {
				filters = append(filters, "site_id = ?")
				filterArgs = append(filterArgs, query.SiteID)
			}
			if query.NodeID != "" {
				filters = append(filters, "node_id = ?")
				filterArgs = append(filterArgs, query.NodeID)
			}
			if query.DeviceID != "" {
				filters = append(filters, "device_id = ?")
				filterArgs = append(filterArgs, query.DeviceID)
			}
			extra := "1=1"
			if len(filters) > 0 {
				extra = strings.Join(filters, " AND ")
			}

			if dynamicEval {
				rules, err := s.CompiledMultiplierRules(ctx)
				if err != nil {
					return TrafficSeries{}, err
				}

				keyExpr := "node_id"
				whereClass := `class IN (?, ?, ?)`
				classArgs := []any{accounting.ClassProxyRaw, accounting.ClassProxyAdjusted, accounting.ClassProxyUnadjusted}
				switch query.Group {
				case "class":
					keyExpr = "''"
					whereClass = `class IN ('direct', 'proxy_raw', 'proxy_adjusted', 'proxy_unadjusted')`
					classArgs = nil
				case "outbound":
					keyExpr = "outbound"
					whereClass = `class IN (?, ?, ?) AND outbound != ''`
				case "device":
					keyExpr = "device_id"
					whereClass = `class IN (?, ?, ?) AND device_id != ''`
				}

				var querySQL string
				var args []any
				baseCond := deviceScope(query.Group) + ` AND ` + whereClass + ` AND ` + extra
				if pureDaily {
					querySQL = `SELECT (day / ?) * ? AS bucket, ` + keyExpr + `, class, outbound, SUM(rx), SUM(tx)
					 FROM ledger_daily
					 WHERE ` + baseCond + ` AND day >= ? AND day <= ?
					 GROUP BY bucket, ` + keyExpr + `, class, outbound
					 ORDER BY bucket`
					args = append(args, query.BucketSeconds, query.BucketSeconds)
					args = append(args, classArgs...)
					args = append(args, filterArgs...)
					args = append(args, fromDay, toUnix)
				} else {
					querySQL = `SELECT bucket, k, class, outbound, SUM(rx), SUM(tx)
					 FROM (
						SELECT (sampled_at / ?) * ? AS bucket, ` + keyExpr + ` AS k, class, outbound, rx, tx
						FROM ledger_samples
						WHERE ` + baseCond + ` AND sampled_at >= ? AND sampled_at <= ?
						UNION ALL
						SELECT (d.day / ?) * ? AS bucket, ` + keyExpr + ` AS k, d.class, d.outbound, d.rx, d.tx
						FROM ledger_daily d
						WHERE ` + baseCond + ` AND d.day >= ? AND d.day <= ?
						  AND NOT EXISTS (
							SELECT 1 FROM ledger_samples s
							WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
						  )
					 )
					 GROUP BY bucket, k, class, outbound
					 ORDER BY bucket`
					args = append(args, query.BucketSeconds, query.BucketSeconds)
					args = append(args, classArgs...)
					args = append(args, filterArgs...)
					args = append(args, fromUnix, toUnix)
					args = append(args, query.BucketSeconds, query.BucketSeconds)
					args = append(args, classArgs...)
					args = append(args, filterArgs...)
					args = append(args, fromDay, toUnix, daySeconds)
				}

				rows, err := s.db.QueryContext(ctx, querySQL, args...)
				if err != nil {
					return TrafficSeries{}, err
				}
				defer rows.Close()

				type legRow struct {
					bucket int64
					key    string
					class  string
					rx, tx uint64
				}
				var legacyRows []legRow
				var rawSeen uint64

				for rows.Next() {
					var bucket int64
					var key, class, outbound string
					var rx, tx int64
					if err := rows.Scan(&bucket, &key, &class, &outbound, &rx, &tx); err != nil {
						return TrafficSeries{}, err
					}
					uRx, uTx := clampUint64(rx), clampUint64(tx)
					if query.Group == "class" {
						switch class {
						case accounting.ClassDirect:
							addPoint(bucket, accounting.ClassDirect, uRx, uTx)
						case accounting.ClassProxyRaw:
							rawSeen += uRx + uTx
							addPoint(bucket, accounting.ClassProxyRaw, uRx, uTx)
							m := rules.Match(outbound)
							addPoint(bucket, accounting.ClassProxyAdjusted, accounting.ScaleBytes(uRx, m.Factor), accounting.ScaleBytes(uTx, m.Factor))
							if m.MatchSource == accounting.SourceDefault {
								addPoint(bucket, accounting.ClassProxyUnadjusted, uRx, uTx)
							}
						case accounting.ClassProxyAdjusted, accounting.ClassProxyUnadjusted:
							legacyRows = append(legacyRows, legRow{bucket: bucket, key: class, class: class, rx: uRx, tx: uTx})
						}
						continue
					}
					switch class {
					case accounting.ClassProxyRaw:
						rawSeen += uRx + uTx
						m := rules.Match(outbound)
						if query.Class == accounting.ClassProxyAdjusted {
							addPoint(bucket, key, accounting.ScaleBytes(uRx, m.Factor), accounting.ScaleBytes(uTx, m.Factor))
						} else if query.Class == accounting.ClassProxyUnadjusted && m.MatchSource == accounting.SourceDefault {
							addPoint(bucket, key, uRx, uTx)
						}
					case query.Class:
						legacyRows = append(legacyRows, legRow{bucket: bucket, key: key, class: class, rx: uRx, tx: uTx})
					}
				}
				if err := rows.Err(); err != nil {
					return TrafficSeries{}, err
				}
				if rawSeen == 0 && len(legacyRows) > 0 {
					for _, lr := range legacyRows {
						addPoint(lr.bucket, lr.key, lr.rx, lr.tx)
					}
				}
			} else {
				keyExpr := "node_id"
				whereClass := `class = ?`
				switch query.Group {
				case "outbound":
					keyExpr = "outbound"
					whereClass = `class = ? AND outbound != ''`
				case "device":
					keyExpr = "device_id"
					whereClass = `class = ? AND device_id != ''`
				}

				var querySQL string
				var args []any
				baseCond := deviceScope(query.Group) + ` AND ` + whereClass + ` AND ` + extra
				if pureDaily {
					querySQL = `SELECT (day / ?) * ? AS bucket, ` + keyExpr + `, SUM(rx), SUM(tx)
					 FROM ledger_daily
					 WHERE ` + baseCond + ` AND day >= ? AND day <= ?
					 GROUP BY bucket, ` + keyExpr + `
					 ORDER BY bucket, ` + keyExpr
					args = append(args, query.BucketSeconds, query.BucketSeconds, query.Class)
					args = append(args, filterArgs...)
					args = append(args, fromDay, toUnix)
				} else {
					querySQL = `SELECT bucket, k, SUM(rx), SUM(tx)
					 FROM (
						SELECT (sampled_at / ?) * ? AS bucket, ` + keyExpr + ` AS k, rx, tx
						FROM ledger_samples
						WHERE ` + baseCond + ` AND sampled_at >= ? AND sampled_at <= ?
						UNION ALL
						SELECT (d.day / ?) * ? AS bucket, ` + keyExpr + ` AS k, d.rx, d.tx
						FROM ledger_daily d
						WHERE ` + baseCond + ` AND d.day >= ? AND d.day <= ?
						  AND NOT EXISTS (
							SELECT 1 FROM ledger_samples s
							WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
						  )
					 )
					 GROUP BY bucket, k
					 ORDER BY bucket, k`
					args = append(args, query.BucketSeconds, query.BucketSeconds, query.Class)
					args = append(args, filterArgs...)
					args = append(args, fromUnix, toUnix)
					args = append(args, query.BucketSeconds, query.BucketSeconds, query.Class)
					args = append(args, filterArgs...)
					args = append(args, fromDay, toUnix, daySeconds)
				}

				rows, err := s.db.QueryContext(ctx, querySQL, args...)
				if err != nil {
					return TrafficSeries{}, err
				}
				defer rows.Close()
				for rows.Next() {
					var bucket int64
					var key string
					var rx, tx int64
					if err := rows.Scan(&bucket, &key, &rx, &tx); err != nil {
						return TrafficSeries{}, err
					}
					addPoint(bucket, key, clampUint64(rx), clampUint64(tx))
				}
				if err := rows.Err(); err != nil {
					return TrafficSeries{}, err
				}
			}

		default:
			return TrafficSeries{}, fmt.Errorf("%w: invalid group", ErrInvalidInput)
		}
	} else {
		switch query.Group {
		case "group", "process", "domain", "iface":
			if strings.TrimSpace(query.DeviceID) != "" {
				return TrafficSeries{}, fmt.Errorf("%w: device filter is not supported for dimension groups", ErrInvalidInput)
			}
		case "node", "device", "outbound", "class":
		default:
			return TrafficSeries{}, fmt.Errorf("%w: invalid group", ErrInvalidInput)
		}
	}

	keys := sortedKeys(keySet)
	var points []TrafficPoint
	if query.To.Before(query.From) {
		points = []TrafficPoint{}
	} else {
		capPoints := int((lastBucket-fromSec)/bs) + 1
		if capPoints < 0 {
			capPoints = 1
		}
		points = make([]TrafficPoint, 0, capPoints)
		for bucket := fromSec; bucket <= lastBucket; bucket += bs {
			values := map[string]uint64{}
			rxVals := map[string]uint64{}
			txVals := map[string]uint64{}
			for _, key := range keys {
				values[key] = pointsByBucket[bucket][key]
				rxVals[key] = rxByBucket[bucket][key]
				txVals[key] = txByBucket[bucket][key]
			}
			points = append(points, TrafficPoint{
				Bucket:   time.Unix(bucket, 0).UTC(),
				Values:   values,
				RxValues: rxVals,
				TxValues: txVals,
			})
		}
	}

	breakdown := make(map[string]RxTxTotal, len(keys))
	for _, key := range keys {
		breakdown[key] = RxTxTotal{
			Rx:    rxTotals[key],
			Tx:    txTotals[key],
			Total: totals[key],
		}
	}

	return TrafficSeries{
		From:          query.From.UTC(),
		To:            query.To.UTC(),
		BucketSeconds: query.BucketSeconds,
		Keys:          keys,
		Totals:        totals,
		RxTotals:      rxTotals,
		TxTotals:      txTotals,
		Breakdown:     breakdown,
		Points:        points,
	}, nil
}

type DimensionQuery struct {
	Type   string
	SiteID string
	NodeID string
	From   time.Time
	To     time.Time
	Limit  int
	Sort   string
}

type DimensionStat struct {
	Type  string  `json:"type"`
	Key   string  `json:"key"`
	Rx    uint64  `json:"rx"`
	Tx    uint64  `json:"tx"`
	Total uint64  `json:"total"`
	Share float64 `json:"share"`
}

type DimensionResult struct {
	Type    string          `json:"type,omitempty"`
	From    time.Time       `json:"from"`
	To      time.Time       `json:"to"`
	TotalRx uint64          `json:"total_rx"`
	TotalTx uint64          `json:"total_tx"`
	Total   uint64          `json:"total"`
	Items   []DimensionStat `json:"items"`
}

func (s *Store) ListDimensionStats(ctx context.Context, query DimensionQuery) (DimensionResult, error) {
	if query.To.IsZero() {
		query.To = time.Now().UTC()
	}
	if query.From.IsZero() {
		query.From = query.To.Add(-72 * time.Hour)
	}
	if query.Type != "" && !validDimensionType(query.Type) {
		return DimensionResult{}, fmt.Errorf("%w: invalid dimension type %q", ErrInvalidInput, query.Type)
	}
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 500 {
		query.Limit = 500
	}
	switch query.Sort {
	case "", "total":
		query.Sort = "total"
	case "rx", "tx":
	default:
		return DimensionResult{}, fmt.Errorf("%w: invalid sort %q", ErrInvalidInput, query.Sort)
	}

	if query.To.Before(query.From) {
		return DimensionResult{
			Type:  query.Type,
			From:  query.From.UTC(),
			To:    query.To.UTC(),
			Items: []DimensionStat{},
		}, nil
	}

	var baseWhere []string
	var baseArgs []any
	if query.Type != "" {
		baseWhere = append(baseWhere, "dim_type = ?")
		baseArgs = append(baseArgs, query.Type)
	}
	if query.SiteID != "" {
		baseWhere = append(baseWhere, "site_id = ?")
		baseArgs = append(baseArgs, query.SiteID)
	}
	if query.NodeID != "" {
		baseWhere = append(baseWhere, "node_id = ?")
		baseArgs = append(baseArgs, query.NodeID)
	}
	extra := "1=1"
	if len(baseWhere) > 0 {
		extra = strings.Join(baseWhere, " AND ")
	}

	fromUnix := query.From.UTC().Unix()
	toUnix := query.To.UTC().Unix()
	fromDay := (fromUnix / daySeconds) * daySeconds

	var querySQL string
	var args []any
	if query.To.Sub(query.From) > range7d {
		querySQL = `SELECT dim_type, dim_key, SUM(rx), SUM(tx)
		 FROM dimension_daily
		 WHERE ` + extra + ` AND day >= ? AND day <= ?
		 GROUP BY dim_type, dim_key`
		args = append(args, baseArgs...)
		args = append(args, fromDay, toUnix)
	} else {
		querySQL = `SELECT dim_type, dim_key, SUM(rx), SUM(tx)
		 FROM (
			SELECT dim_type, dim_key, rx, tx
			FROM dimension_samples
			WHERE ` + extra + ` AND sampled_at >= ? AND sampled_at <= ?
			UNION ALL
			SELECT d.dim_type, d.dim_key, d.rx, d.tx
			FROM dimension_daily d
			WHERE ` + extra + ` AND d.day >= ? AND d.day <= ?
			  AND NOT EXISTS (
				SELECT 1 FROM dimension_samples s
				WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
			  )
		 )
		 GROUP BY dim_type, dim_key`
		args = append(args, baseArgs...)
		args = append(args, fromUnix, toUnix)
		args = append(args, baseArgs...)
		args = append(args, fromDay, toUnix, daySeconds)
	}

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return DimensionResult{}, err
	}
	defer rows.Close()

	var all []DimensionStat
	typeTotals := map[string]uint64{}
	var totalRx, totalTx uint64
	for rows.Next() {
		var dimType, dimKey string
		var rx, tx int64
		if err := rows.Scan(&dimType, &dimKey, &rx, &tx); err != nil {
			return DimensionResult{}, err
		}
		uRx, uTx := clampUint64(rx), clampUint64(tx)
		tot := uRx + uTx
		all = append(all, DimensionStat{
			Type:  dimType,
			Key:   dimKey,
			Rx:    uRx,
			Tx:    uTx,
			Total: tot,
		})
		typeTotals[dimType] += tot
		totalRx += uRx
		totalTx += uTx
	}
	if err := rows.Err(); err != nil {
		return DimensionResult{}, err
	}

	sort.Slice(all, func(i, j int) bool {
		var vi, vj uint64
		switch query.Sort {
		case "rx":
			vi, vj = all[i].Rx, all[j].Rx
		case "tx":
			vi, vj = all[i].Tx, all[j].Tx
		default:
			vi, vj = all[i].Total, all[j].Total
		}
		if vi != vj {
			return vi > vj
		}
		if all[i].Type != all[j].Type {
			return all[i].Type < all[j].Type
		}
		return all[i].Key < all[j].Key
	})

	if len(all) > query.Limit {
		all = all[:query.Limit]
	}
	for i := range all {
		denom := typeTotals[all[i].Type]
		if denom > 0 {
			all[i].Share = float64(all[i].Total) / float64(denom)
		}
	}
	if all == nil {
		all = []DimensionStat{}
	}

	return DimensionResult{
		Type:    query.Type,
		From:    query.From.UTC(),
		To:      query.To.UTC(),
		TotalRx: totalRx,
		TotalTx: totalTx,
		Total:   totalRx + totalTx,
		Items:   all,
	}, nil
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
