package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// composeModel is the reply box and nothing else. There was one mode per
// create path; the TUI no longer creates anything, so there is one mode.
type composeModel struct {
	active  bool
	width   int
	context string
	text    string
	cursor  int
	err     string
}

func (m *composeModel) resize(w int) {
	if w < 30 {
		m.width = 60
		return
	}
	m.width = w * 80 / 100
}

func (m *composeModel) open(context string) {
	m.active, m.context, m.text, m.cursor, m.err = true, context, "", 0, ""
}

func (m *composeModel) close() {
	m.active, m.context, m.text, m.cursor, m.err = false, "", "", 0, ""
}

// runes is the text as the cursor counts it: the cursor is an index into this
// and never a byte offset. Every edit goes through here, so there is one
// definition of a position and a multi-byte character cannot be cut in half.
func (m composeModel) runes() []rune { return []rune(m.text) }

func (m *composeModel) setRunes(r []rune) { m.text = string(r) }

func (m *composeModel) update(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		m.close()
		return nil
	case tea.KeyEnter:
		if strings.TrimSpace(m.text) == "" {
			m.err = "cannot be empty"
			return nil
		}
		m.err = ""
		text := m.text
		return func() tea.Msg { return composeSendMsg{text: text} }
	case tea.KeyBackspace:
		if m.cursor > 0 {
			m.setRunes(slices.Delete(m.runes(), m.cursor-1, m.cursor))
			m.cursor--
		}
	case tea.KeyDelete:
		if m.cursor < len(m.runes()) {
			m.setRunes(slices.Delete(m.runes(), m.cursor, m.cursor+1))
		}
	case tea.KeyLeft:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyRight:
		if m.cursor < len(m.runes()) {
			m.cursor++
		}
	case tea.KeyRunes, tea.KeySpace:
		runes := msg.Runes
		if len(runes) == 0 && msg.Type == tea.KeySpace {
			runes = []rune{' '}
		}
		if len(runes) > 0 {
			at := min(m.cursor, len(m.runes()))
			m.setRunes(slices.Insert(m.runes(), at, runes...))
			m.cursor = at + len(runes)
		}
	}
	return nil
}

// caretRune is the box's text cursor. It is a plain bar rather than the block
// element a text cursor would rather be, because U+258F is East Asian
// Ambiguous. See TestNoDrawnGlyphIsEastAsianAmbiguous.
const caretRune = '|'

// caret is the text with the cursor drawn between the two halves at the
// cursor, so the box shows where the next keystroke lands. The cursor is a rune
// index and is clamped, so a stale one splits nothing and overruns nothing.
func (m composeModel) caret() string {
	r := []rune(m.text)
	at := min(max(m.cursor, 0), len(r))
	return string(r[:at]) + string(caretRune) + string(r[at:])
}

// asciiBorder is the reply box's border. lipgloss's RoundedBorder and
// NormalBorder are both drawn from East Asian Ambiguous box-drawing glyphs, and
// a box is four rules and four corners: eight cells per line the app and the
// emulator can disagree about, on the one surface drawn over the list.
var asciiBorder = lipgloss.Border{
	Top:         string(ruleRune),
	Bottom:      string(ruleRune),
	Left:        string(paneDividerRune),
	Right:       string(paneDividerRune),
	TopLeft:     "+",
	TopRight:    "+",
	BottomLeft:  "+",
	BottomRight: "+",
}

func (m composeModel) view() string {
	// Sanitized like every other draw site, and not only because the text is
	// untrusted: a paste arrives as a tea.PasteMsg that nothing handles, so what
	// reaches the box is not necessarily keystrokes.
	// See composeTextBudget for the 6.
	budget := max(m.width-composeTextBudget, 1)
	title := truncateCells(termtext.SanitizeLine(m.context), budget, "")
	text := truncateCells(termtext.SanitizeLine(m.caret()), budget, "")
	failure := truncateCells(termtext.SanitizeLine(m.err), budget, "")
	hint := "Enter to send - Esc to cancel"

	// The box is bordered and padded, and lipgloss measures narrow, so the
	// width it is given has to carry the excess or the box is wider than the
	// terminal on a wide-ambiguous one. That makes the width depend on the
	// text, and the text budget depend on the width, so the text is cut to a
	// generous budget, the excess measured, and only then padded to the width
	// the box will actually have. Cutting cannot raise the excess, so one pass
	// is enough. See cells.go.
	excess := excessCells(title) + excessCells(text) + excessCells(failure) + excessCells(hint)
	content := max(m.width-composeBorderCells-excess, 1)
	lines := []string{
		modalTitleStyle.Render(pad(truncateCells(title, content, ""), content)),
		pad(truncateCells(text, content, ""), content),
		modalErrorStyle.Render(pad(truncateCells(failure, content, ""), content)),
		modalHintStyle.Render(pad(truncateCells(hint, content, ""), content)),
	}
	return lipgloss.NewStyle().
		Border(asciiBorder).
		BorderForeground(modalBorder).
		Foreground(modalFg).
		Padding(0, 2).
		Width(content).
		Render(strings.Join(lines, "\n"))
}

// composeTextBudget is the cells the box leaves for its border and its one-cell
// horizontal padding, and composeBorderCells is the subset of those that the
// box's own width must pay for before its content is laid out.
const (
	composeTextBudget  = 6
	composeBorderCells = 2
)
