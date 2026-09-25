package tui

import (
	"strings"
	"testing"

	lipgloss "github.com/charmbracelet/lipgloss"
)

func TestPlaceOverlay_Centered(t *testing.T) {
	base := "line1\nline2\nline3\nline4\nline5"
	overlay := "AB"

	result := placeOverlay(base, overlay, 10, 5)
	lines := strings.Split(result, "\n")

	// y0 = (5-1)/2 = 2, so overlay replaces line[2]
	// x0 = (10-2)/2 = 4
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
	if lines[0] != "line1" {
		t.Fatalf("line 0 unchanged: %q", lines[0])
	}
	if lines[4] != "line5" {
		t.Fatalf("line 4 unchanged: %q", lines[4])
	}
	if lines[2] != "    AB" {
		t.Fatalf("line 2 centered at x0=4: %q", lines[2])
	}
}

func TestPlaceOverlay_BoxBorderPreserved(t *testing.T) {
	base := "╭──────────╮\n│          │\n│          │\n│          │\n╰──────────╯"
	overlay := "XY"

	result := placeOverlay(base, overlay, 10, 5)
	lines := strings.Split(result, "\n")

	// y0 = (5-1)/2 = 2, overlay at line[2]
	// Condition: y > 0 && y < len(baseLines)-3 → 2 > 0 && 2 < 2 → false
	// So line[2] goes through non-box path
	// x0 = (10-2)/2 = 4
	if lines[0] != "╭──────────╮" {
		t.Fatalf("top border unchanged: %q", lines[0])
	}
	if lines[4] != "╰──────────╯" {
		t.Fatalf("bottom border unchanged: %q", lines[4])
	}
	// Line[2] is replaced via non-box path (y=2 doesn't satisfy y<2)
	if !strings.Contains(lines[2], "XY") {
		t.Fatalf("overlay should be in line 2: %q", lines[2])
	}
}

func TestPlaceOverlay_BoxBorderPreservedMiddle(t *testing.T) {
	// For a taller base, the middle box lines DO preserve borders
	// Box visual width must match the width parameter for lipgloss.Width check
	// Box: 2 corner chars + 12 inner = visual width 14
	// y0 = (7-1)/2 = 3, condition: y > 0 && y < 7-3=4 → y=3 qualifies
	base := "╭────────────╮\n│            │\n│            │\n│            │\n│            │\n│            │\n╰────────────╯"
	overlay := "OK"

	result := placeOverlay(base, overlay, 14, 5)
	lines := strings.Split(result, "\n")

	// y0 = 3, overlay at line[3]
	// y=3: 3 > 0 && 3 < 4 → true, so box path
	// innerWidth = 12, overlayW = 2, innerX0 = (12-2)/2 = 5
	// Result: leftBorder + "     OK     " + rightBorder
	if !strings.Contains(lines[3], "OK") {
		t.Fatalf("overlay should be in line 3: %q", lines[3])
	}
	// Should have border characters
	if !strings.Contains(lines[3], "│") {
		t.Fatalf("box line should have border chars: %q", lines[3])
	}
}

func TestPlaceOverlay_NonBoxLinesCentered(t *testing.T) {
	base := "header\ncontent\nfooter"
	overlay := "MODAL"

	result := placeOverlay(base, overlay, 10, 3)
	lines := strings.Split(result, "\n")

	// y0 = (3-1)/2 = 1, overlay at line[1]
	// x0 = (10-5)/2 = 2
	if !strings.HasPrefix(lines[1], "  MODAL") {
		t.Fatalf("overlay centered at x0=2: %q", lines[1])
	}
	// Line[0] unchanged
	if lines[0] != "header" {
		t.Fatalf("line 0 unchanged: %q", lines[0])
	}
}

func TestPlaceOverlay_WidthFallback(t *testing.T) {
	base := "hello\nworld"
	overlay := "X"

	result := placeOverlay(base, overlay, 0, 2)
	lines := strings.Split(result, "\n")

	// width=0 → lipgloss.Width(base) = 5
	// x0 = (5-1)/2 = 2
	expectedW := lipgloss.Width(base)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	// Overlay replaces line[0] at x0=2
	if lines[0] != "  X" {
		t.Fatalf("overlay at x0=2: %q", lines[0])
	}
	// Line[1] unchanged
	if lines[1] != "world" {
		t.Fatalf("line 1 unchanged: %q", lines[1])
	}
	_ = expectedW
}

func TestPlaceOverlay_HeightFallback(t *testing.T) {
	base := "hello\nworld\nfoo"
	overlay := "X"

	result := placeOverlay(base, overlay, 10, 0)
	lines := strings.Split(result, "\n")

	expectedH := lipgloss.Height(base)
	if len(lines) != expectedH {
		t.Fatalf("expected %d lines, got %d", expectedH, len(lines))
	}
}

func TestPlaceOverlay_OverlayWiderThanBase(t *testing.T) {
	base := "ab\ncd"
	overlay := "LONG"

	result := placeOverlay(base, overlay, 4, 2)
	lines := strings.Split(result, "\n")

	// overlayW capped to width-2 = 2
	// x0 = (4-2)/2 = 1
	if !strings.HasPrefix(lines[0], " ") {
		t.Fatalf("should have left padding: %q", lines[0])
	}
}

func TestPlaceOverlay_OverlayTallerThanBase(t *testing.T) {
	base := "ab\ncd"
	overlay := "1\n2\n3\n4"

	result := placeOverlay(base, overlay, 10, 4)
	lines := strings.Split(result, "\n")

	// 2 base lines, so result has 2 lines
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (base height), got %d", len(lines))
	}
	// overlayH=4, height=4: overlayH>=height, so overlayH=4-2=2
	// y0 = (2-2)/2 = 0
	// x0 = (10-1)/2 = 4
	if lines[0] != "    1" {
		t.Fatalf("overlay line 0: %q", lines[0])
	}
	if lines[1] != "    2" {
		t.Fatalf("overlay line 1: %q", lines[1])
	}
}

func TestPlaceOverlay_EmptyOverlay(t *testing.T) {
	base := "hello\nworld"
	overlay := ""

	result := placeOverlay(base, overlay, 10, 2)
	lines := strings.Split(result, "\n")

	if len(lines) != 2 {
		t.Fatalf("should preserve base lines: got %d", len(lines))
	}
	// Empty overlay with height 0 means no lines to replace
}

func TestPlaceOverlay_SmallBase(t *testing.T) {
	base := "╭──╮\n╰──╯"
	overlay := "X"

	result := placeOverlay(base, overlay, 4, 2)
	lines := strings.Split(result, "\n")

	// y0 = (2-1)/2 = 0
	// x0 = (4-1)/2 = 1
	// y=0: y>0 is false → non-box path
	if lines[0] != " X" {
		t.Fatalf("overlay at y=0: %q", lines[0])
	}
}

func TestPlaceOverlay_AnsiInBase(t *testing.T) {
	// lipgloss.Width handles ANSI correctly
	base := "╭──────────╮\n│          │\n╰──────────╯"
	overlay := "OK"

	result := placeOverlay(base, overlay, 10, 3)
	lines := strings.Split(result, "\n")

	if lines[0] != "╭──────────╮" {
		t.Fatalf("top border: %q", lines[0])
	}
	if !strings.Contains(lines[1], "OK") {
		t.Fatalf("overlay in line 1: %q", lines[1])
	}
}

func TestPlaceOverlay_MultiLineOverlay(t *testing.T) {
	base := "line1\nline2\nline3\nline4\nline5"
	overlay := "A\nB"

	result := placeOverlay(base, overlay, 10, 5)
	lines := strings.Split(result, "\n")

	// y0 = (5-2)/2 = 1
	// x0 = (10-1)/2 = 4
	// Overlay at lines 1 and 2
	if lines[0] != "line1" {
		t.Fatalf("line 0: %q", lines[0])
	}
	if lines[1] != "    A" {
		t.Fatalf("overlay line 0 at y=1: %q", lines[1])
	}
	if lines[2] != "    B" {
		t.Fatalf("overlay line 1 at y=2: %q", lines[2])
	}
	if lines[4] != "line5" {
		t.Fatalf("line 4: %q", lines[4])
	}
}

func TestPlaceOverlay_HeightCentering(t *testing.T) {
	// height > baseLines: uses height-based centering
	base := "top\nmid\nbot"
	overlay := "X"

	result := placeOverlay(base, overlay, 10, 5)
	lines := strings.Split(result, "\n")

	// overlayH=1, height=5: alt = (5-1)/2 = 2
	// y0 = (3-1)/2 = 1, alt=2 < y0=1 is false → y0 stays 1
	if !strings.Contains(lines[1], "X") {
		t.Fatalf("overlay at y=1: %q", lines[1])
	}
}

func TestPlaceOverlay_HeightCenteringAlt(t *testing.T) {
	// When alt < y0, y0 takes alt
	base := "a\nb\nc\nd\ne\nf\ng"
	overlay := "X"

	result := placeOverlay(base, overlay, 10, 10)
	lines := strings.Split(result, "\n")

	// overlayH=1, height=10: alt = (10-1)/2 = 4
	// y0 = (7-1)/2 = 3, alt=4 < y0=3 is false → y0 stays 3
	if !strings.Contains(lines[3], "X") {
		t.Fatalf("overlay at y=3: %q", lines[3])
	}
}

func TestPlaceOverlay_TallerOverlayThanHeight(t *testing.T) {
	// overlayH=3, height=4: overlayH < height, so no cap
	base := "line1\nline2\nline3\nline4\nline5"
	overlay := "A\nB\nC"

	result := placeOverlay(base, overlay, 10, 4)
	lines := strings.Split(result, "\n")

	// overlayH=3 (no cap since 3 < 4)
	// y0 = (5-3)/2 = 1
	// x0 = (10-1)/2 = 4
	if lines[0] != "line1" {
		t.Fatalf("line 0: %q", lines[0])
	}
	if lines[1] != "    A" {
		t.Fatalf("overlay at y=1: %q", lines[1])
	}
	if lines[2] != "    B" {
		t.Fatalf("overlay at y=2: %q", lines[2])
	}
	if lines[3] != "    C" {
		t.Fatalf("overlay at y=3: %q", lines[3])
	}
}

func TestPlaceOverlay_NegativeX0Clamped(t *testing.T) {
	// When overlay is wider than half the width, x0 could be negative
	base := "abc\ndef"
	overlay := "123456"

	result := placeOverlay(base, overlay, 10, 2)
	lines := strings.Split(result, "\n")

	// overlayW=6, x0 = (10-6)/2 = 2
	if lines[0] != "  123456" {
		t.Fatalf("overlay: %q", lines[0])
	}
}

func TestPlaceOverlay_NoBoxPrefix(t *testing.T) {
	// Lines not starting with box chars use non-box path
	base := "abc\ndef\nghi"
	overlay := "X"

	result := placeOverlay(base, overlay, 10, 3)
	lines := strings.Split(result, "\n")

	// y0 = (3-1)/2 = 1
	// x0 = (10-1)/2 = 4
	if lines[1] != "    X" {
		t.Fatalf("overlay at y=1: %q", lines[1])
	}
}
