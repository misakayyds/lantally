package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/alert"
	"github.com/misakayyds/lantally/internal/protocol"
)

type ProviderSubscription struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Source          string   `json:"source"`
	SourceType      string   `json:"source_type,omitempty"`
	NodeID          string   `json:"node_id,omitempty"`
	ProviderName    string   `json:"provider_name,omitempty"`
	VehicleType     string   `json:"vehicle_type"`
	UploadBytes     uint64   `json:"upload_bytes"`
	DownloadBytes   uint64   `json:"download_bytes"`
	UsedBytes       uint64   `json:"used_bytes"`
	TotalBytes      uint64   `json:"total_bytes"`
	Unit            string   `json:"unit"`
	QuotaUnit       string   `json:"quota_unit"`
	Expire          int64    `json:"expire"`
	ExpireAt        int64    `json:"expire_at"`
	ResetDay        int      `json:"reset_day"`
	OutboundPattern string   `json:"outbound_pattern"`
	LinkedNodes     []string `json:"linked_nodes"`
	Proxies         []string `json:"proxies"`
	NodesLocked     bool     `json:"nodes_locked,omitempty"`
	Enabled         bool     `json:"enabled"`
	UpdatedAt       string   `json:"updated_at"`
}

type ProviderStatus struct {
	ProviderSubscription
	LocalAdjusted uint64    `json:"local_adjusted"`
	ProxyAdjusted uint64    `json:"proxy_adjusted"`
	DriftBytes    int64     `json:"drift_bytes"`
	DriftRatio    float64   `json:"drift_ratio"`
	DriftAlert    bool      `json:"drift_alert"`
	OverDrift     bool      `json:"over_drift"`
	CycleFrom     time.Time `json:"cycle_from"`
	CycleTo       time.Time `json:"cycle_to"`
	SharedGroup   string    `json:"shared_group,omitempty"`
}

type ProviderSummary struct {
	TotalBytes    uint64  `json:"total_bytes"`
	UsedBytes     uint64  `json:"used_bytes"`
	LocalAdjusted uint64  `json:"local_adjusted"`
	ProxyAdjusted uint64  `json:"proxy_adjusted"`
	DriftBytes    int64   `json:"drift_bytes"`
	DriftRatio    float64 `json:"drift_ratio"`
	DriftAlert    bool    `json:"drift_alert"`
	ActiveCount   int     `json:"active_count"`
}

type ProviderInput struct {
	Name            string
	Unit            string
	TotalValue      *float64
	TotalBytes      *uint64
	UsedValue       *float64
	UsedBytes       *uint64
	UploadBytes     *uint64
	DownloadBytes   *uint64
	ExpireAt        *int64
	ResetDay        *int
	OutboundPattern *string
	LinkedNodes     *[]string
	Proxies         *[]string
	Enabled         *bool
}

func (s *Store) UpsertMihomoProviders(
	ctx context.Context,
	nodeID string,
	providers []protocol.ProviderSnapshot,
	now time.Time,
) error {
	if len(providers) == 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ts := now.UTC().Format(time.RFC3339Nano)
	nodeID = strings.TrimSpace(nodeID)

	type filteredProvider struct {
		name        string
		vehicleType string
		upload      uint64
		download    uint64
		total       uint64
		expire      int64
		proxies     []string
	}
	var filtered []filteredProvider
	for _, p := range providers {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		vt := strings.TrimSpace(p.VehicleType)
		// Filter out Mihomo's built-in "Compatible" pseudo-providers and empty non-HTTP entries.
		if strings.EqualFold(vt, "Compatible") {
			continue
		}
		isHTTP := strings.EqualFold(vt, "HTTP")
		if !isHTTP && p.Upload == 0 && p.Download == 0 && p.Total == 0 && p.Expire == 0 {
			continue
		}
		if vt == "" {
			vt = "HTTP"
		}
		proxies := cleanProviderProxies(p.Proxies)
		filtered = append(filtered, filteredProvider{
			name:        name,
			vehicleType: vt,
			upload:      p.Upload,
			download:    p.Download,
			total:       p.Total,
			expire:      p.Expire,
			proxies:     proxies,
		})
	}
	if len(filtered) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, fp := range filtered {
		var existingLinkedJSON, existingProxiesJSON string
		var nodesLockedInt int
		err = tx.QueryRowContext(
			ctx,
			`SELECT linked_nodes_json, proxies_json, nodes_locked
			 FROM provider_subscriptions
			 WHERE source_type = 'mihomo' AND provider_name = ?`,
			fp.name,
		).Scan(&existingLinkedJSON, &existingProxiesJSON, &nodesLockedInt)

		if errors.Is(err, sql.ErrNoRows) {
			var linked []string
			if nodeID != "" {
				linked = []string{nodeID}
			}
			linkedJSON, _ := json.Marshal(nonNilStrings(linked))
			proxiesJSON, _ := json.Marshal(nonNilStrings(fp.proxies))
			if _, err = tx.ExecContext(
				ctx,
				`INSERT INTO provider_subscriptions (
					name, source_type, node_id, provider_name, vehicle_type,
					upload_bytes, download_bytes, total_bytes, quota_unit, expire_at,
					reset_day, outbound_pattern, linked_nodes_json, proxies_json,
					nodes_locked, enabled, updated_at
				) VALUES (?, 'mihomo', ?, ?, ?, ?, ?, ?, 'GB', ?, 1, '', ?, ?, 0, 1, ?)`,
				fp.name,
				nodeID,
				fp.name,
				fp.vehicleType,
				int64(fp.upload),
				int64(fp.download),
				int64(fp.total),
				fp.expire,
				string(linkedJSON),
				string(proxiesJSON),
				ts,
			); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}

		var existingLinked []string
		_ = json.Unmarshal([]byte(existingLinkedJSON), &existingLinked)
		if nodesLockedInt == 0 && nodeID != "" {
			existingLinked = mergeUniqueSortedStrings(existingLinked, []string{nodeID})
		} else {
			existingLinked = mergeUniqueSortedStrings(existingLinked, nil)
		}

		var existingProxies []string
		_ = json.Unmarshal([]byte(existingProxiesJSON), &existingProxies)
		mergedProxies := mergeUniqueSortedStrings(existingProxies, fp.proxies)

		linkedJSON, _ := json.Marshal(nonNilStrings(existingLinked))
		proxiesJSON, _ := json.Marshal(nonNilStrings(mergedProxies))

		// Preserve user-edited columns: name, enabled, outbound_pattern, reset_day, quota_unit, nodes_locked.
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE provider_subscriptions
			 SET node_id = ?, vehicle_type = ?, upload_bytes = ?, download_bytes = ?,
			     total_bytes = ?, expire_at = ?, linked_nodes_json = ?, proxies_json = ?, updated_at = ?
			 WHERE source_type = 'mihomo' AND provider_name = ?`,
			nodeID,
			fp.vehicleType,
			int64(fp.upload),
			int64(fp.download),
			int64(fp.total),
			fp.expire,
			string(linkedJSON),
			string(proxiesJSON),
			ts,
			fp.name,
		); err != nil {
			return err
		}
	}

	err = tx.Commit()
	return err
}

func (s *Store) ListProviderSubscriptions(ctx context.Context) ([]ProviderSubscription, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, source_type, node_id, provider_name, vehicle_type,
		        upload_bytes, download_bytes, total_bytes, quota_unit, expire_at,
		        reset_day, outbound_pattern, linked_nodes_json, proxies_json,
		        nodes_locked, enabled, updated_at
		 FROM provider_subscriptions
		 ORDER BY CASE WHEN source_type = 'mihomo' THEN 0 ELSE 1 END, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ProviderSubscription{}
	for rows.Next() {
		p, err := scanProviderSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProviderSubscription(ctx context.Context, id int64) (ProviderSubscription, error) {
	if id <= 0 {
		return ProviderSubscription{}, fmt.Errorf("%w: invalid provider id", ErrInvalidInput)
	}
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, name, source_type, node_id, provider_name, vehicle_type,
		        upload_bytes, download_bytes, total_bytes, quota_unit, expire_at,
		        reset_day, outbound_pattern, linked_nodes_json, proxies_json,
		        nodes_locked, enabled, updated_at
		 FROM provider_subscriptions
		 WHERE id = ?`,
		id,
	)
	p, err := scanProviderSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderSubscription{}, sql.ErrNoRows
	}
	if err != nil {
		return ProviderSubscription{}, err
	}
	return p, nil
}

func (s *Store) CreateProviderSubscription(ctx context.Context, input ProviderInput) (ProviderSubscription, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ProviderSubscription{}, fmt.Errorf("%w: provider name is required", ErrInvalidInput)
	}
	unit := strings.TrimSpace(input.Unit)
	if unit == "" {
		unit = accounting.UnitGB
	}
	_, canonicalUnit, err := accounting.UnitMultiplier(unit)
	if err != nil {
		return ProviderSubscription{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	var totalBytes uint64
	if input.TotalValue != nil {
		b, err := accounting.QuotaToBytes(*input.TotalValue, canonicalUnit)
		if err != nil {
			return ProviderSubscription{}, fmt.Errorf("%w: total_value: %v", ErrInvalidInput, err)
		}
		totalBytes = b
	} else if input.TotalBytes != nil {
		if *input.TotalBytes > math.MaxInt64 {
			return ProviderSubscription{}, fmt.Errorf("%w: total_bytes exceeds int64 range", ErrInvalidInput)
		}
		totalBytes = *input.TotalBytes
	}

	var uploadBytes, downloadBytes uint64
	if input.UploadBytes != nil {
		if *input.UploadBytes > math.MaxInt64 {
			return ProviderSubscription{}, fmt.Errorf("%w: upload_bytes exceeds int64 range", ErrInvalidInput)
		}
		uploadBytes = *input.UploadBytes
	}
	if input.DownloadBytes != nil {
		if *input.DownloadBytes > math.MaxInt64 {
			return ProviderSubscription{}, fmt.Errorf("%w: download_bytes exceeds int64 range", ErrInvalidInput)
		}
		downloadBytes = *input.DownloadBytes
	}
	if input.UploadBytes == nil && input.DownloadBytes == nil {
		if input.UsedValue != nil {
			b, err := accounting.QuotaToBytes(*input.UsedValue, canonicalUnit)
			if err != nil {
				return ProviderSubscription{}, fmt.Errorf("%w: used_value: %v", ErrInvalidInput, err)
			}
			downloadBytes = b
		} else if input.UsedBytes != nil {
			if *input.UsedBytes > math.MaxInt64 {
				return ProviderSubscription{}, fmt.Errorf("%w: used_bytes exceeds int64 range", ErrInvalidInput)
			}
			downloadBytes = *input.UsedBytes
		}
	}

	resetDay := 1
	if input.ResetDay != nil {
		resetDay = *input.ResetDay
	}
	if resetDay < 1 || resetDay > 28 {
		return ProviderSubscription{}, fmt.Errorf("%w: reset_day must be between 1 and 28", ErrInvalidInput)
	}

	var expireAt int64
	if input.ExpireAt != nil {
		if *input.ExpireAt < 0 {
			return ProviderSubscription{}, fmt.Errorf("%w: expire_at must be non-negative", ErrInvalidInput)
		}
		expireAt = *input.ExpireAt
	}

	var outboundPattern string
	if input.OutboundPattern != nil {
		outboundPattern = strings.TrimSpace(*input.OutboundPattern)
		if outboundPattern != "" {
			if _, err := regexp.Compile(outboundPattern); err != nil {
				return ProviderSubscription{}, fmt.Errorf("%w: invalid outbound_pattern regex: %v", ErrInvalidInput, err)
			}
		}
	}

	var linkedNodes []string
	if input.LinkedNodes != nil {
		linkedNodes = mergeUniqueSortedStrings(*input.LinkedNodes, nil)
	}
	var proxies []string
	if input.Proxies != nil {
		proxies = cleanProviderProxies(*input.Proxies)
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	linkedJSON, _ := json.Marshal(nonNilStrings(linkedNodes))
	proxiesJSON, _ := json.Marshal(nonNilStrings(proxies))
	ts := time.Now().UTC().Format(time.RFC3339Nano)

	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO provider_subscriptions (
			name, source_type, node_id, provider_name, vehicle_type,
			upload_bytes, download_bytes, total_bytes, quota_unit, expire_at,
			reset_day, outbound_pattern, linked_nodes_json, proxies_json,
			nodes_locked, enabled, updated_at
		) VALUES (?, 'manual', '', '', 'Manual', ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		name,
		int64(uploadBytes),
		int64(downloadBytes),
		int64(totalBytes),
		canonicalUnit,
		expireAt,
		resetDay,
		outboundPattern,
		string(linkedJSON),
		string(proxiesJSON),
		boolToInt(enabled),
		ts,
	)
	if err != nil {
		return ProviderSubscription{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ProviderSubscription{}, err
	}
	return s.GetProviderSubscription(ctx, id)
}

func (s *Store) UpdateProviderSubscription(ctx context.Context, id int64, input ProviderInput) (ProviderSubscription, error) {
	if id <= 0 {
		return ProviderSubscription{}, fmt.Errorf("%w: invalid provider id", ErrInvalidInput)
	}
	existing, err := s.GetProviderSubscription(ctx, id)
	if err != nil {
		return ProviderSubscription{}, err
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = existing.Name
	}

	canonicalUnit := existing.QuotaUnit
	if strings.TrimSpace(input.Unit) != "" {
		_, cu, err := accounting.UnitMultiplier(input.Unit)
		if err != nil {
			return ProviderSubscription{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		canonicalUnit = cu
	}
	if canonicalUnit == "" {
		canonicalUnit = accounting.UnitGB
	}

	totalBytes := existing.TotalBytes
	if input.TotalValue != nil {
		b, err := accounting.QuotaToBytes(*input.TotalValue, canonicalUnit)
		if err != nil {
			return ProviderSubscription{}, fmt.Errorf("%w: total_value: %v", ErrInvalidInput, err)
		}
		totalBytes = b
	} else if input.TotalBytes != nil {
		if *input.TotalBytes > math.MaxInt64 {
			return ProviderSubscription{}, fmt.Errorf("%w: total_bytes exceeds int64 range", ErrInvalidInput)
		}
		totalBytes = *input.TotalBytes
	}

	uploadBytes := existing.UploadBytes
	downloadBytes := existing.DownloadBytes
	if input.UploadBytes != nil {
		if *input.UploadBytes > math.MaxInt64 {
			return ProviderSubscription{}, fmt.Errorf("%w: upload_bytes exceeds int64 range", ErrInvalidInput)
		}
		uploadBytes = *input.UploadBytes
	}
	if input.DownloadBytes != nil {
		if *input.DownloadBytes > math.MaxInt64 {
			return ProviderSubscription{}, fmt.Errorf("%w: download_bytes exceeds int64 range", ErrInvalidInput)
		}
		downloadBytes = *input.DownloadBytes
	}
	if input.UploadBytes == nil && input.DownloadBytes == nil {
		if input.UsedValue != nil {
			b, err := accounting.QuotaToBytes(*input.UsedValue, canonicalUnit)
			if err != nil {
				return ProviderSubscription{}, fmt.Errorf("%w: used_value: %v", ErrInvalidInput, err)
			}
			uploadBytes = 0
			downloadBytes = b
		} else if input.UsedBytes != nil {
			if *input.UsedBytes > math.MaxInt64 {
				return ProviderSubscription{}, fmt.Errorf("%w: used_bytes exceeds int64 range", ErrInvalidInput)
			}
			uploadBytes = 0
			downloadBytes = *input.UsedBytes
		}
	}

	resetDay := existing.ResetDay
	if input.ResetDay != nil {
		resetDay = *input.ResetDay
	}
	if resetDay < 1 || resetDay > 28 {
		return ProviderSubscription{}, fmt.Errorf("%w: reset_day must be between 1 and 28", ErrInvalidInput)
	}

	expireAt := existing.ExpireAt
	if input.ExpireAt != nil {
		if *input.ExpireAt < 0 {
			return ProviderSubscription{}, fmt.Errorf("%w: expire_at must be non-negative", ErrInvalidInput)
		}
		expireAt = *input.ExpireAt
	}

	outboundPattern := existing.OutboundPattern
	if input.OutboundPattern != nil {
		outboundPattern = strings.TrimSpace(*input.OutboundPattern)
		if outboundPattern != "" {
			if _, err := regexp.Compile(outboundPattern); err != nil {
				return ProviderSubscription{}, fmt.Errorf("%w: invalid outbound_pattern regex: %v", ErrInvalidInput, err)
			}
		}
	}

	linkedNodes := existing.LinkedNodes
	nodesLocked := existing.NodesLocked
	if input.LinkedNodes != nil {
		linkedNodes = mergeUniqueSortedStrings(*input.LinkedNodes, nil)
		nodesLocked = true
	}
	proxies := existing.Proxies
	if input.Proxies != nil {
		proxies = cleanProviderProxies(*input.Proxies)
	}
	enabled := existing.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	linkedJSON, _ := json.Marshal(nonNilStrings(linkedNodes))
	proxiesJSON, _ := json.Marshal(nonNilStrings(proxies))
	ts := time.Now().UTC().Format(time.RFC3339Nano)

	res, err := s.db.ExecContext(
		ctx,
		`UPDATE provider_subscriptions
		 SET name = ?, upload_bytes = ?, download_bytes = ?, total_bytes = ?, quota_unit = ?,
		     expire_at = ?, reset_day = ?, outbound_pattern = ?, linked_nodes_json = ?,
		     proxies_json = ?, nodes_locked = ?, enabled = ?, updated_at = ?
		 WHERE id = ?`,
		name,
		int64(uploadBytes),
		int64(downloadBytes),
		int64(totalBytes),
		canonicalUnit,
		expireAt,
		resetDay,
		outboundPattern,
		string(linkedJSON),
		string(proxiesJSON),
		boolToInt(nodesLocked),
		boolToInt(enabled),
		ts,
		id,
	)
	if err != nil {
		return ProviderSubscription{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ProviderSubscription{}, err
	}
	if n == 0 {
		return ProviderSubscription{}, sql.ErrNoRows
	}
	return s.GetProviderSubscription(ctx, id)
}

func (s *Store) DeleteProviderSubscription(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: invalid provider id", ErrInvalidInput)
	}
	sub, getErr := s.GetProviderSubscription(ctx, id)
	if getErr != nil && !errors.Is(getErr, sql.ErrNoRows) {
		return getErr
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM provider_subscriptions WHERE id = ?`, id)
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
	nowTS := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(
		ctx,
		`UPDATE alerts SET resolved_at = ? WHERE fingerprint LIKE ? AND resolved_at IS NULL`,
		nowTS,
		fmt.Sprintf("drift|provider|%d|%%", id),
	)
	if getErr == nil {
		sKey := providerSharedGroupKey(sub)
		if !strings.HasPrefix(sKey, "id:") {
			subs, listErr := s.ListProviderSubscriptions(ctx)
			if listErr == nil {
				stillActive := false
				for _, other := range subs {
					if other.Enabled && !strings.EqualFold(other.VehicleType, "Compatible") && providerSharedGroupKey(other) == sKey {
						stillActive = true
						break
					}
				}
				if !stillActive {
					_, _ = s.db.ExecContext(
						ctx,
						`UPDATE alerts SET resolved_at = ? WHERE fingerprint LIKE ? AND resolved_at IS NULL`,
						nowTS,
						fmt.Sprintf("drift|provider|group|%s|%%", sKey),
					)
				}
			}
		}
	}
	return nil
}

type providerGroupStatus struct {
	sharedGroup   string
	totalBytes    uint64
	usedBytes     uint64
	localAdjusted uint64
	resetDay      int
	cycleFrom     time.Time
	members       []ProviderStatus
}

func (s *Store) ListProviderStatuses(
	ctx context.Context,
	now time.Time,
	loc *time.Location,
) ([]ProviderStatus, ProviderSummary, error) {
	statuses, summary, _, err := s.listProviderStatusesWithGroups(ctx, now, loc)
	return statuses, summary, err
}

func (s *Store) listProviderStatusesWithGroups(
	ctx context.Context,
	now time.Time,
	loc *time.Location,
) ([]ProviderStatus, ProviderSummary, []providerGroupStatus, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if loc == nil {
		loc = time.UTC
	}
	subs, err := s.ListProviderSubscriptions(ctx)
	if err != nil {
		return nil, ProviderSummary{}, nil, err
	}
	rules, err := s.CompiledMultiplierRules(ctx)
	if err != nil {
		return nil, ProviderSummary{}, nil, err
	}

	type cycleData struct {
		from time.Time
		to   time.Time
		adj  map[nodeOutboundKey]uint64
	}
	cycleCache := map[int]cycleData{}

	loadCycle := func(resetDay int) (cycleData, error) {
		if resetDay < 1 || resetDay > 28 {
			resetDay = 1
		}
		if cached, ok := cycleCache[resetDay]; ok {
			return cached, nil
		}
		from, to := BillingPeriodInLoc(now, resetDay, loc)
		adj, err := s.periodProxyAdjustedByNodeOutbound(ctx, from, to, rules)
		if err != nil {
			return cycleData{}, err
		}
		cd := cycleData{from: from, to: to, adj: adj}
		cycleCache[resetDay] = cd
		return cd, nil
	}

	statuses := make([]ProviderStatus, 0, len(subs))
	type sharedGroupAccum struct {
		sharedGroup string
		totalBytes  uint64
		usedBytes   uint64
		resetDay    int
		cycleFrom   time.Time
		members     []ProviderStatus
		matched     map[nodeOutboundKey]uint64 // dedup key across providers in shared group -> adjusted bytes
	}
	sharedGroups := map[string]*sharedGroupAccum{}
	var sharedOrder []string
	var activeCount int

	for _, sub := range subs {
		// Defensive guard against any pre-existing Compatible rows.
		if strings.EqualFold(sub.VehicleType, "Compatible") {
			continue
		}
		cd, err := loadCycle(sub.ResetDay)
		if err != nil {
			return nil, ProviderSummary{}, nil, err
		}

		matcher, err := compileProviderMatcher(sub)
		if err != nil {
			return nil, ProviderSummary{}, nil, err
		}

		var localAdjusted uint64
		var matchedKeys []nodeOutboundKey
		for k, val := range cd.adj {
			if matcher.matches(k.nodeID, k.outbound) {
				localAdjusted += val
				matchedKeys = append(matchedKeys, k)
			}
		}

		usedBytes := sub.UploadBytes + sub.DownloadBytes
		driftBytes := int64(localAdjusted) - int64(usedBytes)
		driftRatio := safeDriftRatio(localAdjusted, usedBytes)
		driftAlert := sub.Enabled && usedBytes > 0 && driftRatio > alert.DefaultDriftConfig().TargetRatio

		sKey := providerSharedGroupKey(sub)
		if sKey == "" {
			sKey = fmt.Sprintf("id:%d", sub.ID)
		}
		st := ProviderStatus{
			ProviderSubscription: sub,
			LocalAdjusted:        localAdjusted,
			ProxyAdjusted:        localAdjusted,
			DriftBytes:           driftBytes,
			DriftRatio:           driftRatio,
			DriftAlert:           driftAlert,
			OverDrift:            driftAlert,
			CycleFrom:            cd.from,
			CycleTo:              cd.to,
			SharedGroup:          sKey,
		}
		statuses = append(statuses, st)

		if sub.Enabled {
			activeCount++
			grp := sharedGroups[sKey]
			if grp == nil {
				grp = &sharedGroupAccum{
					sharedGroup: sKey,
					totalBytes:  sub.TotalBytes,
					usedBytes:   usedBytes,
					resetDay:    sub.ResetDay,
					cycleFrom:   cd.from,
					matched:     map[nodeOutboundKey]uint64{},
				}
				sharedGroups[sKey] = grp
				sharedOrder = append(sharedOrder, sKey)
			} else {
				if sub.TotalBytes > grp.totalBytes {
					grp.totalBytes = sub.TotalBytes
				}
				if usedBytes > grp.usedBytes {
					grp.usedBytes = usedBytes
				}
			}
			grp.members = append(grp.members, st)
			for _, k := range matchedKeys {
				if cur, exists := grp.matched[k]; !exists || cd.adj[k] > cur {
					grp.matched[k] = cd.adj[k]
				}
			}
		}
	}

	var summary ProviderSummary
	summary.ActiveCount = activeCount
	groups := make([]providerGroupStatus, 0, len(sharedOrder))
	for _, sKey := range sharedOrder {
		grp := sharedGroups[sKey]
		var grpLocal uint64
		for _, v := range grp.matched {
			grpLocal += v
		}
		summary.TotalBytes += grp.totalBytes
		summary.UsedBytes += grp.usedBytes
		summary.LocalAdjusted += grpLocal
		groups = append(groups, providerGroupStatus{
			sharedGroup:   grp.sharedGroup,
			totalBytes:    grp.totalBytes,
			usedBytes:     grp.usedBytes,
			localAdjusted: grpLocal,
			resetDay:      grp.resetDay,
			cycleFrom:     grp.cycleFrom,
			members:       grp.members,
		})
	}
	summary.ProxyAdjusted = summary.LocalAdjusted
	summary.DriftBytes = int64(summary.LocalAdjusted) - int64(summary.UsedBytes)
	summary.DriftRatio = safeDriftRatio(summary.LocalAdjusted, summary.UsedBytes)
	summary.DriftAlert = summary.UsedBytes > 0 && summary.DriftRatio > alert.DefaultDriftConfig().TargetRatio

	return statuses, summary, groups, nil
}

type providerTrafficMatcher struct {
	nodeSet    map[string]struct{}
	proxySet   map[string]struct{}
	outboundRe *regexp.Regexp
	allOut     bool
}

func compileProviderMatcher(sub ProviderSubscription) (providerTrafficMatcher, error) {
	m := providerTrafficMatcher{}
	if len(sub.LinkedNodes) > 0 {
		m.nodeSet = make(map[string]struct{}, len(sub.LinkedNodes))
		for _, n := range sub.LinkedNodes {
			m.nodeSet[n] = struct{}{}
		}
	}
	if strings.TrimSpace(sub.OutboundPattern) != "" {
		re, err := regexp.Compile(sub.OutboundPattern)
		if err != nil {
			return providerTrafficMatcher{}, err
		}
		m.outboundRe = re
	} else if len(sub.Proxies) > 0 {
		m.proxySet = make(map[string]struct{}, len(sub.Proxies))
		for _, p := range sub.Proxies {
			m.proxySet[p] = struct{}{}
		}
	} else {
		m.allOut = true
	}
	return m, nil
}

func (m providerTrafficMatcher) matches(nodeID, outbound string) bool {
	if len(m.nodeSet) > 0 {
		if _, ok := m.nodeSet[nodeID]; !ok {
			return false
		}
	}
	if m.outboundRe != nil {
		return m.outboundRe.MatchString(outbound)
	}
	if len(m.proxySet) > 0 {
		_, ok := m.proxySet[outbound]
		return ok
	}
	return m.allOut
}

func providerSharedGroupKey(sub ProviderSubscription) string {
	if sub.SourceType == "mihomo" && sub.TotalBytes > 0 {
		if sub.ExpireAt > 0 {
			return fmt.Sprintf("te:%d:%d", sub.TotalBytes, sub.ExpireAt)
		}
		return fmt.Sprintf("tud:%d:%d:%d", sub.TotalBytes, sub.UploadBytes, sub.DownloadBytes)
	}
	return fmt.Sprintf("id:%d", sub.ID)
}

func safeDriftRatio(localAdjusted, usedBytes uint64) float64 {
	if usedBytes == 0 {
		return 0
	}
	diff := float64(localAdjusted) - float64(usedBytes)
	if diff < 0 {
		diff = -diff
	}
	ratio := diff / float64(usedBytes)
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0
	}
	return ratio
}

type nodeOutboundKey struct {
	nodeID   string
	outbound string
}

func (s *Store) periodProxyAdjustedByNodeOutbound(
	ctx context.Context,
	from, to time.Time,
	rules *accounting.CompiledRuleSet,
) (map[nodeOutboundKey]uint64, error) {
	out := map[nodeOutboundKey]uint64{}
	if to.Before(from) {
		return out, nil
	}
	fromUnix := from.UTC().Unix()
	toUnix := to.UTC().Unix()
	fromDay := (fromUnix / daySeconds) * daySeconds

	var querySQL string
	var args []any
	if to.Sub(from) > range7d {
		querySQL = `SELECT node_id, outbound, SUM(rx), SUM(tx)
		 FROM ledger_daily
		 WHERE device_id = '' AND class = ? AND outbound != '' AND day >= ? AND day <= ?
		 GROUP BY node_id, outbound`
		args = []any{accounting.ClassProxyRaw, fromDay, toUnix}
	} else {
		querySQL = `SELECT node_id, outbound, SUM(rx), SUM(tx)
		 FROM (
			SELECT node_id, outbound, rx, tx
			FROM ledger_samples
			WHERE device_id = '' AND class = ? AND outbound != '' AND sampled_at >= ? AND sampled_at <= ?
			UNION ALL
			SELECT d.node_id, d.outbound, d.rx, d.tx
			FROM ledger_daily d
			WHERE d.device_id = '' AND d.class = ? AND d.outbound != '' AND d.day >= ? AND d.day <= ?
			  AND NOT EXISTS (
				SELECT 1 FROM ledger_samples s
				WHERE s.sampled_at >= d.day AND s.sampled_at < d.day + ?
			  )
		 )
		 GROUP BY node_id, outbound`
		args = []any{
			accounting.ClassProxyRaw, fromUnix, toUnix,
			accounting.ClassProxyRaw, fromDay, toUnix, daySeconds,
		}
	}

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var nodeID, outbound string
		var rx, tx int64
		if err := rows.Scan(&nodeID, &outbound, &rx, &tx); err != nil {
			return nil, err
		}
		uRx, uTx := clampUint64(rx), clampUint64(tx)
		factor := rules.Match(outbound).Factor
		adj := accounting.ScaleBytes(uRx, factor) + accounting.ScaleBytes(uTx, factor)
		out[nodeOutboundKey{nodeID: nodeID, outbound: outbound}] += adj
	}
	return out, rows.Err()
}

func scanProviderSubscription(sc rowScanner) (ProviderSubscription, error) {
	var (
		p                            ProviderSubscription
		upload, download, total      int64
		linkedJSON, proxiesJSON      string
		nodesLockedInt, enabledInt   int
	)
	if err := sc.Scan(
		&p.ID,
		&p.Name,
		&p.SourceType,
		&p.NodeID,
		&p.ProviderName,
		&p.VehicleType,
		&upload,
		&download,
		&total,
		&p.QuotaUnit,
		&p.ExpireAt,
		&p.ResetDay,
		&p.OutboundPattern,
		&linkedJSON,
		&proxiesJSON,
		&nodesLockedInt,
		&enabledInt,
		&p.UpdatedAt,
	); err != nil {
		return ProviderSubscription{}, err
	}
	p.Source = p.SourceType
	p.UploadBytes = clampUint64(upload)
	p.DownloadBytes = clampUint64(download)
	p.UsedBytes = p.UploadBytes + p.DownloadBytes
	p.TotalBytes = clampUint64(total)
	if p.QuotaUnit == "" {
		p.QuotaUnit = accounting.UnitGB
	}
	p.Unit = p.QuotaUnit
	p.Expire = p.ExpireAt
	p.NodesLocked = nodesLockedInt != 0
	p.Enabled = enabledInt != 0
	p.LinkedNodes = []string{}
	p.Proxies = []string{}
	if strings.TrimSpace(linkedJSON) != "" {
		_ = json.Unmarshal([]byte(linkedJSON), &p.LinkedNodes)
	}
	if strings.TrimSpace(proxiesJSON) != "" {
		_ = json.Unmarshal([]byte(proxiesJSON), &p.Proxies)
	}
	if p.LinkedNodes == nil {
		p.LinkedNodes = []string{}
	}
	if p.Proxies == nil {
		p.Proxies = []string{}
	}
	return p, nil
}

func cleanProviderProxies(raw []string) []string {
	set := map[string]struct{}{}
	for _, p := range raw {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		switch strings.ToUpper(trimmed) {
		case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL":
			continue
		}
		set[trimmed] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func mergeUniqueSortedStrings(a, b []string) []string {
	set := map[string]struct{}{}
	for _, v := range a {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	for _, v := range b {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
