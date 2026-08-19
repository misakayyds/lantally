package alert

import (
	"math"
	"time"
)

type Kind string

const (
	KindGrowth  Kind = "growth"
	KindSilence Kind = "silence"
	KindReset   Kind = "reset"
	KindDrift   Kind = "drift"
)

type Record struct {
	Kind        Kind
	SiteID      string
	NodeID      string
	DeviceID    string
	Message     string
	ObservedAt  time.Time
	Fingerprint string
}

type GrowthConfig struct {
	Multiplier float64
}

type SilenceConfig struct {
	Interval time.Duration
}

type DriftConfig struct {
	TargetRatio float64
}

func DefaultGrowthConfig() GrowthConfig {
	return GrowthConfig{Multiplier: 3}
}

func DefaultDriftConfig() DriftConfig {
	return DriftConfig{TargetRatio: 0.10}
}

// EvalGrowth fires when daily bytes exceed multiplier × seven-day median.
func EvalGrowth(dailyBytes float64, median7d float64, cfg GrowthConfig) (Record, bool) {
	if cfg.Multiplier <= 0 {
		cfg = DefaultGrowthConfig()
	}
	if median7d <= 0 {
		return Record{}, false
	}
	threshold := cfg.Multiplier * median7d
	if dailyBytes <= threshold {
		return Record{}, false
	}
	return Record{
		Kind:    KindGrowth,
		Message: "daily bytes exceeded configured growth threshold",
	}, true
}

// EvalSilence fires when the node has not reported within three intervals.
func EvalSilence(lastReport, now time.Time, interval time.Duration) (Record, bool) {
	if interval <= 0 {
		return Record{}, false
	}
	if now.Sub(lastReport) <= 3*interval {
		return Record{}, false
	}
	return Record{
		Kind:    KindSilence,
		Message: "node stopped reporting within expected interval window",
	}, true
}

// EvalReset fires on boot-id changes or explicit collector gaps.
func EvalReset(newBoot bool, gapReason string) (Record, bool) {
	if !newBoot && gapReason == "" {
		return Record{}, false
	}
	message := "collector or node reset detected"
	if gapReason != "" {
		message = "collector gap detected: " + gapReason
	}
	return Record{Kind: KindReset, Message: message}, true
}

// EvalDrift compares adjusted and provider totals; larger-than-target drift stays visible.
func EvalDrift(adjusted, provider float64, cfg DriftConfig) (Record, bool) {
	if provider <= 0 {
		return Record{}, false
	}
	if cfg.TargetRatio <= 0 {
		cfg = DefaultDriftConfig()
	}
	ratio := math.Abs(adjusted-provider) / provider
	if ratio <= cfg.TargetRatio {
		return Record{}, false
	}
	return Record{
		Kind:    KindDrift,
		Message: "provider drift exceeds configured target",
	}, true
}
