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
	lines := []string{
		modalTitleStyle.Render(termtext.Truncate(termtext.SanitizeLine(m.context), m.width-6, "")),
		// Sanitized like every other draw site, and not only because the text
		// is untrusted: a paste arrives as a tea.PasteMsg that nothing handles,
		// so what reaches the box is not necessarily keystrokes.
		pad(termtext.Truncate(termtext.SanitizeLine(m.caret()), m.width-6, ""), m.width-6),
		m.errLine(),
		modalHintStyle.Render("Enter to send - Esc to cancel"),
	}
	return lipgloss.NewStyle().
		Border(asciiBorder).
		BorderForeground(modalBorder).
		Foreground(modalFg).
		Padding(0, 2).
		Width(m.width - 2).
		Render(strings.Join(lines, "\n"))
}

func (m composeModel) errLine() string {
	if m.err == "" {
		return ""
	}
	return modalErrorStyle.Render(m.err)
}
