package tui

import (
	"strings"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// A message is a clock, an author, and a body on its own lines. The body
// starts one cell past the clock, so the author is free to run further right
// on the header line without ever colliding with the body below it.
const (
	threadIndent  = 2
	threadClockW  = 5
	threadHeadGap = 1
	threadBodyAt  = threadIndent + threadClockW + threadHeadGap
)

// renderThread draws a thread at any width: in the pane beside the list, or
// filling the terminal. The original post is not special-cased, so there is
// one uniform block per message and nothing to keep in sync.
func renderThread(w, h int, title string, thread []store.Message) string {
	if w < 1 || h < 1 {
		return ""
	}
	if len(thread) == 0 {
		// pad only right-fills, so a placeholder wider than the pane has to be
		// cut first or it overruns the row it shares with the list.
		return padLines(termtext.Truncate(placeholder("no thread — press g"), w, "…"), w, h)
	}
	header := pad(sepStyle.Render(termtext.Truncate(termtext.SanitizeLine(title), w, "…")), w)
	rows := []string{header, pad(sepStyle.Render(strings.Repeat("─", w)), w)}
	if keep := h - 2; keep > 0 {
		body := threadLines(w, thread)
		if len(body) > keep {
			body = body[len(body)-keep:]
		}
		rows = append(rows, body...)
	}
	return padLines(strings.Join(rows, "\n"), w, h)
}

// threadLines is every message as a header line plus its wrapped content.
// Every line is truncated and padded to w, so a long author name or a wide
// grapheme cannot push a line past the pane.
//
// The body column is exactly what is left after the indent, with no floor: a
// floor wider than that space wraps at one width and truncates at a narrower
// one, which silently drops words. Below the indent the body is blank, which
// is the one width at which content cannot be shown at all.
func threadLines(w int, thread []store.Message) []string {
	body := w - threadBodyAt
	lines := make([]string, 0, len(thread)*3)
	for _, m := range thread {
		header := strings.Repeat(" ", threadIndent) +
			pad(formatClock(m.CreatedAt), threadClockW) +
			strings.Repeat(" ", threadHeadGap) +
			nameStyle(m.AuthorType).Render(termtext.Truncate(termtext.SanitizeLine(m.Name), ColW, ""))
		lines = append(lines, pad(threadHeaderStyle.Render(termtext.Truncate(header, w, "")), w))
		for _, line := range termtext.Wrap(termtext.SanitizeBlock(m.Content), body) {
			// Clamping the indent keeps a line inside a pane narrower than the
			// indent itself, and is the same string at every wider pane.
			lines = append(lines, pad(
				threadBodyStyle.Render(strings.Repeat(" ", min(threadBodyAt, w))+termtext.Truncate(line, body, "")), w))
		}
	}
	return lines
}

// padLines pads s to exactly h rows of w cells. A thread longer than the
// terminal is already truncated by the caller, so the last h rows win.
func padLines(s string, w, h int) string {
	rows := strings.Split(s, "\n")
	if len(rows) > h {
		rows = rows[len(rows)-h:]
	}
	for len(rows) < h {
		rows = append(rows, strings.Repeat(" ", w))
	}
	for i, r := range rows {
		rows[i] = pad(r, w)
	}
	return strings.Join(rows, "\n")
}
