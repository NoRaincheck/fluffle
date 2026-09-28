package tui

import (
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// The footer is a set of key/description items reflowed into as many aligned
// columns as the terminal has room for, which is what the narrowest supported
// terminal bounds. These tests pin that shape, because the alternative — one
// run-on line — cannot say what a key does and cannot adapt to width.

func plainLines(s string) []string {
	return strings.Split(termtext.StripANSI(s), "\n")
}

// A terminal too narrow for two columns falls back to one item per row rather
// than truncating an item: a half-printed shortcut teaches the wrong thing.
func TestHelpReflowsToOneColumnWhenTwoDoNotFit(t *testing.T) {
	items := []helpItem{
		{key: "↑↓", description: "move the cursor"},
		{key: "g", description: "group the rows"},
	}
	rows := reflowHelp(items, helpItemWidth(items[0]), helpColumnGap)
	if len(rows) != 2 {
		t.Fatalf("an item per row was expected, got %d rows: %v", len(rows), rows)
	}
}

// The greatest column count that fits is the one to use: a wide terminal should
// not leave a footer four rows tall when two rows of it would fit.
func TestHelpReflowsToTheGreatestColumnCountThatFits(t *testing.T) {
	items := helpItems()
	wide := len(strings.Split(renderHelp(items, 200), "\n"))
	narrow := len(strings.Split(renderHelp(items, MinWidth), "\n"))
	if wide >= narrow {
		t.Errorf("a 200-column footer is %d rows and a %d-column one is %d: a wider terminal did not use the extra room",
			wide, MinWidth, narrow)
	}
}

// Every item occupies one cell of the grid, so a reflow cannot drop one or
// duplicate one. Order is the keymap's, so the footer reads in the same order
// as the key handling.
func TestHelpReflowKeepsEveryItemOnceInOrder(t *testing.T) {
	items := helpItems()
	for _, width := range []int{MinWidth, 80, 100, 120, 160, 200, 400} {
		var seen []string
		for _, row := range reflowHelp(items, width, helpColumnGap) {
			for _, it := range row {
				seen = append(seen, it.key)
			}
		}
		if len(seen) != len(items) {
			t.Fatalf("at %d columns the footer names %d of %d items: %v", width, len(seen), len(items), seen)
		}
		for i, it := range items {
			if seen[i] != it.key {
				t.Fatalf("at %d columns item %d is %q, want %q: %v", width, i, seen[i], it.key, seen)
			}
		}
	}
}

// Every bound key has to be named, or it is a key the user has to guess at. The
// footer is now several rows, so completeness is no longer bounded by the
// narrowest terminal and this list is the whole keymap.
//
// The arrow keys are looked for as the word "arrows" rather than as U+2191 and
// U+2193: they are bound and named, but naming them with the glyphs would put
// two East Asian Ambiguous characters in every frame. The invariant is that the
// key is named, not that a particular rune spells it.
func TestHelpNamesEveryBoundKey(t *testing.T) {
	footer := termtext.StripANSI(renderHelp(helpItems(), 200))
	for _, want := range []string{"arrows", "Enter", "Esc", "g", "v", "r", "q", "ctrl+c"} {
		if !strings.Contains(footer, want) {
			t.Errorf("the footer does not name %q:\n%s", want, footer)
		}
	}
}

// No footer line may be wider than the terminal it is drawn in, at any width
// the TUI admits, or a line wraps and every band below it slides off screen.
func TestHelpNeverExceedsTheTerminal(t *testing.T) {
	for _, width := range []int{MinWidth, 72, 80, 100, 110, 120, 160, 200, 400} {
		for i, line := range plainLines(renderHelp(helpItems(), width)) {
			if n := termtext.DisplayWidth(line); n > width {
				t.Errorf("at %d columns footer row %d is %d cells: %q", width, i, n, line)
			}
		}
	}
}

// The narrowest terminal the TUI admits has to hold the whole keymap.
func TestHelpFitsTheNarrowestTerminal(t *testing.T) {
	if n := len(plainLines(renderHelp(helpItems(), MinWidth))); n < 1 {
		t.Errorf("the footer is %d rows at the %d-column floor", n, MinWidth)
	}
}

// Every returned row shares one aligned grid, so a column is as wide as the
// widest item that lands in it — which is what lets a short item be padded
// without the renderer remeasuring every line.
func TestHelpColumnWidthsAreTheWidestItemInEachColumn(t *testing.T) {
	if got, want := helpItemWidth(helpItem{key: "a", description: "one"}), 5; got != want {
		t.Errorf("helpItemWidth = %d, want %d", got, want)
	}
	items := []helpItem{
		{key: "a", description: "one"},
		{key: "longkey", description: "two"},
		{key: "b", description: "three"},
		{key: "longerkey", description: "four"},
	}
	// Wide enough for two columns and not for three, so each column holds two
	// items of different widths.
	rows := reflowHelp(items, 25, helpColumnGap)
	if len(rows) != 2 || len(rows[0]) != 2 {
		t.Fatalf("wanted 2 rows of 2 items, got %v", rows)
	}
	got := helpColumnWidths(rows)
	want := []int{
		max(helpItemWidth(items[0]), helpItemWidth(items[2])),
		max(helpItemWidth(items[1]), helpItemWidth(items[3])),
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("column widths = %v, want %v", got, want)
	}
}

// The gap the reflow budgets and the gap the renderer draws are the same
// number, or the footer comes out narrower than the layout chose columns for
// and a wider terminal is left with room it never spent.
func TestHelpTheDrawnGapIsTheBudgetedGap(t *testing.T) {
	items := []helpItem{
		{key: "a", description: "one"},
		{key: "bb", description: "two"},
		{key: "c", description: "three"},
		{key: "dd", description: "four"},
	}
	// Wide enough for two columns of this grid and not for three.
	rows := reflowHelp(items, 20, helpColumnGap)
	if len(rows) == 0 || len(rows[0]) != 2 {
		t.Fatalf("wanted two columns, got %v", rows)
	}
	widths := helpColumnWidths(rows)
	line := plainLines(renderHelp(items, 20))[0]
	if at := cellIndexOf(line, helpDivider); at != widths[0] {
		t.Errorf("the divider is at cell %d, want %d: the column is not padded to its grid width", at, widths[0])
	}
	if at := cellIndexOf(line, "bb"); at != widths[0]+helpColumnGap {
		t.Errorf("the second column starts at cell %d, want %d", at, widths[0]+helpColumnGap)
	}
}

// cellIndexOf is where sub begins in line, counted in cells. The footer is
// padded to the terminal and styled, so a byte or rune offset is not the
// position a terminal puts it at.
func cellIndexOf(line, sub string) int {
	for i := range line {
		if strings.HasPrefix(line[i:], sub) {
			return termtext.DisplayWidth(line[:i])
		}
	}
	return -1
}
