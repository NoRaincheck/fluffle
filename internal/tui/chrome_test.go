package tui

import (
	"strings"
	"testing"
	"time"

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
