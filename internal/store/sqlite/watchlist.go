package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
)

type WatchPoint struct {
	Bucket     time.Time `json:"t"`
	Rx         uint64    `json:"rx"`
	Tx         uint64    `json:"tx"`
	Total      uint64    `json:"total"`
	Adjusted   uint64    `json:"adjusted"`
	AdjustedRx uint64    `json:"adjusted_rx"`
	AdjustedTx uint64    `json:"adjusted_tx"`
}

type WatchItemStatus struct {
	accounting.WatchItem
	Rx              uint64       `json:"rx"`
	Tx              uint64       `json:"tx"`
	Total           uint64       `json:"total"`
	Adjusted        uint64       `json:"adjusted"`
	AdjustedRx      uint64       `json:"adjusted_rx"`
	AdjustedTx      uint64       `json:"adjusted_tx"`
	UsedBytes       uint64       `json:"used_bytes"`
	UsedRatio       float64      `json:"used_ratio"`
	DaysRemaining   *int         `json:"days_remaining"`
	OverQuota       bool         `json:"over_quota"`
	Expired         bool         `json:"expired"`
	OverlapPossible bool         `json:"overlap_possible,omitempty"`
	CycleFrom       time.Time    `json:"cycle_from,omitempty"`
	CycleTo         time.Time    `json:"cycle_to,omitempty"`
	From            time.Time    `json:"from"`
	To              time.Time    `json:"to"`
	BucketSeconds   int          `json:"bucket_seconds"`
	Series          []WatchPoint `json:"series"`
}

func (s *Store) ListWatchItems(ctx context.Context) ([]accounting.WatchItem, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, matchers_json, override_factor, quota_value, quota_bytes, quota_unit,
		        reset_type, reset_day, expires_at, pinned, enabled, created_at, updated_at
		 FROM watch_items
		 ORDER BY pinned DESC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []accounting.WatchItem{}
	for rows.Next() {
		item, err := scanWatchItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetWatchItem(ctx context.Context, id int64) (accounting.WatchItem, error) {
	if id <= 0 {
		return accounting.WatchItem{}, fmt.Errorf("%w: invalid watch item id", ErrInvalidInput)
	}
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, name, matchers_json, override_factor, quota_value, quota_bytes, quota_unit,
		        reset_type, reset_day, expires_at, pinned, enabled, created_at, updated_at
		 FROM watch_items
		 WHERE id = ?`,
		id,
	)
	item, err := scanWatchItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return accounting.WatchItem{}, sql.ErrNoRows
	}
	if err != nil {
		return accounting.WatchItem{}, err
	}
	return item, nil
}

func (s *Store) CreateWatchItem(ctx context.Context, item accounting.WatchItem) (accounting.WatchItem, error) {
	if err := accounting.ValidateWatchItem(&item); err != nil {
		return accounting.WatchItem{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	matchersJSON, err := json.Marshal(item.Matchers)
	if err != nil {
		return accounting.WatchItem{}, fmt.Errorf("%w: invalid matchers", ErrInvalidInput)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	item.CreatedAt = now
	item.UpdatedAt = now

	var overrideArg any
	if item.OverrideFactor != nil {
		overrideArg = *item.OverrideFactor
	}
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO watch_items (
			name, matchers_json, override_factor, quota_value, quota_bytes, quota_unit,
			reset_type, reset_day, expires_at, pinned, enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Name,
		string(matchersJSON),
		overrideArg,
		item.QuotaValue,
		int64(item.QuotaBytes),
		item.QuotaUnit,
		string(item.ResetType),
		item.ResetDay,
		item.ExpiresAt,
		boolToInt(item.Pinned),
		boolToInt(item.Enabled),
		item.CreatedAt,
		item.UpdatedAt,
	)
	if err != nil {
		return accounting.WatchItem{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return accounting.WatchItem{}, err
	}
	item.ID = id
	return item, nil
}

func (s *Store) UpdateWatchItem(ctx context.Context, id int64, item accounting.WatchItem) (accounting.WatchItem, error) {
	if id <= 0 {
		return accounting.WatchItem{}, fmt.Errorf("%w: invalid watch item id", ErrInvalidInput)
	}
	if err := accounting.ValidateWatchItem(&item); err != nil {
		return accounting.WatchItem{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	matchersJSON, err := json.Marshal(item.Matchers)
	if err != nil {
		return accounting.WatchItem{}, fmt.Errorf("%w: invalid matchers", ErrInvalidInput)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var overrideArg any
	if item.OverrideFactor != nil {
		overrideArg = *item.OverrideFactor
	}
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE watch_items
		 SET name = ?, matchers_json = ?, override_factor = ?, quota_value = ?, quota_bytes = ?,
		     quota_unit = ?, reset_type = ?, reset_day = ?, expires_at = ?, pinned = ?, enabled = ?, updated_at = ?
		 WHERE id = ?`,
		item.Name,
		string(matchersJSON),
		overrideArg,
		item.QuotaValue,
		int64(item.QuotaBytes),
		item.QuotaUnit,
		string(item.ResetType),
		item.ResetDay,
		item.ExpiresAt,
		boolToInt(item.Pinned),
		boolToInt(item.Enabled),
		now,
		id,
	)
	if err != nil {
		return accounting.WatchItem{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return accounting.WatchItem{}, err
	}
	if n == 0 {
		return accounting.WatchItem{}, sql.ErrNoRows
	}
	return s.GetWatchItem(ctx, id)
}

func (s *Store) DeleteWatchItem(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: invalid watch item id", ErrInvalidInput)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM watch_items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	_, _ = s.db.ExecContext(
		ctx,
		`UPDATE alerts SET resolved_at = ? WHERE fingerprint LIKE ? AND resolved_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano),
		fmt.Sprintf("quota|watch|%d|%%", id),
	)
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWatchItem(sc rowScanner) (accounting.WatchItem, error) {
	var (
		item           accounting.WatchItem
		matchersRaw    string
		overrideFactor sql.NullFloat64
		quotaBytes     int64
		resetType      string
		pinnedInt      int
		enabledInt     int
	)
	if err := sc.Scan(
		&item.ID,
		&item.Name,
		&matchersRaw,
		&overrideFactor,
		&item.QuotaValue,
		&quotaBytes,
		&item.QuotaUnit,
		&resetType,
		&item.ResetDay,
		&item.ExpiresAt,
		&pinnedInt,
		&enabledInt,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return accounting.WatchItem{}, err
	}
	if overrideFactor.Valid {
		f := overrideFactor.Float64
		item.OverrideFactor = &f
	}
	if quotaBytes > 0 {
		item.QuotaBytes = uint64(quotaBytes)
	}
	if item.QuotaValue == 0 && item.QuotaBytes > 0 && item.QuotaUnit != "" {
		if v, err := accounting.BytesToUnit(item.QuotaBytes, item.QuotaUnit); err == nil {
			item.QuotaValue = v
		}
	}
	item.ResetType = accounting.ResetType(resetType)
	item.Pinned = pinnedInt != 0
	item.Enabled = enabledInt != 0
	item.Matchers = accounting.WatchMatcherMap{}
	if strings.TrimSpace(matchersRaw) != "" {
		_ = json.Unmarshal([]byte(matchersRaw), &item.Matchers)
	}
	return item, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type watchBucketVals struct {
	rx, tx, adjRx, adjTx uint64
}

func watchCycleWindow(
	item accounting.WatchItem,
	now time.Time,
	loc *time.Location,
) (from, to time.Time, daysRemaining *int, expired bool, cycleKey string) {
	now = now.UTC()
	if loc == nil {
		loc = time.UTC
	}
	switch item.ResetType {
	case accounting.ResetMonthly:
		from, to = BillingPeriodInLoc(now, item.ResetDay, loc)
		nextReset := from.In(loc).AddDate(0, 1, 0)
		days := int(math.Ceil(nextReset.Sub(now).Hours() / 24.0))
		if days < 0 {
			days = 0
		}
		return from, to, &days, false, strconv.FormatInt(from.Unix(), 10)
	case accounting.ResetFixed, accounting.ResetFixedDate:
		cycleKey = "fixed"
	default:
		cycleKey = "none"
	}
	from = time.Time{}
	to = now
	if item.ExpiresAt != "" {
		if exp, err := accounting.ParseExpiresAt(item.ExpiresAt); err == nil && !exp.IsZero() {
			if !exp.After(now) {
				to = exp
				expired = true
				d := 0
				daysRemaining = &d
			} else {
				d := int(math.Ceil(exp.Sub(now).Hours() / 24.0))
				daysRemaining = &d
			}
		}
	}
	return from, to, daysRemaining, expired, cycleKey
}

func (s *Store) EvaluateWatchItem(
	ctx context.Context,
	item accounting.WatchItem,
	from, to time.Time,
	bucketSeconds int,
	loc *time.Location,
	now time.Time,
) (WatchItemStatus, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return WatchItemStatus{}, err
	}
	return s.evaluateWatchItemWithRules(ctx, item, from, to, bucketSeconds, loc, now, rules)
}

func (s *Store) EvaluateWatchItems(
	ctx context.Context,
	from, to time.Time,
	bucketSeconds int,
	loc *time.Location,
	now time.Time,
) ([]WatchItemStatus, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	items, err := s.ListWatchItems(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]WatchItemStatus, 0, len(items))
	for _, item := range items {
		st, err := s.evaluateWatchItemWithRules(ctx, item, from, to, bucketSeconds, loc, now, rules)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}

func (s *Store) ListPinnedWatchStatuses(ctx context.Context, now time.Time, loc *time.Location) ([]WatchItemStatus, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	items, err := s.ListWatchItems(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return nil, err
	}
	out := []WatchItemStatus{}
	for _, item := range items {
		if !item.Pinned {
			continue
		}
		st, err := s.evaluateWatchItemWithRules(ctx, item, time.Time{}, time.Time{}, 0, loc, now, rules)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func (s *Store) evaluateWatchItemWithRules(
	ctx context.Context,
	item accounting.WatchItem,
	reqFrom, reqTo time.Time,
	bucketSeconds int,
	loc *time.Location,
	now time.Time,
	rules *accounting.CompiledRuleSet,
) (WatchItemStatus, error) {
	compiled, err := accounting.CompileWatchItem(item)
	if err != nil {
		return WatchItemStatus{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	cycleFrom, cycleTo, daysRemaining, expired, _ := watchCycleWindow(compiled.Item, now, loc)

	explicitRange := !reqFrom.IsZero() || !reqTo.IsZero()
	evalFrom, evalTo := cycleFrom, cycleTo
	if explicitRange {
		evalFrom, evalTo = reqFrom.UTC(), reqTo.UTC()
		if evalTo.IsZero() {
			evalTo = now.UTC()
		}
	}

	seriesFrom, seriesTo, bs, err := s.resolveWatchSeriesWindow(ctx, evalFrom, evalTo, bucketSeconds)
	if err != nil {
		return WatchItemStatus{}, err
	}

	combined, overlapPossible, err := s.evalWatchBuckets(ctx, compiled, evalFrom, evalTo, seriesFrom, seriesTo, bs, rules)
	if err != nil {
		return WatchItemStatus{}, err
	}

	fromSec := (seriesFrom.UTC().Unix() / int64(bs)) * int64(bs)
	lastBucket := (seriesTo.UTC().Unix() / int64(bs)) * int64(bs)
	var series []WatchPoint
	var totalRx, totalTx, totalAdjRx, totalAdjTx uint64

	if seriesTo.Before(seriesFrom) {
		series = []WatchPoint{}
	} else {
		capPts := int((lastBucket-fromSec)/int64(bs)) + 1
		if capPts < 0 {
			capPts = 1
		}
		series = make([]WatchPoint, 0, capPts)
		for b := fromSec; b <= lastBucket; b += int64(bs) {
			v := combined[b]
			series = append(series, WatchPoint{
				Bucket:     time.Unix(b, 0).UTC(),
				Rx:         v.rx,
				Tx:         v.tx,
				Total:      v.rx + v.tx,
				Adjusted:   v.adjRx + v.adjTx,
				AdjustedRx: v.adjRx,
				AdjustedTx: v.adjTx,
			})
			totalRx += v.rx
			totalTx += v.tx
			totalAdjRx += v.adjRx
			totalAdjTx += v.adjTx
		}
	}

	usedBytes := totalAdjRx + totalAdjTx
	if explicitRange && (evalFrom != cycleFrom || evalTo != cycleTo) {
		cycleFromSec, cycleToSec, cycleBS, err := s.resolveWatchSeriesWindow(ctx, cycleFrom, cycleTo, 0)
		if err != nil {
			return WatchItemStatus{}, err
		}
		cycleBuckets, _, err := s.evalWatchBuckets(ctx, compiled, cycleFrom, cycleTo, cycleFromSec, cycleToSec, cycleBS, rules)
		if err != nil {
			return WatchItemStatus{}, err
		}
		usedBytes = 0
		for _, v := range cycleBuckets {
			usedBytes += v.adjRx + v.adjTx
		}
	}

	var usedRatio float64
	var overQuota bool
	if compiled.Item.QuotaBytes > 0 {
		usedRatio = float64(usedBytes) / float64(compiled.Item.QuotaBytes)
		if math.IsNaN(usedRatio) || math.IsInf(usedRatio, 0) {
			usedRatio = 0
		}
		overQuota = usedBytes > compiled.Item.QuotaBytes
	}

	return WatchItemStatus{
		WatchItem:       compiled.Item,
		Rx:              totalRx,
		Tx:              totalTx,
		Total:           totalRx + totalTx,
		Adjusted:        totalAdjRx + totalAdjTx,
		AdjustedRx:      totalAdjRx,
		AdjustedTx:      totalAdjTx,
		UsedBytes:       usedBytes,
		UsedRatio:       usedRatio,
		DaysRemaining:   daysRemaining,
		OverQuota:       overQuota,
		Expired:         expired,
		OverlapPossible: overlapPossible,
		CycleFrom:       cycleFrom,
		CycleTo:         cycleTo,
		From:            seriesFrom,
		To:              seriesTo,
		BucketSeconds:   bs,
		Series:          series,
	}, nil
}

func (s *Store) resolveWatchSeriesWindow(
	ctx context.Context,
	evalFrom, evalTo time.Time,
	requestedBS int,
) (seriesFrom, seriesTo time.Time, bs int, err error) {
	seriesTo = evalTo.UTC()
	if seriesTo.IsZero() {
		seriesTo = time.Now().UTC()
	}

	if !evalFrom.IsZero() {
		seriesFrom = evalFrom.UTC()
		if requestedBS > 0 {
			return seriesFrom, seriesTo, requestedBS, nil
		}
		bs = DefaultBucketSeconds(seriesFrom, seriesTo)
		if !seriesTo.Before(seriesFrom) && seriesTo.Sub(seriesFrom) <= range7d {
			lHasS, lHasD, err := s.hasUnrepresentedDaily(ctx, "ledger_samples", "ledger_daily", seriesFrom, seriesTo)
			if err != nil {
				return time.Time{}, time.Time{}, 0, err
			}
			dHasS, dHasD, err := s.hasUnrepresentedDaily(ctx, "dimension_samples", "dimension_daily", seriesFrom, seriesTo)
			if err != nil {
				return time.Time{}, time.Time{}, 0, err
			}
			if !(lHasS || dHasS) && (lHasD || dHasD) && bs < daySeconds {
				bs = daySeconds
			}
		}
		return seriesFrom, seriesTo, bs, nil
	}

	// All-time window (evalFrom.IsZero()): inspect earliest timestamp up to seriesTo
	// across ledger_* and dimension_* so >14d purged daily history is represented
	// without allocating an unbounded bucket slice.
	toUnix := seriesTo.Unix()
	var minTS int64
	queries := []struct {
		sql  string
		args []any
	}{
		{`SELECT IFNULL(MIN(sampled_at), 0) FROM ledger_samples WHERE sampled_at <= ?`, []any{toUnix}},
		{`SELECT IFNULL(MIN(day), 0) FROM ledger_daily WHERE day <= ?`, []any{toUnix}},
		{`SELECT IFNULL(MIN(sampled_at), 0) FROM dimension_samples WHERE sampled_at <= ?`, []any{toUnix}},
		{`SELECT IFNULL(MIN(day), 0) FROM dimension_daily WHERE day <= ?`, []any{toUnix}},
	}
	for _, q := range queries {
		var ts int64
		if err := s.db.QueryRowContext(ctx, q.sql, q.args...).Scan(&ts); err != nil {
			return time.Time{}, time.Time{}, 0, err
		}
		if ts > 0 && (minTS == 0 || ts < minTS) {
			minTS = ts
		}
	}

	if minTS == 0 {
		seriesFrom = seriesTo.Add(-72 * time.Hour)
		if requestedBS > 0 {
			return seriesFrom, seriesTo, requestedBS, nil
		}
		return seriesFrom, seriesTo, bucket30m, nil
	}

	earliest := time.Unix(minTS, 0).UTC()
	if seriesTo.Sub(earliest) > range7d {
		seriesFrom = time.Unix((minTS/daySeconds)*daySeconds, 0).UTC()
	} else {
		seriesFrom = seriesTo.Add(-72 * time.Hour)
		if earliest.Before(seriesFrom) {
			seriesFrom = earliest
		}
	}

	if requestedBS > 0 {
		return seriesFrom, seriesTo, requestedBS, nil
	}
	bs = DefaultBucketSeconds(seriesFrom, seriesTo)
	if seriesTo.Sub(seriesFrom) <= range7d {
		lHasS, lHasD, err := s.hasUnrepresentedDaily(ctx, "ledger_samples", "ledger_daily", seriesFrom, seriesTo)
		if err != nil {
			return time.Time{}, time.Time{}, 0, err
		}
		dHasS, dHasD, err := s.hasUnrepresentedDaily(ctx, "dimension_samples", "dimension_daily", seriesFrom, seriesTo)
		if err != nil {
			return time.Time{}, time.Time{}, 0, err
		}
		if !(lHasS || dHasS) && (lHasD || dHasD) && bs < daySeconds {
			bs = daySeconds
		}
	}
	return seriesFrom, seriesTo, bs, nil
}

// evalWatchBuckets implements orthogonal multi-dimensional watchlist evaluation:
//   - Intra-dimension Values use OR (each matching key counted at most once).
//   - `node` acts as an AND scope filter (`node_id IN (...)`) across all queries.
//   - `device` + `outbound` evaluates as an exact SQL AND on `ledger_*`.
//   - Multiple orthogonal non-`node` components (e.g. `group` + `process`) are
//     evaluated independently per bucket using M2's lossless tiering
//     (`>7d -> *_daily`, `<=7d -> *_samples UNION ALL unrepresented *_daily`,
//     all-time -> `*_samples UNION ALL unrepresented *_daily`) and combined
//     per bucket via min(rx_i), min(tx_i), min(adj_rx_i), min(adj_tx_i) so
//     orthogonal dimensions never double-count and status totals equal sum(series).
func (s *Store) evalWatchBuckets(
	ctx context.Context,
	compiled *accounting.CompiledWatchItem,
	evalFrom, evalTo, seriesFrom, seriesTo time.Time,
	bs int,
	rules *accounting.CompiledRuleSet,
) (combined map[int64]watchBucketVals, overlapPossible bool, err error) {
	if evalTo.Before(evalFrom) {
		return map[int64]watchBucketVals{}, false, nil
	}
	fromSec := (seriesFrom.UTC().Unix() / int64(bs)) * int64(bs)
	lastBucket := (seriesTo.UTC().Unix() / int64(bs)) * int64(bs)
	clampBucket := func(b int64) int64 {
		if b < fromSec {
			return fromSec
		}
		if b > lastBucket {
			return lastBucket
		}
		return b
	}

	var nodeFilterSQL string
	var nodeArgs []any
	if nodeMatcher, hasNode := compiled.Matchers[accounting.WatchDimNode]; hasNode {
		matchedNodes, err := s.matchWatchNodes(ctx, nodeMatcher)
		if err != nil {
			return nil, false, err
		}
		if len(matchedNodes) == 0 {
			return map[int64]watchBucketVals{}, false, nil
		}
		nodeFilterSQL, nodeArgs = sqlInClause("node_id", matchedNodes)
	}

	_, hasDev := compiled.Matchers[accounting.WatchDimDevice]
	_, hasOut := compiled.Matchers[accounting.WatchDimOutbound]
	hasDim := false
	for _, dim := range []accounting.WatchDimension{
		accounting.WatchDimIface,
		accounting.WatchDimGroup,
		accounting.WatchDimProcess,
		accounting.WatchDimDomain,
	} {
		if _, ok := compiled.Matchers[dim]; ok {
			hasDim = true
			break
		}
	}

	var components []map[int64]watchBucketVals

	// Ledger component: device+outbound, device-only, outbound-only, or node-only.
	if hasDev || hasOut || (!hasDim && compiled.Matchers[accounting.WatchDimNode] != nil) {
		comp, err := s.evalLedgerWatchComponent(
			ctx, compiled, hasDev, hasOut, nodeFilterSQL, nodeArgs,
			evalFrom, evalTo, bs, clampBucket, rules,
		)
		if err != nil {
			return nil, false, err
		}
		components = append(components, comp)
	}

	// Orthogonal dimension components: iface, group, process, domain.
	for _, dim := range []accounting.WatchDimension{
		accounting.WatchDimIface,
		accounting.WatchDimGroup,
		accounting.WatchDimProcess,
		accounting.WatchDimDomain,
	} {
		matcher, ok := compiled.Matchers[dim]
		if !ok {
			continue
		}
		comp, err := s.evalDimensionWatchComponent(
			ctx, string(dim), matcher, compiled.Item.OverrideFactor,
			nodeFilterSQL, nodeArgs, evalFrom, evalTo, bs, clampBucket,
		)
		if err != nil {
			return nil, false, err
		}
		components = append(components, comp)
	}

	if len(components) == 0 {
		return map[int64]watchBucketVals{}, false, nil
	}
	if len(components) == 1 {
		return components[0], false, nil
	}

	hasLedgerOutbound := hasOut
	overlapPossible = true
	combined = make(map[int64]watchBucketVals, len(components[0]))
	for b, first := range components[0] {
		minVal := first
		presentAll := true
		for _, comp := range components[1:] {
			other, ok := comp[b]
			if !ok {
				presentAll = false
				break
			}
			if other.rx < minVal.rx {
				minVal.rx = other.rx
			}
			if other.tx < minVal.tx {
				minVal.tx = other.tx
			}
			if other.adjRx < minVal.adjRx {
				minVal.adjRx = other.adjRx
			}
			if other.adjTx < minVal.adjTx {
				minVal.adjTx = other.adjTx
			}
		}
		if presentAll {
			if hasLedgerOutbound && compiled.Item.OverrideFactor == nil {
				if minVal.rx == 0 || first.rx == 0 {
					minVal.adjRx = 0
				} else if minVal.rx == first.rx {
					minVal.adjRx = first.adjRx
				} else {
					minVal.adjRx = accounting.ScaleBytes(minVal.rx, float64(first.adjRx)/float64(first.rx))
				}
				if minVal.tx == 0 || first.tx == 0 {
					minVal.adjTx = 0
				} else if minVal.tx == first.tx {
					minVal.adjTx = first.adjTx
				} else {
					minVal.adjTx = accounting.ScaleBytes(minVal.tx, float64(first.adjTx)/float64(first.tx))
				}
			}
			if minVal.rx+minVal.tx+minVal.adjRx+minVal.adjTx > 0 {
				combined[b] = minVal
			}
		}
	}
	return combined, overlapPossible, nil
}

func (s *Store) evalLedgerWatchComponent(
	ctx context.Context,
	compiled *accounting.CompiledWatchItem,
	hasDev, hasOut bool,
	nodeFilterSQL string,
	nodeArgs []any,
	evalFrom, evalTo time.Time,
	bs int,
	clampBucket func(int64) int64,
	rules *accounting.CompiledRuleSet,
) (map[int64]watchBucketVals, error) {
	out := map[int64]watchBucketVals{}

	var extraFilters []string
	var extraArgs []any
	if nodeFilterSQL != "" {
		extraFilters = append(extraFilters, nodeFilterSQL)
		extraArgs = append(extraArgs, nodeArgs...)
	}

	if hasDev {
		devIDs, err := s.matchWatchDevices(ctx, compiled.Matchers[accounting.WatchDimDevice])
		if err != nil {
			return nil, err
		}
		if len(devIDs) == 0 {
			return out, nil
		}
		clause, args := sqlInClause("device_id", devIDs)
		extraFilters = append(extraFilters, clause)
		extraArgs = append(extraArgs, args...)
	} else {
		extraFilters = append(extraFilters, "device_id = ''")
	}

	if hasOut {
		extraFilters = append(extraFilters, "class IN (?, ?)")
		extraArgs = append(extraArgs, accounting.ClassProxyRaw, accounting.ClassDirect)
	}

	baseWhere := strings.Join(extraFilters, " AND ")
	groupCols := "bucket, class, outbound"
	selectCols := "class, outbound"
	if hasDev && !hasOut {
		groupCols = "bucket, device_id, class, outbound"
		selectCols = "device_id, class, outbound"
	}

	querySQL, queryArgs := buildTieredLedgerQuery(evalFrom, evalTo, bs, baseWhere, extraArgs, selectCols, groupCols)
	rows, err := s.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	override := compiled.Item.OverrideFactor

	if hasOut {
		outMatcher := compiled.Matchers[accounting.WatchDimOutbound]
		for rows.Next() {
			var bucket int64
			var class, outbound string
			var rx, tx int64
			if err := rows.Scan(&bucket, &class, &outbound, &rx, &tx); err != nil {
				return nil, err
			}
			uRx, uTx := clampUint64(rx), clampUint64(tx)
			if uRx == 0 && uTx == 0 {
				continue
			}
			b := clampBucket(bucket)
			switch class {
			case accounting.ClassProxyRaw:
				if outbound == "" || !outMatcher.Match(outbound) {
					continue
				}
				factor := rules.Match(outbound).Factor
				if override != nil {
					factor = *override
				}
				cur := out[b]
				cur.rx += uRx
				cur.tx += uTx
				cur.adjRx += accounting.ScaleBytes(uRx, factor)
				cur.adjTx += accounting.ScaleBytes(uTx, factor)
				out[b] = cur
			case accounting.ClassDirect:
				if !outMatcher.Match("DIRECT") {
					continue
				}
				factor := 1.0
				if override != nil {
					factor = *override
				}
				cur := out[b]
				cur.rx += uRx
				cur.tx += uTx
				cur.adjRx += accounting.ScaleBytes(uRx, factor)
				cur.adjTx += accounting.ScaleBytes(uTx, factor)
				out[b] = cur
			}
		}
		return out, rows.Err()
	}

	// Node-only or device-only: compute per (bucket, entity) total and adjusted proxy delta.
	type entityAccum struct {
		totRx, totTx       uint64
		dirRx, dirTx       uint64
		rawRx, rawTx       uint64
		proxyAdjRx         uint64
		proxyAdjTx         uint64
		legAdjRx, legAdjTx uint64
	}
	type bucketEntityKey struct {
		bucket int64
		entity string
	}
	perEntity := map[bucketEntityKey]*entityAccum{}

	for rows.Next() {
		var bucket int64
		var entity, class, outbound string
		var rx, tx int64
		if hasDev {
			if err := rows.Scan(&bucket, &entity, &class, &outbound, &rx, &tx); err != nil {
				return nil, err
			}
		} else {
			if err := rows.Scan(&bucket, &class, &outbound, &rx, &tx); err != nil {
				return nil, err
			}
		}
		uRx, uTx := clampUint64(rx), clampUint64(tx)
		k := bucketEntityKey{bucket: clampBucket(bucket), entity: entity}
		acc := perEntity[k]
		if acc == nil {
			acc = &entityAccum{}
			perEntity[k] = acc
		}
		switch class {
		case accounting.ClassTotal:
			acc.totRx += uRx
			acc.totTx += uTx
		case accounting.ClassDirect:
			acc.dirRx += uRx
			acc.dirTx += uTx
		case accounting.ClassProxyRaw:
			acc.rawRx += uRx
			acc.rawTx += uTx
			factor := rules.Match(outbound).Factor
			acc.proxyAdjRx += accounting.ScaleBytes(uRx, factor)
			acc.proxyAdjTx += accounting.ScaleBytes(uTx, factor)
		case accounting.ClassProxyAdjusted:
			acc.legAdjRx += uRx
			acc.legAdjTx += uTx
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for k, acc := range perEntity {
		var rx, tx, adjRx, adjTx uint64
		if acc.totRx+acc.totTx > 0 {
			rx, tx = acc.totRx, acc.totTx
		} else {
			rx, tx = acc.dirRx+acc.rawRx, acc.dirTx+acc.rawTx
		}
		if override != nil {
			adjRx = accounting.ScaleBytes(rx, *override)
			adjTx = accounting.ScaleBytes(tx, *override)
		} else if acc.totRx+acc.totTx > 0 {
			pAdjRx, pAdjTx := acc.proxyAdjRx, acc.proxyAdjTx
			if acc.rawRx+acc.rawTx == 0 && acc.legAdjRx+acc.legAdjTx > 0 {
				pAdjRx, pAdjTx = acc.legAdjRx, acc.legAdjTx
			}
			nonProxyRx := clampSub(acc.totRx, acc.rawRx)
			nonProxyTx := clampSub(acc.totTx, acc.rawTx)
			adjRx = nonProxyRx + pAdjRx
			adjTx = nonProxyTx + pAdjTx
		} else {
			pAdjRx, pAdjTx := acc.proxyAdjRx, acc.proxyAdjTx
			if acc.rawRx+acc.rawTx == 0 && acc.legAdjRx+acc.legAdjTx > 0 {
				pAdjRx, pAdjTx = acc.legAdjRx, acc.legAdjTx
			}
			adjRx = acc.dirRx + pAdjRx
			adjTx = acc.dirTx + pAdjTx
		}
		if rx+tx+adjRx+adjTx == 0 {
			continue
		}
		cur := out[k.bucket]
		cur.rx += rx
		cur.tx += tx
		cur.adjRx += adjRx
		cur.adjTx += adjTx
		out[k.bucket] = cur
	}

	return out, nil
}

func (s *Store) evalDimensionWatchComponent(
	ctx context.Context,
	dimType string,
	matcher *accounting.CompiledDimensionMatcher,
	override *float64,
	nodeFilterSQL string,
	nodeArgs []any,
	evalFrom, evalTo time.Time,
	bs int,
	clampBucket func(int64) int64,
) (map[int64]watchBucketVals, error) {
	out := map[int64]watchBucketVals{}

	filters := []string{"dim_type = ?"}
	args := []any{dimType}
	if nodeFilterSQL != "" {
		filters = append(filters, nodeFilterSQL)
		args = append(args, nodeArgs...)
	}
	baseWhere := strings.Join(filters, " AND ")

	var querySQL string
	var queryArgs []any
	toUnix := evalTo.UTC().Unix()
	if !evalFrom.IsZero() && evalTo.Sub(evalFrom) > range7d {
		fromDay := (evalFrom.UTC().Unix() / daySeconds) * daySeconds
		querySQL = `SELECT (day / ?) * ? AS bucket, dim_key, SUM(rx), SUM(tx)
		 FROM dimension_daily
		 WHERE ` + baseWhere + ` AND day >= ? AND day <= ?
		 GROUP BY bucket, dim_key`
		queryArgs = append(queryArgs, bs, bs)
		queryArgs = append(queryArgs, args...)
		queryArgs = append(queryArgs, fromDay, toUnix)
	} else if !evalFrom.IsZero() {
		fromUnix := evalFrom.UTC().Unix()
		fromDay := (fromUnix / daySeconds) * daySeconds
		querySQL = `SELECT bucket, dim_key, SUM(rx), SUM(tx)
		 FROM (
			SELECT (sampled_at / ?) * ? AS bucket, dim_key, rx, tx
			FROM dimension_samples
			WHERE ` + baseWhere + ` AND sampled_at >= ? AND sampled_at <= ?
			UNION ALL
			SELECT (d.day / ?) * ? AS bucket, d.dim_key, d.rx, d.tx
			FROM dimension_daily d
			WHERE ` + baseWhere + ` AND d.day >= ? AND d.day <= ?
			  AND NOT EXISTS (
				SELECT 1 FROM dimension_samples s
				WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
			  )
		 )
		 GROUP BY bucket, dim_key`
		queryArgs = append(queryArgs, bs, bs)
		queryArgs = append(queryArgs, args...)
		queryArgs = append(queryArgs, fromUnix, toUnix)
		queryArgs = append(queryArgs, bs, bs)
		queryArgs = append(queryArgs, args...)
		queryArgs = append(queryArgs, fromDay, toUnix, daySeconds)
	} else {
		// All-time (evalFrom.IsZero()): combine dimension_samples with unrepresented dimension_daily.
		querySQL = `SELECT bucket, dim_key, SUM(rx), SUM(tx)
		 FROM (
			SELECT (sampled_at / ?) * ? AS bucket, dim_key, rx, tx
			FROM dimension_samples
			WHERE ` + baseWhere + ` AND sampled_at <= ?
			UNION ALL
			SELECT (d.day / ?) * ? AS bucket, d.dim_key, d.rx, d.tx
			FROM dimension_daily d
			WHERE ` + baseWhere + ` AND d.day <= ?
			  AND NOT EXISTS (
				SELECT 1 FROM dimension_samples s
				WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
			  )
		 )
		 GROUP BY bucket, dim_key`
		queryArgs = append(queryArgs, bs, bs)
		queryArgs = append(queryArgs, args...)
		queryArgs = append(queryArgs, toUnix)
		queryArgs = append(queryArgs, bs, bs)
		queryArgs = append(queryArgs, args...)
		queryArgs = append(queryArgs, toUnix, daySeconds)
	}

	rows, err := s.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	factor := 1.0
	if override != nil {
		factor = *override
	}

	for rows.Next() {
		var bucket int64
		var key string
		var rx, tx int64
		if err := rows.Scan(&bucket, &key, &rx, &tx); err != nil {
			return nil, err
		}
		if !matcher.Match(key) {
			continue
		}
		uRx, uTx := clampUint64(rx), clampUint64(tx)
		if uRx == 0 && uTx == 0 {
			continue
		}
		b := clampBucket(bucket)
		cur := out[b]
		cur.rx += uRx
		cur.tx += uTx
		cur.adjRx += accounting.ScaleBytes(uRx, factor)
		cur.adjTx += accounting.ScaleBytes(uTx, factor)
		out[b] = cur
	}
	return out, rows.Err()
}

func buildTieredLedgerQuery(
	evalFrom, evalTo time.Time,
	bs int,
	baseWhere string,
	baseArgs []any,
	selectCols, groupCols string,
) (string, []any) {
	toUnix := evalTo.UTC().Unix()
	var args []any
	if !evalFrom.IsZero() && evalTo.Sub(evalFrom) > range7d {
		fromDay := (evalFrom.UTC().Unix() / daySeconds) * daySeconds
		querySQL := `SELECT (day / ?) * ? AS bucket, ` + selectCols + `, SUM(rx), SUM(tx)
		 FROM ledger_daily
		 WHERE ` + baseWhere + ` AND day >= ? AND day <= ?
		 GROUP BY ` + groupCols
		args = append(args, bs, bs)
		args = append(args, baseArgs...)
		args = append(args, fromDay, toUnix)
		return querySQL, args
	}
	if !evalFrom.IsZero() {
		fromUnix := evalFrom.UTC().Unix()
		fromDay := (fromUnix / daySeconds) * daySeconds
		querySQL := `SELECT bucket, ` + selectCols + `, SUM(rx), SUM(tx)
		 FROM (
			SELECT (sampled_at / ?) * ? AS bucket, ` + selectCols + `, rx, tx
			FROM ledger_samples
			WHERE ` + baseWhere + ` AND sampled_at >= ? AND sampled_at <= ?
			UNION ALL
			SELECT (d.day / ?) * ? AS bucket, ` + selectCols + `, d.rx, d.tx
			FROM ledger_daily d
			WHERE ` + baseWhere + ` AND d.day >= ? AND d.day <= ?
			  AND NOT EXISTS (
				SELECT 1 FROM ledger_samples s
				WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
			  )
		 )
		 GROUP BY ` + groupCols
		args = append(args, bs, bs)
		args = append(args, baseArgs...)
		args = append(args, fromUnix, toUnix)
		args = append(args, bs, bs)
		args = append(args, baseArgs...)
		args = append(args, fromDay, toUnix, daySeconds)
		return querySQL, args
	}
	querySQL := `SELECT bucket, ` + selectCols + `, SUM(rx), SUM(tx)
	 FROM (
		SELECT (sampled_at / ?) * ? AS bucket, ` + selectCols + `, rx, tx
		FROM ledger_samples
		WHERE ` + baseWhere + ` AND sampled_at <= ?
		UNION ALL
		SELECT (d.day / ?) * ? AS bucket, ` + selectCols + `, d.rx, d.tx
		FROM ledger_daily d
		WHERE ` + baseWhere + ` AND d.day <= ?
		  AND NOT EXISTS (
			SELECT 1 FROM ledger_samples s
			WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
		  )
	 )
	 GROUP BY ` + groupCols
	args = append(args, bs, bs)
	args = append(args, baseArgs...)
	args = append(args, toUnix)
	args = append(args, bs, bs)
	args = append(args, baseArgs...)
	args = append(args, toUnix, daySeconds)
	return querySQL, args
}

func (s *Store) matchWatchNodes(ctx context.Context, matcher *accounting.CompiledDimensionMatcher) ([]string, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id FROM nodes
		 UNION
		 SELECT DISTINCT node_id FROM ledger_totals
		 UNION
		 SELECT DISTINCT node_id FROM dimension_daily`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matched []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		if matcher.Match(nodeID) {
			matched = append(matched, nodeID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(matched)
	return matched, nil
}

func (s *Store) matchWatchDevices(ctx context.Context, matcher *accounting.CompiledDimensionMatcher) ([]string, error) {
	devRows, err := s.db.QueryContext(
		ctx,
		`SELECT id, IFNULL(canonical_id, ''), IFNULL(display_name, '') FROM devices`,
	)
	if err != nil {
		return nil, err
	}
	defer devRows.Close()

	type devInfo struct {
		id, canonical, displayName string
	}
	byID := map[string]devInfo{}
	children := map[string][]string{}
	for devRows.Next() {
		var d devInfo
		if err := devRows.Scan(&d.id, &d.canonical, &d.displayName); err != nil {
			return nil, err
		}
		byID[d.id] = d
		if d.canonical != "" && d.canonical != d.id {
			children[d.canonical] = append(children[d.canonical], d.id)
		}
	}
	if err := devRows.Err(); err != nil {
		return nil, err
	}
	if err := devRows.Close(); err != nil {
		return nil, err
	}

	evRows, err := s.db.QueryContext(ctx, `SELECT device_id, evidence_rank, evidence_value FROM identity_evidence`)
	if err != nil {
		return nil, err
	}
	defer evRows.Close()

	matchedRoots := map[string]struct{}{}
	resolveRoot := func(id string) string {
		cur := id
		seen := map[string]struct{}{}
		for {
			if _, ok := seen[cur]; ok {
				return cur
			}
			seen[cur] = struct{}{}
			info, ok := byID[cur]
			if !ok || info.canonical == "" || info.canonical == cur {
				return cur
			}
			cur = info.canonical
		}
	}

	for id, d := range byID {
		if matcher.Match(id) || (d.displayName != "" && matcher.Match(d.displayName)) {
			matchedRoots[resolveRoot(id)] = struct{}{}
		}
	}
	for evRows.Next() {
		var devID, val string
		var rank int
		if err := evRows.Scan(&devID, &rank, &val); err != nil {
			return nil, err
		}
		if matcher.Match(val) {
			matchedRoots[resolveRoot(devID)] = struct{}{}
			continue
		}
		if strings.Contains(val, "|") {
			for _, part := range strings.Split(val, "|") {
				if part != "" && matcher.Match(part) {
					matchedRoots[resolveRoot(devID)] = struct{}{}
					break
				}
			}
		}
	}
	if err := evRows.Err(); err != nil {
		return nil, err
	}

	// Expand canonical roots to include merged child device_ids so historical
	// pre-merge ledger rows are included in device watch evaluation.
	allIDs := map[string]struct{}{}
	var expand func(id string)
	expand = func(id string) {
		if _, seen := allIDs[id]; seen {
			return
		}
		allIDs[id] = struct{}{}
		for _, child := range children[id] {
			expand(child)
		}
	}
	for root := range matchedRoots {
		expand(root)
	}

	out := make([]string, 0, len(allIDs))
	for id := range allIDs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func sqlInClause(col string, vals []string) (string, []any) {
	placeholders := make([]string, len(vals))
	args := make([]any, len(vals))
	for i, v := range vals {
		placeholders[i] = "?"
		args[i] = v
	}
	return col + " IN (" + strings.Join(placeholders, ",") + ")", args
}

func clampSub(a, b uint64) uint64 {
	if a <= b {
		return 0
	}
	return a - b
}
