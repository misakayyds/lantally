package accounting

import "testing"

func TestApplyMultiplierUsesConfiguredOutbound(t *testing.T) {
	adjusted, unadjusted := ApplyMultiplier(1000, "ss-test", map[string]float64{"ss-test": 1.5})
	if unadjusted {
		t.Fatal("expected adjusted outbound")
	}
	if adjusted != 1500 {
		t.Fatalf("adjusted = %d, want 1500", adjusted)
	}
}

func TestApplyMultiplierMarksUnknownOutbound(t *testing.T) {
	adjusted, unadjusted := ApplyMultiplier(1000, "missing", map[string]float64{"ss-test": 1.5})
	if !unadjusted {
		t.Fatal("expected unknown outbound to remain unadjusted")
	}
	if adjusted != 1000 {
		t.Fatalf("adjusted = %d, want raw 1000", adjusted)
	}
}

func TestApplyMultiplierRejectsNilRules(t *testing.T) {
	adjusted, unadjusted := ApplyMultiplier(1000, "ss-test", nil)
	if !unadjusted || adjusted != 1000 {
		t.Fatalf("nil rules = (%d, %v), want (1000, true)", adjusted, unadjusted)
	}
}
