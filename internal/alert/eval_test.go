package alert

import (
	"testing"
	"time"
)

func TestEvalGrowthUsesSevenDayMedian(t *testing.T) {
	record, fired := EvalGrowth(400, 100, GrowthConfig{Multiplier: 3})
	if !fired || record.Kind != KindGrowth {
		t.Fatalf("growth alert = (%+v, %v)", record, fired)
	}
}

func TestEvalSilenceUsesThreeIntervals(t *testing.T) {
	now := parseTime(t, "2026-08-19T12:00:00Z")
	last := now.Add(-46 * time.Minute)
	_, fired := EvalSilence(last, now, 15*time.Minute)
	if !fired {
		t.Fatal("expected silence alert")
	}
}

func TestEvalResetDetectsBootOrGap(t *testing.T) {
	record, fired := EvalReset(true, "")
	if !fired || record.Kind != KindReset {
		t.Fatalf("reset alert = (%+v, %v)", record, fired)
	}
}

func TestEvalDriftKeepsLargeErrorVisible(t *testing.T) {
	record, fired := EvalDrift(120, 100, DriftConfig{TargetRatio: 0.10})
	if !fired || record.Kind != KindDrift {
		t.Fatalf("drift alert = (%+v, %v)", record, fired)
	}
	if _, exactBoundary := EvalDrift(110, 100, DriftConfig{TargetRatio: 0.10}); exactBoundary {
		t.Fatal("exact 0.10 boundary should not fire drift alert")
	}
}

func TestEvalQuota(t *testing.T) {
	if _, fired := EvalQuota(100, 100, "AI"); fired {
		t.Fatal("exact quota boundary should not fire")
	}
	if _, fired := EvalQuota(100, 0, "AI"); fired {
		t.Fatal("zero quota should not fire")
	}
	rec, fired := EvalQuota(101, 100, "AI")
	if !fired || rec.Kind != KindQuota {
		t.Fatalf("quota alert = (%+v, %v)", rec, fired)
	}
}

func parseTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
