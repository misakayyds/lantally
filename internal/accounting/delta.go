// Package accounting contains shared counter accounting primitives.
package accounting

// DeltaUint64 returns the unsigned increase between two cumulative counters.
// A decrease indicates a reset and never produces a negative or wrapped delta.
func DeltaUint64(prev, cur uint64) (delta uint64, reset bool) {
	if cur < prev {
		return 0, true
	}
	return cur - prev, false
}
