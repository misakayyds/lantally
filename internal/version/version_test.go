package version

import "testing"

func TestStringIncludesVersion(t *testing.T) {
	if String() == "" || Version != "0.1.0-dev" {
		t.Fatalf("Version=%q String=%q", Version, String())
	}
	orig := Commit
	Commit = "abc1234"
	t.Cleanup(func() { Commit = orig })
	if String() != "0.1.0-dev (abc1234)" {
		t.Fatalf("String() = %q", String())
	}
}
