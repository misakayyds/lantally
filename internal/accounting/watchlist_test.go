package accounting

import (
	"math"
	"testing"
)

func TestQuotaUnitsAndConversions(t *testing.T) {
	if got, err := QuotaToBytes(1, "MB"); err != nil || got != 1048576 {
		t.Fatalf("1 MB = %d, err=%v; want 1048576", got, err)
	}
	if got, err := QuotaToBytes(1, "GB"); err != nil || got != 1073741824 {
		t.Fatalf("1 GB = %d, err=%v; want 1073741824", got, err)
	}
	if got, err := QuotaToBytes(1, "TB"); err != nil || got != 1099511627776 {
		t.Fatalf("1 TB = %d, err=%v; want 1099511627776", got, err)
	}
	if got, err := QuotaToBytes(2.5, "MiB"); err != nil || got != 2621440 {
		t.Fatalf("2.5 MiB = %d, err=%v; want 2621440", got, err)
	}
	if got, err := QuotaToBytes(512, "B"); err != nil || got != 512 {
		t.Fatalf("512 B = %d, err=%v; want 512", got, err)
	}
	if v, err := BytesToUnit(1073741824, "GB"); err != nil || v != 1.0 {
		t.Fatalf("BytesToUnit(1GiB, GB) = %v, err=%v", v, err)
	}
	if _, err := QuotaToBytes(-1, "GB"); err == nil {
		t.Fatal("expected error for negative quota")
	}
	if _, err := QuotaToBytes(math.NaN(), "GB"); err == nil {
		t.Fatal("expected error for NaN quota")
	}
	if _, err := QuotaToBytes(1, "PB"); err == nil {
		t.Fatal("expected error for unsupported PB unit")
	}
	if _, err := QuotaToBytes(9000000, "TB"); err == nil {
		t.Fatal("expected error for > MaxInt64 quota bytes (9000000 TB)")
	}
	if _, err := QuotaToBytes(float64(math.MaxInt64), "B"); err == nil {
		t.Fatal("expected error for float64(MaxInt64) B overflow")
	}
}

func TestValidateWatchItemAndCompile(t *testing.T) {
	f2 := 2.0
	item := WatchItem{
		Name: "  AI Coding  ",
		Matchers: WatchMatcherMap{
			WatchDimProcess: {Type: MatchExact, Values: []string{"Cursor", "Cursor", "codex"}},
			WatchDimDomain:  {Type: MatchExact, Values: []string{"API.Watched.Test."}},
			WatchDimIface:   {Type: MatchRegex, Values: []string{"^(wg|tailscale)"}},
			WatchDimGroup:   {Type: MatchKeyword, Values: []string{"ai"}},
		},
		OverrideFactor: &f2,
		QuotaValue:     10,
		QuotaUnit:      "GB",
		ResetType:      ResetMonthly,
		ResetDay:       15,
		Pinned:         true,
		Enabled:        true,
	}
	compiled, err := CompileWatchItem(item)
	if err != nil {
		t.Fatalf("CompileWatchItem failed: %v", err)
	}
	if compiled.Item.Name != "AI Coding" {
		t.Fatalf("trimmed name = %q", compiled.Item.Name)
	}
	if compiled.Item.QuotaBytes != 10*(1<<30) {
		t.Fatalf("quota bytes = %d, want %d", compiled.Item.QuotaBytes, 10*(1<<30))
	}
	if len(compiled.Item.Matchers[WatchDimProcess].Values) != 2 {
		t.Fatalf("expected deduplicated process values, got %v", compiled.Item.Matchers[WatchDimProcess].Values)
	}
	if !compiled.Matchers[WatchDimProcess].Match("Cursor") || !compiled.Matchers[WatchDimProcess].Match("codex") || compiled.Matchers[WatchDimProcess].Match("curl") {
		t.Fatal("unexpected process match behavior")
	}
	if !compiled.Matchers[WatchDimDomain].Match("sub.api.watched.test") || compiled.Matchers[WatchDimDomain].Match("other.test") {
		t.Fatal("unexpected domain match behavior")
	}
	if !compiled.Matchers[WatchDimIface].Match("wg0") || compiled.Matchers[WatchDimIface].Match("eth0") {
		t.Fatal("unexpected iface regex match behavior")
	}
	if !compiled.Matchers[WatchDimGroup].Match("AI-Pool") || compiled.Matchers[WatchDimGroup].Match("Direct") {
		t.Fatal("unexpected group keyword match behavior")
	}
}

func TestValidateWatchItemRejections(t *testing.T) {
	zero := 0.0
	neg := -1.5
	base := WatchMatcherMap{WatchDimProcess: {Type: MatchExact, Values: []string{"curl"}}}

	cases := []struct {
		name string
		item WatchItem
	}{
		{"empty name", WatchItem{Name: "   ", Matchers: base}},
		{"empty matchers", WatchItem{Name: "Empty", Matchers: WatchMatcherMap{}}},
		{"bad dim", WatchItem{Name: "BadDim", Matchers: WatchMatcherMap{"user": {Type: MatchExact, Values: []string{"a"}}}}},
		{"auto_extract rejected", WatchItem{Name: "Auto", Matchers: WatchMatcherMap{WatchDimOutbound: {Type: MatchAutoExtract, Values: []string{"a"}}}}},
		{"empty values", WatchItem{Name: "NoVals", Matchers: WatchMatcherMap{WatchDimProcess: {Type: MatchExact, Values: []string{}}}}},
		{"blank value", WatchItem{Name: "BlankVal", Matchers: WatchMatcherMap{WatchDimProcess: {Type: MatchExact, Values: []string{"  "}}}}},
		{"all whitespace values", WatchItem{Name: "AllBlank", Matchers: WatchMatcherMap{WatchDimDomain: {Type: MatchExact, Values: []string{"  ", "\t"}}}}},
		{"all whitespace regex values", WatchItem{Name: "AllBlankRegex", Matchers: WatchMatcherMap{WatchDimDomain: {Type: MatchRegex, Values: []string{"  ", "\t"}}}}},
		{"invalid regex", WatchItem{Name: "BadRe", Matchers: WatchMatcherMap{WatchDimDomain: {Type: MatchRegex, Values: []string{"("}}}}},
		{"override zero", WatchItem{Name: "Ov0", Matchers: base, OverrideFactor: &zero}},
		{"override neg", WatchItem{Name: "OvNeg", Matchers: base, OverrideFactor: &neg}},
		{"negative quota", WatchItem{Name: "NegQ", Matchers: base, QuotaValue: -1, QuotaUnit: "GB"}},
		{"bad quota unit", WatchItem{Name: "BadUnit", Matchers: base, QuotaValue: 1, QuotaUnit: "PB"}},
		{"quota value > MaxInt64", WatchItem{Name: "HugeQuotaVal", Matchers: base, QuotaValue: 9000000, QuotaUnit: "TB"}},
		{"quota bytes > MaxInt64", WatchItem{Name: "HugeQuotaBytes", Matchers: base, QuotaBytes: uint64(math.MaxInt64) + 1, QuotaUnit: "GB"}},
		{"domain double dot", WatchItem{Name: "DoubleDot", Matchers: WatchMatcherMap{WatchDimDomain: {Type: MatchExact, Values: []string{".."}}}}},
		{"monthly day 0", WatchItem{Name: "Day0", Matchers: base, ResetType: ResetMonthly, ResetDay: 0}},
		{"monthly day 29", WatchItem{Name: "Day29", Matchers: base, ResetType: ResetMonthly, ResetDay: 29}},
		{"bad reset type", WatchItem{Name: "BadReset", Matchers: base, ResetType: "weekly"}},
		{"bad expires_at", WatchItem{Name: "BadExp", Matchers: base, ResetType: ResetFixedDate, ExpiresAt: "not-a-date"}},
	}

	for _, tc := range cases {
		if err := ValidateWatchItem(&tc.item); err == nil {
			t.Errorf("%s: expected validation error, got nil", tc.name)
		}
	}
}

func TestCompileDimensionMatcherSkipsEmptyNormalizedDomain(t *testing.T) {
	cm, err := CompileDimensionMatcher(WatchDimDomain, DimensionMatcher{
		Type:   MatchExact,
		Values: []string{"..", ".", " ... "},
	})
	if err != nil {
		t.Fatalf("CompileDimensionMatcher failed: %v", err)
	}
	if len(cm.lowerExact) != 0 || len(cm.exactSet) != 0 {
		t.Fatalf("expected empty normalized domains to be skipped, got lowerExact=%v exactSet=%v", cm.lowerExact, cm.exactSet)
	}
	for _, candidate := range []string{"", ".", "..", "example.com", "example.com."} {
		if cm.Match(candidate) {
			t.Fatalf("expected Match(%q) to be false for dot-only exact domain matcher", candidate)
		}
	}
}

func TestValidateWatchItemNormalizesMixedAndDuplicateValues(t *testing.T) {
	item := WatchItem{
		Name: "MixedDomain",
		Matchers: WatchMatcherMap{
			WatchDimDomain: {
				Type:   MatchExact,
				Values: []string{"dup.example.test", "  ", "dup.example.test."},
			},
			WatchDimProcess: {
				Type:   MatchRegex,
				Values: []string{"  ", "^curl$", "foo "},
			},
		},
	}
	if err := ValidateWatchItem(&item); err != nil {
		t.Fatalf("ValidateWatchItem failed for mixed valid+whitespace values: %v", err)
	}
	gotDom := item.Matchers[WatchDimDomain].Values
	if len(gotDom) != 1 || gotDom[0] != "dup.example.test" {
		t.Fatalf("domain values = %v, want [dup.example.test]", gotDom)
	}
	gotProc := item.Matchers[WatchDimProcess].Values
	if len(gotProc) != 2 || gotProc[0] != "^curl$" || gotProc[1] != "foo " {
		t.Fatalf("process regex values = %v, want [^curl$ foo ]", gotProc)
	}

	// Non-whitespace dot-only exact domain mixed with valid domain still fails.
	badMixed := WatchItem{
		Name: "BadDotMixed",
		Matchers: WatchMatcherMap{
			WatchDimDomain: {
				Type:   MatchExact,
				Values: []string{"dup.example.test", ".."},
			},
		},
	}
	if err := ValidateWatchItem(&badMixed); err == nil {
		t.Fatal("expected error for mixed valid domain and dot-only domain \"..\"")
	}
}

func TestCompileDimensionMatcherSkipsBlankRegex(t *testing.T) {
	cm, err := CompileDimensionMatcher(WatchDimDomain, DimensionMatcher{
		Type:   MatchRegex,
		Values: []string{"", "   ", `^api\.test$`},
	})
	if err != nil {
		t.Fatalf("CompileDimensionMatcher failed: %v", err)
	}
	if cm.Match("other.test") {
		t.Fatal("blank regex entry compiled into match-all")
	}
	if !cm.Match("api.test") {
		t.Fatal("expected valid regex to still match api.test")
	}

	cmAllBlank, err := CompileDimensionMatcher(WatchDimDomain, DimensionMatcher{
		Type:   MatchRegex,
		Values: []string{"", "\t"},
	})
	if err != nil {
		t.Fatalf("CompileDimensionMatcher all-blank failed: %v", err)
	}
	if cmAllBlank.Match("anything.test") {
		t.Fatal("all-blank regex matcher must not match anything")
	}
}
