package screen

import "testing"

func TestSmokeWidthIsWidestLine(t *testing.T) {
	if got := Width("ab\nabcd\nabc"); got != 4 {
		t.Fatalf("Width = %d, want 4", got)
	}
}

func TestSmokeOverlayCenteredReplacesCells(t *testing.T) {
	if got := OverlayCentered("aaaa\nbbbb\ncccc", "ZZ", 4, 3); got != "aaaa\nbZZb\ncccc" {
		t.Fatalf("OverlayCentered = %q", got)
	}
}
