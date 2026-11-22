package accounting

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// WatchDimension identifies a single traffic dimension supported by watch items.
type WatchDimension string

const (
	WatchDimNode     WatchDimension = "node"
	WatchDimIface    WatchDimension = "iface"
	WatchDimOutbound WatchDimension = "outbound"
	WatchDimGroup    WatchDimension = "group"
	WatchDimProcess  WatchDimension = "process"
	WatchDimDomain   WatchDimension = "domain"
	WatchDimDevice   WatchDimension = "device"
)

// ValidWatchDimensions lists all supported watch dimensions in deterministic order.
var ValidWatchDimensions = []WatchDimension{
	WatchDimNode,
	WatchDimIface,
	WatchDimOutbound,
	WatchDimGroup,
	WatchDimProcess,
	WatchDimDomain,
	WatchDimDevice,
}

func IsValidWatchDimension(dim WatchDimension) bool {
	switch dim {
	case WatchDimNode,
		WatchDimIface,
		WatchDimOutbound,
		WatchDimGroup,
		WatchDimProcess,
		WatchDimDomain,
		WatchDimDevice:
		return true
	default:
		return false
	}
}

// DimensionMatcher matches keys within a single dimension using OR semantics across Values.
type DimensionMatcher struct {
	Type   MatchType `json:"type"`
	Values []string  `json:"values"`
}

// WatchMatcherMap maps each dimension to its matcher configuration.
type WatchMatcherMap map[WatchDimension]DimensionMatcher

const (
	UnitB   = "B"
	UnitMB  = "MB"
	UnitGB  = "GB"
	UnitTB  = "TB"
	UnitMiB = "MiB"
	UnitGiB = "GiB"
	UnitTiB = "TiB"

	BytesPerMB uint64 = 1 << 20
	BytesPerGB uint64 = 1 << 30
	BytesPerTB uint64 = 1 << 40
)

type ResetType string

const (
	ResetNone      ResetType = "none"
	ResetMonthly   ResetType = "monthly"
	ResetFixed     ResetType = "fixed"
	ResetFixedDate ResetType = "fixed_date"
)

// WatchItem defines a custom multi-dimensional traffic watch rule and optional quota.
type WatchItem struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name"`
	Matchers       WatchMatcherMap `json:"matchers"`
	OverrideFactor *float64        `json:"override_factor,omitempty"`
	QuotaValue     float64         `json:"quota_value"`
	QuotaUnit      string          `json:"quota_unit"`
	QuotaBytes     uint64          `json:"quota_bytes"`
	ResetType      ResetType       `json:"reset_type"`
	ResetDay       int             `json:"reset_day"`
	ExpiresAt      string          `json:"expires_at,omitempty"`
	Pinned         bool            `json:"pinned"`
	Enabled        bool            `json:"enabled"`
	CreatedAt      string          `json:"created_at,omitempty"`
	UpdatedAt      string          `json:"updated_at,omitempty"`
}

// UnitMultiplier returns the binary byte multiplier and canonical unit label
// for B, MB/MiB, GB/GiB, and TB/TiB.
func UnitMultiplier(unit string) (uint64, string, error) {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case UnitB:
		return 1, UnitB, nil
	case UnitMB:
		return BytesPerMB, UnitMB, nil
	case "MIB":
		return BytesPerMB, UnitMiB, nil
	case UnitGB:
		return BytesPerGB, UnitGB, nil
	case "GIB":
		return BytesPerGB, UnitGiB, nil
	case UnitTB:
		return BytesPerTB, UnitTB, nil
	case "TIB":
		return BytesPerTB, UnitTiB, nil
	default:
		return 0, "", fmt.Errorf("unsupported quota unit %q", unit)
	}
}

// NormalizeWatchUnit validates and canonicalizes watchlist quota units (MB, GB, TB).
func NormalizeWatchUnit(unit string) (string, error) {
	trimmed := strings.TrimSpace(unit)
	if trimmed == "" {
		return UnitGB, nil
	}
	_, canonical, err := UnitMultiplier(trimmed)
	if err != nil {
		return "", err
	}
	switch canonical {
	case UnitMB, UnitGB, UnitTB:
		return canonical, nil
	default:
		return "", fmt.Errorf("watchlist quota_unit must be MB, GB, or TB, got %q", unit)
	}
}

// QuotaToBytes converts a non-negative finite quota value and unit to binary bytes.
func QuotaToBytes(value float64, unit string) (uint64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, errors.New("quota value must be a finite non-negative number")
	}
	mult, _, err := UnitMultiplier(unit)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return 0, nil
	}
	scaled := value * float64(mult)
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled >= float64(math.MaxInt64) {
		return 0, errors.New("quota bytes exceeds int64 range")
	}
	bytes := uint64(math.Round(scaled))
	if bytes > math.MaxInt64 {
		return 0, errors.New("quota bytes exceeds int64 range")
	}
	return bytes, nil
}

// BytesToUnit converts bytes into the specified unit.
func BytesToUnit(bytes uint64, unit string) (float64, error) {
	mult, _, err := UnitMultiplier(unit)
	if err != nil {
		return 0, err
	}
	return float64(bytes) / float64(mult), nil
}

// ParseExpiresAt parses RFC3339Nano, RFC3339, or YYYY-MM-DD (interpreted as end-of-day UTC).
func ParseExpiresAt(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return t.UTC(), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", trimmed, time.UTC); err == nil {
		return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 999999999, time.UTC), nil
	}
	return time.Time{}, fmt.Errorf("invalid expires_at timestamp %q", raw)
}

// ValidateWatchItem validates and normalizes a WatchItem.
func ValidateWatchItem(w *WatchItem) error {
	if w == nil {
		return errors.New("watch item is required")
	}
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" {
		return errors.New("watch item name is required")
	}
	if len(w.Matchers) == 0 {
		return errors.New("at least one dimension matcher is required")
	}

	normalizedMatchers := make(WatchMatcherMap, len(w.Matchers))
	dims := make([]WatchDimension, 0, len(w.Matchers))
	for dim := range w.Matchers {
		dims = append(dims, dim)
	}
	sort.Slice(dims, func(i, j int) bool { return dims[i] < dims[j] })

	for _, dim := range dims {
		if !IsValidWatchDimension(dim) {
			return fmt.Errorf("unsupported watch dimension %q", dim)
		}
		m := w.Matchers[dim]
		switch m.Type {
		case MatchExact, MatchKeyword, MatchRegex:
		default:
			return fmt.Errorf("unsupported matcher type %q for dimension %q", m.Type, dim)
		}
		if len(m.Values) == 0 {
			return fmt.Errorf("matcher values are required for dimension %q", dim)
		}
		seen := make(map[string]struct{}, len(m.Values))
		cleanVals := make([]string, 0, len(m.Values))
		for _, rawVal := range m.Values {
			val := strings.TrimSpace(rawVal)
			if val == "" {
				continue
			}
			if m.Type == MatchRegex {
				val = rawVal
				if _, err := regexp.Compile(val); err != nil {
					return fmt.Errorf("invalid regex %q in dimension %q: %w", val, dim, err)
				}
			} else if dim == WatchDimDomain && m.Type == MatchExact {
				val = strings.ToLower(strings.TrimRight(val, "."))
				if val == "" {
					return fmt.Errorf("empty domain value in dimension %q", dim)
				}
			}
			if _, dup := seen[val]; dup {
				continue
			}
			seen[val] = struct{}{}
			cleanVals = append(cleanVals, val)
		}
		if len(cleanVals) == 0 {
			return fmt.Errorf("matcher values are required for dimension %q", dim)
		}
		normalizedMatchers[dim] = DimensionMatcher{
			Type:   m.Type,
			Values: cleanVals,
		}
	}
	w.Matchers = normalizedMatchers

	if w.OverrideFactor != nil {
		f := *w.OverrideFactor
		if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
			return errors.New("override_factor must be a finite number greater than 0")
		}
	}

	if math.IsNaN(w.QuotaValue) || math.IsInf(w.QuotaValue, 0) || w.QuotaValue < 0 {
		return errors.New("quota_value must be a finite non-negative number")
	}
	unit, err := NormalizeWatchUnit(w.QuotaUnit)
	if err != nil {
		return err
	}
	w.QuotaUnit = unit
	if w.QuotaBytes > math.MaxInt64 {
		return errors.New("quota_bytes exceeds int64 range")
	}
	if w.QuotaValue > 0 {
		bytes, err := QuotaToBytes(w.QuotaValue, w.QuotaUnit)
		if err != nil {
			return err
		}
		w.QuotaBytes = bytes
	} else if w.QuotaBytes > 0 {
		val, err := BytesToUnit(w.QuotaBytes, w.QuotaUnit)
		if err != nil {
			return err
		}
		w.QuotaValue = val
	} else {
		w.QuotaBytes = 0
	}

	rt := ResetType(strings.ToLower(strings.TrimSpace(string(w.ResetType))))
	if rt == "" {
		rt = ResetNone
	}
	switch rt {
	case ResetNone:
		w.ResetType = ResetNone
		w.ResetDay = 0
	case ResetMonthly:
		if w.ResetDay < 1 || w.ResetDay > 28 {
			return errors.New("reset_day must be between 1 and 28 for monthly reset_type")
		}
		w.ResetType = ResetMonthly
	case ResetFixed, ResetFixedDate:
		w.ResetType = rt
		w.ResetDay = 0
	default:
		return fmt.Errorf("unsupported reset_type %q", w.ResetType)
	}

	w.ExpiresAt = strings.TrimSpace(w.ExpiresAt)
	if w.ExpiresAt != "" {
		if _, err := ParseExpiresAt(w.ExpiresAt); err != nil {
			return err
		}
	}

	return nil
}

// CompiledDimensionMatcher matches candidate keys within one dimension using OR across Values.
type CompiledDimensionMatcher struct {
	dim         WatchDimension
	matchType   MatchType
	exactSet    map[string]struct{}
	lowerExact  map[string]struct{}
	lowerValues []string
	regexes     []*regexp.Regexp
}

// CompileDimensionMatcher compiles a single DimensionMatcher.
func CompileDimensionMatcher(dim WatchDimension, m DimensionMatcher) (*CompiledDimensionMatcher, error) {
	if !IsValidWatchDimension(dim) {
		return nil, fmt.Errorf("unsupported watch dimension %q", dim)
	}
	cm := &CompiledDimensionMatcher{
		dim:       dim,
		matchType: m.Type,
	}
	switch m.Type {
	case MatchExact:
		cm.exactSet = make(map[string]struct{}, len(m.Values))
		cm.lowerExact = make(map[string]struct{}, len(m.Values))
		for _, v := range m.Values {
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				continue
			}
			if dim == WatchDimDomain {
				norm := strings.ToLower(strings.TrimRight(trimmed, "."))
				if norm == "" {
					continue
				}
				cm.exactSet[norm] = struct{}{}
				cm.lowerExact[norm] = struct{}{}
			} else {
				cm.exactSet[trimmed] = struct{}{}
			}
		}
	case MatchKeyword:
		cm.lowerValues = make([]string, 0, len(m.Values))
		for _, v := range m.Values {
			lower := strings.ToLower(strings.TrimSpace(v))
			if lower != "" {
				cm.lowerValues = append(cm.lowerValues, lower)
			}
		}
	case MatchRegex:
		cm.regexes = make([]*regexp.Regexp, 0, len(m.Values))
		for _, v := range m.Values {
			if strings.TrimSpace(v) == "" {
				continue
			}
			re, err := regexp.Compile(v)
			if err != nil {
				return nil, fmt.Errorf("compile regex %q: %w", v, err)
			}
			cm.regexes = append(cm.regexes, re)
		}
	default:
		return nil, fmt.Errorf("unsupported matcher type %q", m.Type)
	}
	return cm, nil
}

// Match returns true if candidate matches any configured pattern in this dimension.
func (cm *CompiledDimensionMatcher) Match(candidate string) bool {
	if cm == nil {
		return false
	}
	switch cm.matchType {
	case MatchExact:
		if cm.dim == WatchDimDomain {
			norm := strings.ToLower(strings.TrimRight(strings.TrimSpace(candidate), "."))
			if _, ok := cm.lowerExact[norm]; ok {
				return true
			}
			for target := range cm.lowerExact {
				if strings.HasSuffix(norm, "."+target) {
					return true
				}
			}
			return false
		}
		_, ok := cm.exactSet[candidate]
		return ok
	case MatchKeyword:
		lower := strings.ToLower(candidate)
		for _, kw := range cm.lowerValues {
			if strings.Contains(lower, kw) {
				return true
			}
		}
		return false
	case MatchRegex:
		for _, re := range cm.regexes {
			if re.MatchString(candidate) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// CompiledWatchItem holds precompiled matchers for a WatchItem.
type CompiledWatchItem struct {
	Item     WatchItem
	Matchers map[WatchDimension]*CompiledDimensionMatcher
}

// CompileWatchItem validates and compiles all matchers of a WatchItem.
func CompileWatchItem(item WatchItem) (*CompiledWatchItem, error) {
	if err := ValidateWatchItem(&item); err != nil {
		return nil, err
	}
	compiled := make(map[WatchDimension]*CompiledDimensionMatcher, len(item.Matchers))
	for dim, m := range item.Matchers {
		cm, err := CompileDimensionMatcher(dim, m)
		if err != nil {
			return nil, err
		}
		compiled[dim] = cm
	}
	return &CompiledWatchItem{
		Item:     item,
		Matchers: compiled,
	}, nil
}
