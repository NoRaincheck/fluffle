package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/displaywidth"

	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// Every budget in this app is measured for the **widest** reading of East Asian
// Ambiguous width, because the app cannot ask the terminal which reading it
// uses.
//
// A line the app measures at the terminal's width and the terminal measures at
// width+1 wraps, and a wrapped line makes the emulator consume a row the
// renderer does not know about. Every line after it is then written one row
// off, which is what a "redraw issue" looks like from the outside: the chrome
// is re-rendered correctly and lands in the wrong place, and the header and
// footer appear to have been lost.
//
// The text is untrusted, so the characters that do it arrive in message
// content rather than in the chrome. Curly quotes, em dashes, ellipses,
// arrows, middots, degrees, primes and multiplication signs are all Ambiguous
// and all ordinary in prose. Budgeting for the wide reading is the only choice
// that is safe on both kinds of terminal, and the cost is one cell of budget per
// ambiguous character on the line: a row of ASCII is unaffected, and a row with
// three curly quotes shows three fewer characters.
var (
	narrowCells = displaywidth.Options{ControlSequences: true}
	wideCells   = displaywidth.Options{EastAsianWidth: true, ControlSequences: true}
)

// excessCells is how many more cells s costs on a wide-ambiguous terminal than
// on a narrow one. It is the whole of the disagreement, measured over the
// string rather than per rune, so a grapheme cluster — a ZWJ family, a
// combining sequence — is charged once and not once per component.
func excessCells(s string) int {
	return wideCells.String(s) - narrowCells.String(s)
}

// truncateCells cuts s to at most w cells on the widest terminal that will draw
// it. It is termtext.Truncate with the budget reduced by the excess, which
// keeps ansi.Truncate's ANSI and grapheme handling rather than reimplementing
// the cut.
func truncateCells(s string, w int, tail string) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, max(w-excessCells(s), 0), tail)
}

// wrapCells is termtext.Wrap with the budget reduced by the block's whole
// excess. That is an upper bound on any single line's excess, so every line it
// returns fits on the widest terminal; it is conservative in the sense that a
// block with ambiguous characters late in it wraps a little earlier than it
// strictly needs to.
func wrapCells(s string, w int) []string {
	if w <= 0 {
		return strings.Split(s, "\n")
	}
	return termtext.Wrap(s, max(w-excessCells(s), 1))
}

// cellSlice is the cells of s in [from, to), measured on the wide reading. It
// is the one place the app cuts a rendered line at a cell offset, so it cuts on
// grapheme boundaries and takes only the graphemes that fit *entirely* inside
// the range: a grapheme straddling an edge is dropped rather than half-drawn,
// which is also what keeps the result no wider than the range asked for. A line
// that ends in a wide grapheme loses that grapheme instead of gaining a cell.
func cellSlice(s string, from, to int) string {
	if from >= to {
		return ""
	}
	var b strings.Builder
	at := 0
	graphemes := wideCells.StringGraphemes(s)
	for graphemes.Next() {
		w := graphemes.Width()
		if at+w <= from {
			at += w
			continue
		}
		if at >= to || at+w > to {
			break
		}
		b.WriteString(graphemes.Value())
		at += w
	}
	return b.String()
}

// overlayCentered draws panel over background, centered, in a width-by-height
// viewport, clipping the panel rather than repositioning it.
//
// It is the app's own composition rather than screen.OverlayCentered because
// the width every decision here is made in has to be the wide reading.
// screen.Splice pads a replacement to the width its caller asked for, and its
// caller asks using the narrow reading — so a panel that is *narrower* than the
// caller believes, which is every panel holding an ambiguous character, grows
// the line by exactly the difference and the frame loses a row. Keeping the
// composition in the app keeps one width policy for measuring and for cutting.
func overlayCentered(background, panel string, width, height int) string {
	if panel == "" || width <= 0 || height <= 0 {
		return background
	}
	backLines := strings.Split(background, "\n")
	for len(backLines) < height {
		backLines = append(backLines, "")
	}
	panelLines := strings.Split(panel, "\n")
	panelW := 0
	for _, line := range panelLines {
		panelW = max(panelW, wideCells.String(line))
	}
	column := max(0, (width-panelW)/2)
	row := max(0, (height-len(panelLines))/2)
	for r, panelLine := range panelLines {
		br := row + r
		if br < 0 || br >= height {
			continue
		}
		backLines[br] = cellSlice(backLines[br], 0, column) +
			cellSlice(panelLine, 0, min(panelW, width-column)) +
			cellSlice(backLines[br], column+panelW, width)
	}
	return strings.Join(backLines, "\n")
}
