package accounting

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ApplyMultiplier scales raw bytes using configured outbound multipliers.
// Unknown outbound names leave the value unchanged and set unadjusted=true.
func ApplyMultiplier(
	raw uint64,
	outbound string,
	rules map[string]float64,
) (adjusted uint64, unadjusted bool) {
	if rules == nil {
		return raw, true
	}
	multiplier, ok := rules[outbound]
	if !ok || multiplier <= 0 {
		return raw, true
	}
	scaled := float64(raw) * multiplier
	if scaled >= float64(math.MaxUint64) {
		return math.MaxUint64, false
	}
	return uint64(scaled), false
}

type MatchType string

const (
	MatchKeyword     MatchType = "keyword"
	MatchRegex       MatchType = "regex"
	MatchExact       MatchType = "exact"
	MatchAutoExtract MatchType = "auto_extract"
)

type MatchSource string

const (
	SourceRule    MatchSource = "rule"
	SourceDefault MatchSource = "default"
)

type MultiplierRule struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	MatchType MatchType `json:"match_type"`
	Pattern   string    `json:"pattern"`
	Factor    float64   `json:"factor"`
	Priority  int       `json:"priority"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt string    `json:"updated_at,omitempty"`
}

type MultiplierMatch struct {
	Factor          float64     `json:"factor"`
	MatchSource     MatchSource `json:"match_source"`
	MatchedRule     string      `json:"matched_rule,omitempty"`
	MatchedRuleID   int64       `json:"matched_rule_id,omitempty"`
	MatchedRuleName string      `json:"matched_rule_name,omitempty"`
	MatchedType     MatchType   `json:"matched_type,omitempty"`
}

// ValidateRule validates and normalizes a MultiplierRule before compilation or storage.
func ValidateRule(r *MultiplierRule) error {
	if r == nil {
		return errors.New("rule is required")
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errors.New("rule name is required")
	}
	if math.IsNaN(r.Factor) || math.IsInf(r.Factor, 0) || r.Factor < 0 {
		return errors.New("factor must be a finite non-negative number")
	}
	switch r.MatchType {
	case MatchExact:
		if strings.TrimSpace(r.Pattern) == "" {
			return errors.New("pattern is required for exact rule")
		}
	case MatchKeyword:
		r.Pattern = strings.TrimSpace(r.Pattern)
		if r.Pattern == "" {
			return errors.New("pattern is required for keyword rule")
		}
	case MatchRegex:
		if strings.TrimSpace(r.Pattern) == "" {
			return errors.New("pattern is required for regex rule")
		}
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}
	case MatchAutoExtract:
		if r.Factor == 0 {
			r.Factor = 1.0
		}
		if strings.TrimSpace(r.Pattern) != "" {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				return fmt.Errorf("invalid auto_extract regex pattern: %w", err)
			}
			if re.NumSubexp() < 1 {
				return errors.New("auto_extract regex pattern must include at least one capture group")
			}
		}
	default:
		return fmt.Errorf("unsupported match_type %q", r.MatchType)
	}
	return nil
}

type compiledRule struct {
	rule         MultiplierRule
	lowerPattern string
	re           *regexp.Regexp
}

// CompiledRuleSet evaluates multiplier rules in deterministic priority order.
type CompiledRuleSet struct {
	rules []compiledRule
}

// CompileRules validates, compiles, and sorts enabled rules (priority DESC, id ASC, name ASC).
func CompileRules(rules []MultiplierRule) (*CompiledRuleSet, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for _, raw := range rules {
		r := raw
		if err := ValidateRule(&r); err != nil {
			return nil, err
		}
		if !r.Enabled {
			continue
		}
		cr := compiledRule{rule: r}
		switch r.MatchType {
		case MatchKeyword:
			cr.lowerPattern = strings.ToLower(r.Pattern)
		case MatchRegex:
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				return nil, fmt.Errorf("compile regex %q: %w", r.Pattern, err)
			}
			cr.re = re
		case MatchAutoExtract:
			if strings.TrimSpace(r.Pattern) != "" {
				re, err := regexp.Compile(r.Pattern)
				if err != nil {
					return nil, fmt.Errorf("compile auto_extract regex %q: %w", r.Pattern, err)
				}
				cr.re = re
			}
		}
		compiled = append(compiled, cr)
	}
	sort.Slice(compiled, func(i, j int) bool {
		if compiled[i].rule.Priority != compiled[j].rule.Priority {
			return compiled[i].rule.Priority > compiled[j].rule.Priority
		}
		if compiled[i].rule.ID != compiled[j].rule.ID {
			return compiled[i].rule.ID < compiled[j].rule.ID
		}
		return compiled[i].rule.Name < compiled[j].rule.Name
	})
	return &CompiledRuleSet{rules: compiled}, nil
}

// Match returns the highest-priority matching rule for outbound, or a 1.0x default match.
func (cs *CompiledRuleSet) Match(outbound string) MultiplierMatch {
	if cs != nil {
		lowerOutbound := strings.ToLower(outbound)
		for _, cr := range cs.rules {
			switch cr.rule.MatchType {
			case MatchExact:
				if outbound == cr.rule.Pattern {
					return ruleMatch(cr.rule, cr.rule.Factor)
				}
			case MatchKeyword:
				if strings.Contains(lowerOutbound, cr.lowerPattern) {
					return ruleMatch(cr.rule, cr.rule.Factor)
				}
			case MatchRegex:
				if cr.re != nil && cr.re.MatchString(outbound) {
					return ruleMatch(cr.rule, cr.rule.Factor)
				}
			case MatchAutoExtract:
				if cr.re != nil {
					m := cr.re.FindStringSubmatch(outbound)
					if len(m) >= 2 {
						if factor, ok := parseValidFactor(m[1]); ok {
							return ruleMatch(cr.rule, factor)
						}
					}
					continue
				}
				if factor, ok := ExtractAutoMultiplier(outbound); ok {
					return ruleMatch(cr.rule, factor)
				}
			}
		}
	}
	return MultiplierMatch{
		Factor:      1.0,
		MatchSource: SourceDefault,
	}
}

func ruleMatch(r MultiplierRule, factor float64) MultiplierMatch {
	return MultiplierMatch{
		Factor:          factor,
		MatchSource:     SourceRule,
		MatchedRule:     r.Name,
		MatchedRuleID:   r.ID,
		MatchedRuleName: r.Name,
		MatchedType:     r.MatchType,
	}
}

// ScaleBytes multiplies raw bytes by factor, rounding to nearest uint64 and clamping overflow.
func ScaleBytes(raw uint64, factor float64) uint64 {
	if raw == 0 || factor <= 0 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		return 0
	}
	if factor == 1.0 {
		return raw
	}
	scaled := math.Round(float64(raw) * factor)
	if scaled <= 0 {
		return 0
	}
	if scaled >= float64(math.MaxUint64) {
		return math.MaxUint64
	}
	return uint64(scaled)
}

var (
	reAutoPrefixCJK    = regexp.MustCompile(`(?:倍率)\s*[:：=]?\s*(\d+(?:\.\d+)?)`)
	reAutoSuffixCJK    = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:倍率|倍)`)
	reAutoPrefixASCIIX = regexp.MustCompile(`[xX](\d+(?:\.\d+)?)`)
	reAutoSuffixASCIIX = regexp.MustCompile(`(\d+(?:\.\d+)?)[xX]`)
	reAutoPrefixMul    = regexp.MustCompile(`×\s*(\d+(?:\.\d+)?)`)
	reAutoSuffixMul    = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*×`)
)

type autoCandidate struct {
	start  int
	factor float64
}

type autoBoundaryKind int

const (
	boundCJKPrefix autoBoundaryKind = iota
	boundCJKSuffix
	boundASCIIX
	boundMulPrefix
	boundMulSuffix
)

// ExtractAutoMultiplier extracts an explicit multiplier factor (0..100) from a proxy node name
// while guarding against common false positives (hex literals, resolutions, IP octets, codecs).
func ExtractAutoMultiplier(nodeName string) (float64, bool) {
	patterns := []struct {
		re   *regexp.Regexp
		kind autoBoundaryKind
	}{
		{re: reAutoPrefixCJK, kind: boundCJKPrefix},
		{re: reAutoSuffixCJK, kind: boundCJKSuffix},
		{re: reAutoPrefixASCIIX, kind: boundASCIIX},
		{re: reAutoSuffixASCIIX, kind: boundASCIIX},
		{re: reAutoPrefixMul, kind: boundMulPrefix},
		{re: reAutoSuffixMul, kind: boundMulSuffix},
	}

	var best *autoCandidate
	for _, p := range patterns {
		matches := p.re.FindAllStringSubmatchIndex(nodeName, -1)
		for _, idx := range matches {
			if len(idx) < 4 {
				continue
			}
			mStart, mEnd := idx[0], idx[1]
			nStart, nEnd := idx[2], idx[3]
			if nStart < 0 || nEnd <= nStart {
				continue
			}
			if !autoBoundariesOK(nodeName, mStart, mEnd, p.kind) {
				continue
			}
			token := strings.ToLower(nodeName[mStart:mEnd])
			if p.kind == boundASCIIX && (token == "x86" || token == "x64" || token == "x264" || token == "x265") {
				continue
			}
			factor, ok := parseValidFactor(nodeName[nStart:nEnd])
			if !ok {
				continue
			}
			if best == nil || mStart < best.start {
				best = &autoCandidate{start: mStart, factor: factor}
			}
		}
	}
	if best == nil {
		return 0, false
	}
	return best.factor, true
}

func autoBoundariesOK(s string, mStart, mEnd int, kind autoBoundaryKind) bool {
	if mStart > 0 {
		prev := s[mStart-1]
		switch kind {
		case boundASCIIX:
			if isASCIIAlphaNum(prev) {
				return false
			}
			if prev == '.' && isASCIIDigit(s[mStart]) {
				return false
			}
		case boundCJKSuffix, boundMulSuffix, boundMulPrefix:
			if isASCIIDigit(prev) {
				return false
			}
			if prev == '.' && isASCIIDigit(s[mStart]) {
				return false
			}
		}
	}
	if mEnd < len(s) {
		next := s[mEnd]
		switch kind {
		case boundASCIIX, boundCJKPrefix, boundMulPrefix:
			if isASCIIAlphaNum(next) {
				return false
			}
			if next == '.' && isASCIIDigit(s[mEnd-1]) && mEnd+1 < len(s) && isASCIIDigit(s[mEnd+1]) {
				return false
			}
		case boundCJKSuffix, boundMulSuffix:
			if isASCIIDigit(next) || next == '.' {
				return false
			}
		}
	}
	return true
}

func parseValidFactor(raw string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return 0, false
	}
	return v, true
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isASCIIAlphaNum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
