package accounting

import "math"

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
