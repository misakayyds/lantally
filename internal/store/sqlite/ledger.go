package sqlite

import (
	"context"
	"database/sql"
	"math"

	"github.com/misakayyds/lantally/internal/accounting"
)

func (s *Store) ApplyLedgerOnce(
	ctx context.Context,
	siteID, nodeID, bootID string,
	seq uint64,
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
