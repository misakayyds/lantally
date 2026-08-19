package sqlite

import (
	"context"
	"database/sql"
	"math"
	"sort"
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
			`INSERT INTO ledger_totals (site_id, node_id, device_id, class, rx, tx)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(site_id, node_id, device_id, class)
			 DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx`,
			siteID,
			nodeID,
			item.DeviceID,
			item.Class,
			int64(item.Rx),
			int64(item.Tx),
		)
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO ledger_samples (sampled_at, site_id, node_id, device_id, class, rx, tx)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sampledUnix,
			siteID,
			nodeID,
			item.DeviceID,
			item.Class,
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
		`SELECT class, rx + tx
		 FROM ledger_totals
		 WHERE site_id = ? AND node_id = ? AND device_id = ''`,
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
		`SELECT d.id, d.site_id, IFNULL(t.class, ''), IFNULL(t.rx, 0), IFNULL(t.tx, 0)
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
		var id, siteID, class string
		var rx, tx int64
		if err := rows.Scan(&id, &siteID, &class, &rx, &tx); err != nil {
			return nil, err
		}
		pos, ok := index[id]
		if !ok {
			pos = len(devices)
			index[id] = pos
			devices = append(devices, DeviceLedger{
				ID:     id,
				SiteID: siteID,
				Bytes:  map[string]uint64{},
			})
		}
		if class != "" {
			devices[pos].Bytes[class] += uint64(rx) + uint64(tx)
		}
	}
	return devices, rows.Err()
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

type TrafficQuery struct {
	From          time.Time
	To            time.Time
	BucketSeconds int
	Class         string
	Group         string
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
	if query.BucketSeconds <= 0 {
		query.BucketSeconds = 1800
	}
	if query.Group == "" {
		query.Group = "node"
	}
	if query.Class == "" {
		query.Class = accounting.ClassTotal
	}
	if query.To.IsZero() {
		query.To = time.Now().UTC()
	}
	if query.From.IsZero() {
		query.From = query.To.Add(-72 * time.Hour)
	}

	keyExpr := "node_id"
	whereClass := `class = ?`
	fromUnix := query.From.UTC().Unix()
	toUnix := query.To.UTC().Unix()
	args := []any{query.BucketSeconds, query.BucketSeconds, query.Class, fromUnix, toUnix}
	if query.Group == "class" {
		keyExpr = "class"
		whereClass = `class IN ('direct', 'proxy_raw', 'proxy_adjusted', 'proxy_unadjusted')`
		args = []any{query.BucketSeconds, query.BucketSeconds, fromUnix, toUnix}
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT (sampled_at / ?) * ? AS bucket, `+keyExpr+`, SUM(rx + tx)
		 FROM ledger_samples
		 WHERE device_id = '' AND `+whereClass+`
		   AND sampled_at >= ? AND sampled_at < ?
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
