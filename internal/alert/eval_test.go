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
}

func parseTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
