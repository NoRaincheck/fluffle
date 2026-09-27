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
	chromeH     = 3

	// MinSplitWidth is the width at which the list and the thread can sit
	// side by side. Below it the list takes the whole width and Enter shows
	// the thread in its place.
	MinSplitWidth = 110
	ListW         = 74
)

// contentWidth is a row's content column width, or zero when the pane is too
// narrow to draw one.
func contentWidth(w int) int {
	if cw := w - rowPrefixW; cw > 0 {
		return cw
	}
	return 0
}

// cell is one fixed-width column: sanitized, then truncated so a hand-edited
// database cannot shift the row.
func cell(s string) string {
	return fmt.Sprintf("%-*s", ColW, termtext.Truncate(termtext.SanitizeLine(s), ColW, ""))
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

// renderRows draws the visible window and pads to exactly h rows so the
// title, hint, and status bands keep their places.
func renderRows(w, h int, rows []store.Row, cursor, scroll int) string {
	if h <= 0 || w <= 0 {
		return ""
	}
	lines := make([]string, 0, h)
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
	return strings.Join(lines, "\n")
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
