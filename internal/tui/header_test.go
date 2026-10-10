package tui

import (
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// The list is four fixed 12-cell columns and a 3-cell count. Nothing labelled
// them, so a scrolled list was a row of numbers and the only way back to knowing
// what a column meant was to scroll to the top and count cells.
func TestTheListNamesItsColumns(t *testing.T) {
	rows := plainLines(renderRows(120, 20, sizedModel(120, 20, 5).rows, 0, 0))
	for _, want := range []string{"TIME", "CHANNEL", "THREAD", "NAME", "CNT"} {
		if !strings.Contains(rows[0], want) {
			t.Errorf("the list header does not name %q: %q", want, rows[0])
		}
	}
}

// The header is the point: it sits above the scrolling window rather than
// inside it, so it holds its row however far the list is scrolled.
func TestTheListHeaderStaysPutWhileScrolling(t *testing.T) {
	rows := sizedModel(120, 40, 200).rows
	atTop := plainLines(renderRows(120, 20, rows, 0, 0))
	scrolled := plainLines(renderRows(120, 20, rows, 180, 150))
	if atTop[0] != scrolled[0] {
		t.Errorf("the header moved with the scroll:\n  top      %q\n  scrolled %q", atTop[0], scrolled[0])
	}
	if scrolled[1] == "" {
		t.Error("the rule under the header went missing while scrolled")
	}
	// And the window really did move, or the test above proves nothing.
	if plainRowsEqual(atTop, scrolled) {
		t.Error("scrolling changed nothing, so this test cannot tell a pinned header from a fixed one")
	}
}

// The header costs rows the window has, and the cursor still has to be drawn.
func TestTheListStillDrawsTheCursorWithAHeader(t *testing.T) {
	m := sizedModel(120, 40, 200)
	m.cursor = 180
	m.clamp()
	rows := plainLines(renderRows(m.width, m.height-m.chromeH(), m.rows, m.cursor, m.scroll))
	found := false
	for i, line := range rows[2:] {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "▸") {
			found = true
			if m.cursor-m.scroll != i {
				t.Errorf("the cursor marker is on window row %d, want %d", i, m.cursor-m.scroll)
			}
		}
	}
	if !found {
		t.Error("no row carries the cursor marker")
	}
}

// An empty list still has to say what its columns are, and still has to say it
// is empty: the placeholder goes under the header, not instead of it.
func TestAnEmptyListKeepsItsHeader(t *testing.T) {
	rows := plainLines(renderRows(120, 20, nil, 0, 0))
	if !strings.Contains(rows[0], "CHANNEL") {
		t.Errorf("the empty list lost its header: %q", rows[0])
	}
	if !strings.Contains(strings.Join(rows, "\n"), "no rows") {
		t.Errorf("the empty list does not say it is empty: %v", rows)
	}
}

// A split put the list and the thread on one row with nothing between them, so
// a truncated content cell ran straight into the thread's title and the only
// cue was that one of them stopped mid-word.
func TestASplitSeparatesTheTwoPanes(t *testing.T) {
	m := sizedModel(200, 40, 5)
	body := plainLines(m.bodyView())[titleH : titleH+m.bodyH()]
	if len(body) == 0 {
		t.Fatal("the split body is empty")
	}
	for i, line := range body {
		if got := dividerCell(line); got != ListW {
			t.Errorf("body row %d has its divider at cell %d, want %d: %q", i, got, ListW, line)
		}
	}
}

// The divider is a column, so the thread pane is one cell narrower than it was
// and the split still fills the width the view is drawn at.
func TestTheDividerCostsTheThreadPaneOneCell(t *testing.T) {
	m := sizedModel(200, 40, 5)
	if got, want := m.paneWidth(), m.viewW()-ListW-PaneDividerW; got != want {
		t.Errorf("paneWidth = %d, want %d", got, want)
	}
}

// Nothing may be lost off the right edge by taking a cell for the divider.
func TestTheSplitStillFillsTheTerminalWithTheDivider(t *testing.T) {
	for _, width := range []int{MinSplitWidth, 111, 120, 160, 200} {
		m := sizedModel(width, 40, 5)
		rows := plainLines(m.bodyView())
		for i, line := range rows {
			if n := termtext.DisplayWidth(line); n > width {
				t.Errorf("at %d columns view row %d is %d cells: %q", width, i, n, line)
			}
		}
	}
}

// The detail view is the thread alone, so it has no list to divide from and no
// list header to draw. The check is over the body rows only: the footer draws
// its own column dividers with the same rune, and chrome is not a pane edge.
func TestTheDetailViewHasNoDivider(t *testing.T) {
	m := sizedModel(200, 40, 5)
	m.detail = true
	m.threadID = 1
	m.thread = []store.Message{{ID: 1, Name: "alice", AuthorType: "human", Content: "hello"}}
	body := plainLines(m.bodyView())
	for i, line := range body[titleH : titleH+m.bodyH()] {
		if strings.ContainsRune(line, paneDividerRune) {
			t.Errorf("detail body row %d carries a pane divider: %q", i, line)
		}
	}
}

// dividerCell is the cell the pane divider falls on, measured in cells rather
// than runes: a styled row carries escape runes that shift every rune index
// after them, and the divider is defined by the list's width.
func dividerCell(line string) int {
	cells := 0
	for _, r := range line {
		if r == paneDividerRune {
			return cells
		}
		cells += termtext.DisplayWidth(string(r))
	}
	return -1
}

func plainRowsEqual(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}
