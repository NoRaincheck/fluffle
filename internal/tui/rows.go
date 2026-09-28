package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// The row prefix is fixed: a cursor, four 12-cell columns, four gaps of two
// cells, and a three-cell count. Every text column is MaxSlug or MaxName wide,
// so one constant replaces a per-column width and an adaptive drop ladder.
const (
	ColW        = 12
	CountW      = 3
	ColGap      = 2
	CursorW     = 2
	rowPrefixW  = CursorW + ColW + ColGap + ColW + ColGap + ColW + ColGap + ColW + ColGap + CountW
	MinContentW = 10
	MinWidth    = rowPrefixW + MinContentW
	MinHeight   = 24

	// titleH and statusH are fixed bands; the footer between them is reflowed
	// to the terminal width and so is however many rows it needs. See
	// model.chromeH, which is the only place the three are added up.
	titleH  = 1
	statusH = 1

	// listHeaderH is the column header and the rule under it, pinned above the
	// list's scrolling window.
	listHeaderH = 2

	// MinSplitWidth is the width at which the list and the thread can sit
	// side by side. Below it the list takes the whole width and Enter shows
	// the thread in its place.
	MinSplitWidth = 110
	ListW         = 74

	// PaneDividerW is the one column between the list and the thread in a
	// split. Without it the list's truncated content cell runs straight into
	// the thread's first line and the only cue is that one of them stopped
	// mid-word.
	PaneDividerW    = 1
	paneDividerRune = '│'
)

// rowHeader labels the four fixed columns and the count, in the same cells
// rowLine draws them in. It is the only legend a scrolled list has, so it is
// drawn above the scrolling window rather than inside it.
//
// The content column is not labelled. The count sits in its last three cells
// with no gap before the content, so a label there would read as one word
// ("CNTCONTENT") and the text column is the one thing on a row that needs no
// explaining.
func rowHeader(w int) string {
	return pad(dimStyle.Render(strings.Repeat(" ", CursorW)+
		cell("TIME")+strings.Repeat(" ", ColGap)+
		cell("CHANNEL")+strings.Repeat(" ", ColGap)+
		cell("THREAD")+strings.Repeat(" ", ColGap)+
		cell("NAME")+strings.Repeat(" ", ColGap)+
		fmt.Sprintf("%*s", CountW, "CNT")), w)
}

// contentWidth is a row's content column width, or zero when the pane is too
// narrow to draw one.
func contentWidth(w int) int {
	if cw := w - rowPrefixW; cw > 0 {
		return cw
	}
	return 0
}

// cell is one fixed-width column: sanitized, then truncated and padded to
// ColW cells. The padding is measured in cells rather than runes because fmt's
// %*s counts runes, and a wide grapheme is worth two cells — so a name the
// rules would never accept, typed straight into the database, overran the
// column and shifted every column after it.
func cell(s string) string {
	return pad(termtext.Truncate(termtext.SanitizeLine(s), ColW, ""), ColW)
}

// firstLine is the representative message's first line. A row is a preview,
// and a preview is one line.
func firstLine(content string) string {
	line, _, _ := strings.Cut(content, "\n")
	return line
}

// rowLine is exactly w cells wide: the fixed prefix, then as much of the
// content as fits, then padding.
func rowLine(w int, row store.Row, selected bool) string {
	marker := "  "
	if selected {
		marker = rowSelectedStyle.Render("▸ ")
	}
	count := ""
	if row.Count > 1 {
		count = strconv.Itoa(row.Count)
		if len(count) > CountW-1 {
			count = "99+"
		}
	}
	content := termtext.Truncate(termtext.SanitizeLine(firstLine(row.Content)), contentWidth(w), "…")
	return pad(marker+
		cell(formatTime(row.Time))+strings.Repeat(" ", ColGap)+
		cell(row.Channel)+strings.Repeat(" ", ColGap)+
		cell(row.Thread)+strings.Repeat(" ", ColGap)+
		cell(row.Name)+strings.Repeat(" ", ColGap)+
		fmt.Sprintf("%*s", CountW, count)+content, w)
}

// renderRows draws the column header, a rule, and the visible window, then pads
// to exactly h rows so the title, footer, and status bands keep their places.
// The header and the rule are outside the window on purpose: the window scrolls
// under them, which is what keeps a scrolled list readable.
func renderRows(w, h int, rows []store.Row, cursor, scroll int) string {
	if h <= 0 || w <= 0 {
		return ""
	}
	lines := []string{rowHeader(w), pad(sepStyle.Render(strings.Repeat("─", w)), w)}
	if len(rows) == 0 {
		// clamp has already zeroed scroll, so the window below cannot read
		// before the start of an empty list.
		lines = append(lines, pad(placeholder("no rows — press g to change group"), w))
	}
	for i := scroll; i < len(rows) && len(lines) < h; i++ {
		lines = append(lines, rowLine(w, rows[i], i == cursor))
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	// A body shorter than the header keeps the header and drops the rest, so a
	// caller always gets h rows and a rule is better than a stray data row.
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// pad right-fills a rendered line to w cells, measuring cells rather than
// runes so styling and wide graphemes do not shift the row.
func pad(s string, w int) string {
	if n := w - termtext.DisplayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func placeholder(s string) string { return placeholderStyle.Render(s) }

// formatTime is exactly ColW cells for every parseable input.
func formatTime(t string) string {
	if t == "" {
		return strings.Repeat(" ", ColW)
	}
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return cell(t)
	}
	return parsed.Format("Jan 02 15:04")
}

// formatClock is the short form used in the thread view.
func formatClock(t string) string {
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return "     "
	}
	return parsed.Format("15:04")
}
