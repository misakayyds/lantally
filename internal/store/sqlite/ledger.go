package sqlite

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
)

func (s *Store) ApplyLedgerOnce(
	ctx context.Context,
	siteID, nodeID, bootID string,
	seq uint64,
	sampledAt time.Time,
	increments []accounting.Increment,
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

	for _, item := range increments {
		if item.Rx == 0 && item.Tx == 0 {
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
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) LedgerTotals(ctx context.Context) (map[string]uint64, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT class, SUM(rx) + SUM(tx)
		 FROM ledger_totals
		 WHERE device_id = ''
		 GROUP BY class`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClassTotals(rows)
}

func (s *Store) NodeLedgerTotals(ctx context.Context, siteID, nodeID string) (map[string]uint64, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT class, SUM(rx) + SUM(tx)
		 FROM ledger_totals
		 WHERE site_id = ? AND node_id = ? AND device_id = ''
		 GROUP BY class`,
		siteID,
		nodeID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClassTotals(rows)
}

func (s *Store) ListDeviceLedgers(ctx context.Context) ([]DeviceLedger, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT d.id, d.site_id, IFNULL(d.display_name, ''), IFNULL(t.class, ''), IFNULL(t.rx, 0), IFNULL(t.tx, 0)
		 FROM devices d
		 LEFT JOIN ledger_totals t
		   ON t.device_id = d.id AND t.site_id = d.site_id
		 WHERE d.canonical_id IS NULL OR d.canonical_id = d.id
		 ORDER BY d.site_id, d.id, t.class`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	index := map[string]int{}
	var devices []DeviceLedger
	for rows.Next() {
		var id, siteID, name, class string
		var rx, tx int64
		if err := rows.Scan(&id, &siteID, &name, &class, &rx, &tx); err != nil {
			return nil, err
		}
		pos, ok := index[id]
		if !ok {
			pos = len(devices)
			index[id] = pos
			devices = append(devices, DeviceLedger{
				ID:     id,
				SiteID: siteID,
				Name:   name,
				Bytes:  map[string]uint64{},
			})
		}
		if class != "" {
			devices[pos].Bytes[class] += uint64(rx) + uint64(tx)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.attachDeviceEvidence(ctx, devices); err != nil {
		return nil, err
	}
	return devices, nil
}

func scanClassTotals(rows *sql.Rows) (map[string]uint64, error) {
	totals := map[string]uint64{
		accounting.ClassTotal:           0,
		accounting.ClassDirect:          0,
		accounting.ClassProxyRaw:        0,
		accounting.ClassProxyAdjusted:   0,
		accounting.ClassProxyUnadjusted: 0,
	}
	for rows.Next() {
		var class string
		var bytes int64
		if err := rows.Scan(&class, &bytes); err != nil {
			return nil, err
		}
		if bytes < 0 {
			bytes = 0
		}
		totals[class] = uint64(bytes)
	}
	return totals, rows.Err()
}

func deviceScope(group string) string {
	if group == "device" {
		return "1=1"
	}
	return "device_id = ''"
}

const (
	sampleRetention = 14 * 24 * time.Hour
	daySeconds      = 86400
	bucket30m       = 1800
	bucket2h        = 7200
	range72h        = 72 * time.Hour
	range7d         = 7 * 24 * time.Hour
)

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
	return s.PurgeSamples(ctx, now)
}

func (s *Store) RollupDaily(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO ledger_daily (day, site_id, node_id, device_id, class, outbound, rx, tx)
		 SELECT (sampled_at / ?) * ?, site_id, node_id, device_id, class, outbound, SUM(rx), SUM(tx)
		 FROM ledger_samples
		 GROUP BY 1, site_id, node_id, device_id, class, outbound
		 ON CONFLICT(day, site_id, node_id, device_id, class, outbound)
		 DO UPDATE SET rx = excluded.rx, tx = excluded.tx`,
		daySeconds,
		daySeconds,
	)
	return err
}

func (s *Store) PurgeSamples(ctx context.Context, now time.Time) error {
	cutoff := now.UTC().Add(-sampleRetention).Unix()
	_, err := s.db.ExecContext(ctx, `DELETE FROM ledger_samples WHERE sampled_at < ?`, cutoff)
	return err
}

type TrafficQuery struct {
	From          time.Time
	To            time.Time
	BucketSeconds int
	Class         string
	Group         string
	NodeID        string
	DeviceID      string
}

type TrafficPoint struct {
	Bucket time.Time         `json:"t"`
	Values map[string]uint64 `json:"values"`
}

type TrafficSeries struct {
	BucketSeconds int               `json:"bucket_seconds"`
	Keys          []string          `json:"keys"`
	Totals        map[string]uint64 `json:"totals"`
	Points        []TrafficPoint    `json:"points"`
}

func (s *Store) TrafficSeries(ctx context.Context, query TrafficQuery) (TrafficSeries, error) {
	if query.To.IsZero() {
		query.To = time.Now().UTC()
	}
	if query.From.IsZero() {
		query.From = query.To.Add(-72 * time.Hour)
	}
	if query.BucketSeconds <= 0 {
		query.BucketSeconds = DefaultBucketSeconds(query.From, query.To)
	}
	if query.Group == "" {
		query.Group = "node"
	}
	if query.Class == "" {
		query.Class = accounting.ClassTotal
	}

	table := "ledger_samples"
	timeCol := "sampled_at"
	if query.To.Sub(query.From) > range7d {
		table = "ledger_daily"
		timeCol = "day"
	}

	keyExpr := "node_id"
	whereClass := `class = ?`
	fromUnix := query.From.UTC().Unix()
	toUnix := query.To.UTC().Unix()
	args := []any{query.BucketSeconds, query.BucketSeconds, query.Class}
	switch query.Group {
	case "class":
		keyExpr = "class"
		whereClass = `class IN ('direct', 'proxy_raw', 'proxy_adjusted', 'proxy_unadjusted')`
		args = []any{query.BucketSeconds, query.BucketSeconds}
	case "outbound":
		keyExpr = "outbound"
		whereClass = `class = ? AND outbound != ''`
	case "device":
		keyExpr = "device_id"
		whereClass = `class = ? AND device_id != ''`
	}

	var filters []string
	if query.NodeID != "" {
		filters = append(filters, "node_id = ?")
		args = append(args, query.NodeID)
	}
	if query.DeviceID != "" {
		filters = append(filters, "device_id = ?")
		args = append(args, query.DeviceID)
	}
	extra := "1=1"
	if len(filters) > 0 {
		extra = strings.Join(filters, " AND ")
	}
	args = append(args, fromUnix, toUnix)

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT (`+timeCol+` / ?) * ? AS bucket, `+keyExpr+`, SUM(rx + tx)
		 FROM `+table+`
		 WHERE `+deviceScope(query.Group)+` AND `+whereClass+` AND `+extra+`
		   AND `+timeCol+` >= ? AND `+timeCol+` <= ?
		 GROUP BY bucket, `+keyExpr+`
		 ORDER BY bucket, `+keyExpr,
		args...,
	)
	if err != nil {
		return TrafficSeries{}, err
	}
	defer rows.Close()

	pointsByBucket := map[int64]map[string]uint64{}
	totals := map[string]uint64{}
	keySet := map[string]struct{}{}

	for rows.Next() {
		var bucket int64
		var key string
		var bytes int64
		if err := rows.Scan(&bucket, &key, &bytes); err != nil {
			return TrafficSeries{}, err
		}
		if bytes < 0 {
			bytes = 0
		}
		if pointsByBucket[bucket] == nil {
			pointsByBucket[bucket] = map[string]uint64{}
		}
		pointsByBucket[bucket][key] += uint64(bytes)
		totals[key] += uint64(bytes)
		keySet[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return TrafficSeries{}, err
	}

	fromSec := query.From.UTC().Unix() / int64(query.BucketSeconds) * int64(query.BucketSeconds)
	toSec := query.To.UTC().Unix()
	keys := sortedKeys(keySet)
	points := make([]TrafficPoint, 0, int((toSec-fromSec)/int64(query.BucketSeconds))+1)
	for bucket := fromSec; bucket < toSec; bucket += int64(query.BucketSeconds) {
		values := map[string]uint64{}
		for _, key := range keys {
			values[key] = pointsByBucket[bucket][key]
		}
		points = append(points, TrafficPoint{
			Bucket: time.Unix(bucket, 0).UTC(),
			Values: values,
		})
	}

	return TrafficSeries{
		BucketSeconds: query.BucketSeconds,
		Keys:          keys,
		Totals:        totals,
		Points:        points,
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
