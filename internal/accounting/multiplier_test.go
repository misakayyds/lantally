package accounting

import (
	"math"
	"testing"
)

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

func TestExtractAutoMultiplier(t *testing.T) {
	positives := []struct {
		name string
		want float64
	}{
		{"x2", 2.0},
		{"2.0x", 2.0},
		{"×1.5", 1.5},
		{"Node×2", 2.0},
		{"2×HK", 2.0},
		{"倍率0.5", 0.5},
		{"倍率:2", 2.0},
		{"1.5倍", 1.5},
		{"HK-01 [0.5x]", 0.5},
		{"JP-0x", 0.0},
		{"HK-01 X1.5", 1.5},
		{"US 2.0X", 2.0},
		{"SG × 3", 3.0},
		{"TW 1.25 倍率", 1.25},
		{"HK 2倍率0.5", 0.5},
		{"HK 10倍率0.5", 0.5},
		{"Node 2倍0.5x", 0.5},
		{"HK.x2", 2.0},
	}
	for _, tc := range positives {
		got, ok := ExtractAutoMultiplier(tc.name)
		if !ok || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("ExtractAutoMultiplier(%q) = (%v, %v), want (%v, true)", tc.name, got, ok, tc.want)
		}
	}

	negatives := []string{
		"proxy-20",
		"max2",
		"box1",
		"tx100",
		"rx200",
		"0x10",
		"0x2",
		"x86",
		"x64",
		"x264",
		"x265",
		"192.168.1.2",
		"1.2.3x",
		"x1.2.3",
		"1920x1080",
		"1920×1080",
		"Node X 2",
		"HK 2 X",
		"2xtreme",
		"x2026",
		"HK .5x",
		".5x",
		"[.25x]",
		"v.5x",
		"HK .5倍",
		"HK 2倍3",
	}
	for _, name := range negatives {
		if got, ok := ExtractAutoMultiplier(name); ok {
			t.Errorf("ExtractAutoMultiplier(%q) = (%v, true), want false", name, got)
		}
	}
}

func TestCompiledRuleSetPriorityAndTieBreak(t *testing.T) {
	rules := []MultiplierRule{
		{ID: 5, Name: "disabled-exact", MatchType: MatchExact, Pattern: "HK-IEPL-2x", Factor: 9.0, Priority: 999, Enabled: false},
		{ID: 2, Name: "auto", MatchType: MatchAutoExtract, Pattern: "", Factor: 1.0, Priority: 50, Enabled: true},
		{ID: 3, Name: "kw-b", MatchType: MatchKeyword, Pattern: "iepl", Factor: 3.0, Priority: 100, Enabled: true},
		{ID: 1, Name: "kw-a", MatchType: MatchKeyword, Pattern: "iepl", Factor: 2.5, Priority: 100, Enabled: true},
		{ID: 4, Name: "regex-vip", MatchType: MatchRegex, Pattern: `^VIP-\d+$`, Factor: 0.0, Priority: 80, Enabled: true},
	}
	cs, err := CompileRules(rules)
	if err != nil {
		t.Fatal(err)
	}

	// Priority 100 ID 1 wins tie-break over ID 3; disabled rule ignored.
	m1 := cs.Match("HK-IEPL-2x")
	if m1.MatchSource != SourceRule || m1.MatchedRuleID != 1 || m1.MatchedRuleName != "kw-a" || m1.Factor != 2.5 {
		t.Fatalf("tie-break match = %+v", m1)
	}

	// auto_extract succeeds on "JP-0.5x"
	m2 := cs.Match("JP-0.5x")
	if m2.MatchSource != SourceRule || m2.MatchedRuleID != 2 || m2.Factor != 0.5 {
		t.Fatalf("auto_extract match = %+v", m2)
	}

	// auto_extract non-match falls through to default 1.0x
	m3 := cs.Match("proxy-20")
	if m3.MatchSource != SourceDefault || m3.Factor != 1.0 || m3.MatchedRuleID != 0 || m3.MatchedRuleName != "" {
		t.Fatalf("default match = %+v", m3)
	}

	// 0x free node via regex
	m4 := cs.Match("VIP-1")
	if m4.MatchSource != SourceRule || m4.Factor != 0.0 {
		t.Fatalf("zero factor match = %+v", m4)
	}
	if ScaleBytes(1000, m4.Factor) != 0 {
		t.Fatalf("ScaleBytes(1000, 0) != 0")
	}
}

func TestValidateRule(t *testing.T) {
	bad := []MultiplierRule{
		{Name: "", MatchType: MatchKeyword, Pattern: "hk", Factor: 1.0, Enabled: true},
		{Name: "bad-type", MatchType: "glob", Pattern: "*", Factor: 1.0, Enabled: true},
		{Name: "empty-kw", MatchType: MatchKeyword, Pattern: "   ", Factor: 1.0, Enabled: true},
		{Name: "empty-exact", MatchType: MatchExact, Pattern: "", Factor: 1.0, Enabled: true},
		{Name: "bad-regex", MatchType: MatchRegex, Pattern: "[", Factor: 1.0, Enabled: true},
		{Name: "no-group-auto", MatchType: MatchAutoExtract, Pattern: `x\d+`, Factor: 1.0, Enabled: true},
		{Name: "neg-factor", MatchType: MatchKeyword, Pattern: "hk", Factor: -0.5, Enabled: true},
		{Name: "nan-factor", MatchType: MatchKeyword, Pattern: "hk", Factor: math.NaN(), Enabled: true},
		{Name: "inf-factor", MatchType: MatchKeyword, Pattern: "hk", Factor: math.Inf(1), Enabled: true},
	}
	for _, r := range bad {
		rc := r
		if err := ValidateRule(&rc); err == nil {
			t.Errorf("expected ValidateRule(%+v) to fail", r)
		}
	}

	goodZero := MultiplierRule{Name: "free", MatchType: MatchKeyword, Pattern: "free", Factor: 0, Enabled: true}
	if err := ValidateRule(&goodZero); err != nil {
		t.Fatalf("0x factor should be allowed: %v", err)
	}
	goodAuto := MultiplierRule{Name: "auto", MatchType: MatchAutoExtract, Pattern: "", Factor: 0, Enabled: true}
	if err := ValidateRule(&goodAuto); err != nil || goodAuto.Factor != 1.0 {
		t.Fatalf("auto_extract default factor = %+v err=%v", goodAuto, err)
	}
}

func TestScaleBytesAndCompiledRuleSetCustomRegexAutoExtract(t *testing.T) {
	if got := ScaleBytes(12345, 1.0); got != 12345 {
		t.Fatalf("ScaleBytes(12345, 1.0) = %d", got)
	}
	if got := ScaleBytes(0, 2.5); got != 0 {
		t.Fatalf("ScaleBytes(0, 2.5) = %d", got)
	}
	for _, badFactor := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := ScaleBytes(1000, badFactor); got != 0 {
			t.Fatalf("ScaleBytes(1000, %v) = %d, want 0", badFactor, got)
		}
	}
	if got := ScaleBytes(3, 0.5); got != 2 {
		t.Fatalf("ScaleBytes(3, 0.5) = %d, want 2 (math.Round tie)", got)
	}
	if got := ScaleBytes(math.MaxUint64, 2.0); got != math.MaxUint64 {
		t.Fatalf("ScaleBytes overflow = %d, want MaxUint64", got)
	}

	var nilSet *CompiledRuleSet
	if m := nilSet.Match("HK-01"); m.MatchSource != SourceDefault || m.Factor != 1.0 {
		t.Fatalf("nil CompiledRuleSet.Match = %+v", m)
	}

	cs, err := CompileRules([]MultiplierRule{
		{ID: 1, Name: "custom-rate", MatchType: MatchAutoExtract, Pattern: `rate=(\d+(?:\.\d+)?)`, Factor: 1.0, Priority: 100, Enabled: true},
		{ID: 2, Name: "exact-vip", MatchType: MatchExact, Pattern: "VIP-Node", Factor: 0.25, Priority: 90, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m := cs.Match("SG rate=3.5"); m.MatchSource != SourceRule || m.MatchedRuleID != 1 || m.Factor != 3.5 {
		t.Fatalf("custom auto_extract match = %+v", m)
	}
	if m := cs.Match("SG rate=999"); m.MatchSource != SourceDefault {
		t.Fatalf("out-of-range auto_extract factor should fall through to default, got %+v", m)
	}
	if m := cs.Match("VIP-Node"); m.MatchSource != SourceRule || m.MatchedRuleID != 2 || m.Factor != 0.25 {
		t.Fatalf("exact match = %+v", m)
	}

	// Trailing-dot symmetry: both Node-2x. and Node-x2. extract 2.0, while x1.2.3 remains rejected.
	for _, sym := range []string{"Node-2x.", "Node-x2."} {
		if f, ok := ExtractAutoMultiplier(sym); !ok || f != 2.0 {
			t.Fatalf("ExtractAutoMultiplier(%q) = (%v, %v), want (2.0, true)", sym, f, ok)
		}
	}
	if f, ok := ExtractAutoMultiplier("x1.2.3"); ok {
		t.Fatalf("ExtractAutoMultiplier(x1.2.3) = (%v, true), want false", f)
	}
}
