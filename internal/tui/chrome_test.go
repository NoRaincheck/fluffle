package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

func sizedModel(w, h, n int) model {
	m := model{width: w, height: h, granularity: store.GranularityMessage}
	for i := range n {
		m.rows = append(m.rows, store.Row{
			ID: int64(i + 1), ThreadID: int64(i + 1), Channel: "general",
			Thread: "welcome", Name: "alice", Content: "hello",
			Time: time.Now().Add(-time.Duration(i) * time.Hour).Format(time.RFC3339),
		})
	}
	return m
}

// The footer is several rows wide, so the chrome around the body is no longer a
// constant. These tests pin the accounting: the body gets whatever the bands
// left, and the bands never overlap or run past the terminal.
func TestChromeIsTheTitleFooterAndStatus(t *testing.T) {
	m := sizedModel(120, 40, 30)
	footer := len(plainLines(renderHelp(helpItems(), 120)))
	if footer < 1 {
		t.Fatalf("the footer is %d rows at 120 columns", footer)
	}
	if want := 2 + footer; m.chromeH() != want {
		t.Errorf("chromeH = %d, want a title, %d footer rows, and a status: %d", m.chromeH(), footer, want)
	}
}

// The whole point of a reflowing footer: a narrow terminal pays in rows and a
// wide one in columns, and the body follows the difference either way.
func TestChromeGrowsAndShrinksWithTheFooter(t *testing.T) {
	narrow, wide := sizedModel(MinWidth, 40, 30), sizedModel(200, 40, 30)
	if narrow.chromeH() <= wide.chromeH() {
		t.Errorf("a %d-column terminal has %d rows of chrome and a 200-column one has %d: the narrow case did not trade columns for rows",
			MinWidth, narrow.chromeH(), wide.chromeH())
	}
}

// Whatever the reflow chose, the view fills the terminal exactly: one row per
// row, none of them wrapping. A view that is a row too tall loses its title to
// the renderer's top-truncation, and a row too short leaves the last band off.
func TestTheViewIsExactlyTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{MinWidth, MinHeight}, {80, 24}, {100, 30}, {110, 30}, {120, 40}, {200, 50}} {
		for _, rows := range []int{0, 1, 5, 200} {
			for _, detail := range []bool{false, true} {
				m := sizedModel(size[0], size[1], rows)
				m.detail = detail
				m.thread = []store.Message{{ID: 1, Name: "alice", AuthorType: "human", Content: "hello"}}
				m.threadID = 1
				lines := strings.Split(m.View(), "\n")
				if len(lines) != size[1] {
					t.Fatalf("%dx%d, %d rows, detail=%v: the view is %d rows", size[0], size[1], rows, detail, len(lines))
				}
				for i, line := range lines {
					if n := termtext.DisplayWidth(line); n > size[0] {
						t.Errorf("%dx%d: view row %d is %d cells", size[0], size[1], i, n)
					}
				}
			}
		}
	}
}

// The status band is one row, whatever the note in it. A note long enough to
// wrap is not a two-row band: it makes the view a row taller than the terminal,
// and the renderer resolves that by dropping the top line, so the title band
// goes and the footer lands in the status band's row. The note is budgeted to
// the band's content width, which is narrower than its block width because the
// style pads a cell each side, so the lengths either side of that boundary are
// the ones that matter.
func TestTheStatusBandIsExactlyOneRow(t *testing.T) {
	for _, w := range []int{MinWidth, MinWidth + 1, 80, 100, 110, 120, 200} {
		for _, n := range []int{0, 10, 60, 80, 90, 96, 97, 98, 99, 100, 120, 400} {
			m := sizedModel(w, MinHeight, 30)
			m.status = strings.Repeat("e", n)
			if got := len(plainLines(m.statusBand())); got != 1 {
				t.Errorf("at %d columns a %d-cell note drew a %d-row status band", w, n, got)
			}
			if got := len(plainLines(m.View())); got != MinHeight {
				t.Errorf("at %d columns a %d-cell note made the view %d rows, want %d", w, n, got, MinHeight)
			}
		}
	}
}

// The note the band cannot show is cut rather than wrapped, so the band still
// ends in an ellipsis and the row under the footer is still the status.
func TestTheStatusBandCutsRatherThanWraps(t *testing.T) {
	m := sizedModel(MinWidth, MinHeight, 30)
	m.status = strings.Repeat("e", 300)
	got := plainLines(m.statusBand())
	if len(got) != 1 {
		t.Fatalf("a 300-cell note drew a %d-row status band", len(got))
	}
	if !strings.HasSuffix(strings.TrimRight(got[0], " "), truncTail) {
		t.Errorf("status band = %q, want it cut to the tail, so the reader knows it was cut", got[0])
	}
}

// The app and the emulator have to agree on how many cells every glyph is
// worth, or the render desyncs: a line the app believes is exactly the
// terminal's width is written as such, the terminal puts it one cell over, the
// row wraps, and the frame loses a row. The glyphs the two can disagree about
// are the East Asian Ambiguous ones — one cell narrow, two wide — and which
// one a terminal uses is a user setting rather than a terminfo capability, so
// nothing at startup can negotiate it.
//
// The app's answer is to draw none of them. This derives the ambiguous set
// from the Unicode table rather than listing the glyphs the chrome happens to
// use today, so a new one cannot slip in.
func TestNoDrawnGlyphIsEastAsianAmbiguous(t *testing.T) {
	defer func(eastAsian bool) { runewidth.DefaultCondition.EastAsianWidth = eastAsian }(runewidth.DefaultCondition.EastAsianWidth)

	widthOf := func(r rune, eastAsian bool) int {
		runewidth.DefaultCondition.EastAsianWidth = eastAsian
		return runewidth.RuneWidth(r)
	}

	ambiguous := map[rune]string{}
	for _, s := range everyView(t) {
		for _, r := range s {
			if r < 128 {
				continue
			}
			narrow, wide := widthOf(r, false), widthOf(r, true)
			if narrow == 1 && wide == 2 {
				ambiguous[r] = fmt.Sprintf("U+%04X is %d cell narrow and %d wide", r, narrow, wide)
			}
		}
	}
	for r, why := range ambiguous {
		t.Errorf("the view draws %q (%s); a terminal that renders ambiguous width as wide "+
			"measures every line holding it one cell per occurrence too wide, which wraps the row", r, why)
	}
}

// everyView is every shape the TUI can draw, so the glyph audit covers the
// stacked list, the split panes, the fullscreen thread, the reply box, both
// empty states, a status note, and the too-narrow notice.
func everyView(t *testing.T) []string {
	t.Helper()
	// A row and a thread title long enough to be truncated, so the audit also
	// covers the glyph a truncation leaves behind.
	rows := []store.Row{
		{ID: 1, ThreadID: 1, Channel: "general", Thread: "welcome", Name: "alice", Count: 3,
			Content: "hello", Time: time.Now().Format(time.RFC3339)},
		{ID: 2, ThreadID: 2, Channel: "a-very-long-channel", Thread: "a-very-long-thread",
			Name: "a-very-long-name", Count: 12345,
			Content: "a representative message whose first line is much wider than any content column",
			Time:    time.Now().Format(time.RFC3339)},
	}
	thread := []store.Message{
		{ID: 1, ThreadID: 1, Name: "alice", AuthorType: "human", Content: "hi",
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: 2, ThreadID: 2, Name: "a-very-long-author", AuthorType: "agent",
			Content:   "a body long enough that the narrowest pane wraps it over several lines",
			CreatedAt: time.Now().Format(time.RFC3339)},
	}
	views := []string{narrowNotice(60, 10)}
	for _, size := range [][2]int{{MinWidth, MinHeight}, {80, 24}, {110, 30}, {200, 50}} {
		for _, filled := range []bool{true, false} {
			for _, detail := range []bool{false, true} {
				for _, status := range []string{"", "sent", "error: DAEMON_DOWN: refused"} {
					m := model{width: size[0], height: size[1], granularity: store.GranularityMessage,
						rows: rows, thread: thread, threadID: 1, detail: detail, status: status}
					if !filled {
						m.rows, m.thread, m.threadID = nil, nil, 0
					}
					m.clamp()
					views = append(views, m.View())
					composing := m
					composing.compose.open("reply in general > welcome")
					composing.compose.resize(composing.width)
					views = append(views, composing.View())
				}
			}
		}
	}
	return views
}

// The footer is the last band above the status, and the status is the last row:
// the view's shape is a contract the bands have to keep together.
func TestTheFooterSitsAboveTheStatus(t *testing.T) {
	m := sizedModel(120, 40, 30)
	m.status = "sent"
	lines := strings.Split(m.bodyView(), "\n")
	footer := renderHelp(helpItems(), 120)
	if got, want := lines[len(lines)-1-footerLines(m)], termtext.StripANSI(footer); !strings.Contains(got, firstKeyLine(want)) {
		t.Errorf("the band above the status is %q, want a row of the footer", got)
	}
	if got := termtext.StripANSI(lines[len(lines)-1]); !strings.Contains(got, "sent") {
		t.Errorf("the last row is %q, want the status", got)
	}
}

func firstKeyLine(footer string) string {
	line, _, _ := strings.Cut(footer, "\n")
	return strings.TrimSpace(termtext.StripANSI(line))
}

func footerLines(m model) int {
	return len(plainLines(renderHelp(helpItems(), m.width)))
}
