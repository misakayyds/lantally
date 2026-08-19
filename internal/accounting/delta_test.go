package accounting

import "testing"

func TestDeltaUint64(t *testing.T) {
	tests := []struct {
		name      string
		prev      uint64
		cur       uint64
		wantDelta uint64
		wantReset bool
	}{
		{name: "increase", prev: 10, cur: 25, wantDelta: 15},
		{name: "unchanged", prev: 25, cur: 25},
		{name: "decrease resets", prev: 25, cur: 10, wantReset: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delta, reset := DeltaUint64(tt.prev, tt.cur)
			if delta != tt.wantDelta || reset != tt.wantReset {
				t.Fatalf("DeltaUint64(%d, %d) = (%d, %v), want (%d, %v)",
					tt.prev, tt.cur, delta, reset, tt.wantDelta, tt.wantReset)
			}
		})
	}
}
