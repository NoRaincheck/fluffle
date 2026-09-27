package termtext

import "testing"

func TestSmokeSanitizeLineDropsNonSGRSequences(t *testing.T) {
	if got := SanitizeLine("a\x1b]0;title\x07b"); got != "ab" {
		t.Fatalf("SanitizeLine = %q, want %q", got, "ab")
	}
}

func TestSmokeTruncateKeepsTailWithinWidth(t *testing.T) {
	if got := Truncate("abcdefgh", 5, "…"); got != "abcd…" {
		t.Fatalf("Truncate = %q, want %q", got, "abcd…")
	}
}

func TestSmokeWrapNeverExceedsWidth(t *testing.T) {
	lines := Wrap("the quick brown fox jumps", 10)
	if len(lines) == 0 {
		t.Fatal("Wrap returned no lines")
	}
	for _, line := range lines {
		if DisplayWidth(line) > 10 {
			t.Fatalf("line %q is wider than 10", line)
		}
	}
}
