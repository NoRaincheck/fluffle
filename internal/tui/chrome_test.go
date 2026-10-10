package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/clipperhouse/displaywidth"
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

// The view fills every row and column it is allowed, and stops one short of the
// terminal on both axes. The stopping is the point: a view that reaches an edge
// is correct only while the terminal measures that edge exactly as the app
// does, and a terminal one cell narrower than it reported — or one cell
// different in a font, or under an ambiguous-width setting — wraps the row and
// desynchronises the frame permanently. slack buys that immunity for one column
// of content and one blank row. See model.slack.
func TestTheViewFillsTheTerminalLessSlack(t *testing.T) {
	for _, size := range [][2]int{{MinWidth, MinHeight}, {80, 24}, {100, 30}, {110, 30}, {120, 40}, {200, 50}} {
		for _, rows := range []int{0, 1, 5, 200} {
			for _, detail := range []bool{false, true} {
				m := sizedModel(size[0], size[1], rows)
				m.detail = detail
				m.thread = []store.Message{{ID: 1, Name: "alice", AuthorType: "human", Content: "hello"}}
				m.threadID = 1
				lines := strings.Split(m.View(), "\n")
				if want := size[1] - slack; len(lines) != want {
					t.Fatalf("%dx%d, %d rows, detail=%v: the view is %d rows, want %d",
						size[0], size[1], rows, detail, len(lines), want)
				}
				// Two cells of margin, because a line the terminal measures one
				// cell over is a row the app cannot recover.
				for i, line := range lines {
					if n := wideCells.String(line); n > m.viewW() {
						t.Errorf("%dx%d: view row %d is %d cells, over the %d the view may draw",
							size[0], size[1], i, n, m.viewW())
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
			if got, want := len(plainLines(m.View())), MinHeight-slack; got != want {
				t.Errorf("at %d columns a %d-cell note made the view %d rows, want %d", w, n, got, want)
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

// The invariant, and it is the one that matters: **no line the TUI draws may
// be wider than the terminal under either reading of East Asian Ambiguous
// width.** A line that the app measures at the terminal's width and the
// terminal measures at width+1 wraps, and a wrapped line makes the emulator
// consume a row the renderer does not know about, so every line after it is
// written one row off. That is the redraw mess: the chrome is re-rendered
// correctly and lands in the wrong place.
//
// The text is untrusted, so the characters that trigger it arrive in message
// content, not in the chrome. Curly quotes, em dashes, ellipses, arrows,
// middots, degrees and primes are all Ambiguous and all ordinary in prose.
func TestNoLineIsWiderThanTheTerminalUnderEitherAmbiguousWidth(t *testing.T) {
	// Measured independently of the app's own width layer, so a mistake in
	// that layer cannot make this pass.
	narrow := displaywidth.Options{ControlSequences: true}
	wide := displaywidth.Options{EastAsianWidth: true, ControlSequences: true}

	// The characters that do it, in text a person would actually write.
	content := strings.Join([]string{
		"it's the \u201cbest\u201d release we've shipped, isn't it?",
		"one \u2014 two \u2014 three, and a loading\u2026 done\u2026",
		"up\u2192 right\u2192 down\u2192; a \u00b7 b \u00b7 c; 20\u00b0C, 3\u00d74, x\u2032y\u2033",
	}, "\n")

	for _, size := range [][2]int{{MinWidth, MinHeight}, {80, 24}, {110, 30}, {120, 40}, {200, 50}} {
		{
			m := model{width: size[0], height: size[1], granularity: store.GranularityMessage}
			m.rows = []store.Row{{
				ID: 1, ThreadID: 1, Channel: "general", Thread: "welcome", Name: "alice",
				Content: strings.ReplaceAll(content, "\n", " "), Time: time.Now().Format(time.RFC3339),
			}}
			m.thread = []store.Message{{
				ID: 1, ThreadID: 1, Name: "alice", AuthorType: "human",
				Content: content, CreatedAt: time.Now().Format(time.RFC3339),
			}}
			m.clamp()
			for _, view := range []struct {
				what string
				text string
			}{{"list", m.View()}, {"thread", func() string { m.detail = true; return m.View() }()}} {
				for i, line := range strings.Split(view.text, "\n") {
					if n := wide.String(line); n > size[0] {
						t.Errorf("%dx%d %s row %d is %d cells on a wide-ambiguous terminal and %d narrow, over a %d-cell terminal",
							size[0], size[1], view.what, i, n, narrow.String(line), size[0])
					}
				}
			}
		}
	}
}

// Slack is the whole point of not drawing to the edge, and this is what it
// buys: a terminal one cell narrower than the size the app was told does not
// wrap a row, so the frame cannot desynchronise. Without slack every line is
// exactly the terminal's width, one cell is one too many, the row wraps, and
// the emulator consumes a row the renderer does not know about — the title
// band scrolls off the top, the footer and status off the bottom, and a row of
// the list is left behind at the top. The app cannot detect that, because the
// only evidence is a screen it never reads.
//
// A disagreement of more than one cell is a resize, and the app learns about
// that from `tea.WindowSizeMsg`. This is the case where the app is told a size
// and the terminal is not that size, which no message reports.
func TestSlackSurvivesAOneCellTerminalDisagreement(t *testing.T) {
	rows := []store.Row{
		{ID: 1, ThreadID: 1, Channel: "general", Thread: "welcome", Name: "alice", Count: 3,
			Content: "it\u2019s the \u201cbest\u201d release \u2014 the \u2026 one",
			Time:    time.Now().Format(time.RFC3339)},
		{ID: 2, ThreadID: 2, Channel: "glyph-check", Thread: "wide-content", Name: "graphemes", Count: 9,
			Content: "\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466 \uff46\uff55\uff4c\uff4c\uff57\uff69\uff64\uff74\uff68 \u5168\u89d2",
			Time:    time.Now().Format(time.RFC3339)},
	}
	thread := []store.Message{{ID: 1, ThreadID: 1, Name: "alice", AuthorType: "human",
		Content:   "curly \u201cquotes\u201d, an em \u2014 dash, an ellipsis\u2026 and an arrow \u2192",
		CreatedAt: time.Now().Format(time.RFC3339)}}

	for _, told := range []int{80, 120, 173} {
		for _, delta := range []int{-1, 0, 1} {
			terminal := told + delta
			m := model{width: told, height: 49, granularity: store.GranularityMessage,
				rows: rows, thread: thread, threadID: 1}
			m.clamp()
			for i, line := range strings.Split(m.View(), "\n") {
				if got := wideCells.String(line); got > terminal {
					t.Errorf("told %d columns, terminal is %d: view row %d is %d cells and would wrap",
						told, terminal, i, got)
				}
			}
		}
	}
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
