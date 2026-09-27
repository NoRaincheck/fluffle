package tui

import (
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
			m.text = m.text[:m.cursor-1] + m.text[m.cursor:]
			m.cursor--
		}
	case tea.KeyLeft:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyRight:
		if m.cursor < len(m.text) {
			m.cursor++
		}
	case tea.KeyRunes, tea.KeySpace:
		runes := msg.Runes
		if len(runes) == 0 && msg.Type == tea.KeySpace {
			runes = []rune{' '}
		}
		for _, r := range runes {
			m.text = m.text[:m.cursor] + string(r) + m.text[m.cursor:]
			m.cursor++
		}
	}
	return nil
}

func (m composeModel) view() string {
	cursor := "▏ "
	if m.cursor < len(m.text) {
		cursor = "▏"
	}
	lines := []string{
		modalTitleStyle.Render(termtext.Truncate(termtext.SanitizeLine(m.context), m.width-6, "")),
		pad(termtext.Truncate(m.text+cursor, m.width-6, ""), m.width-6),
		m.errLine(),
		modalHintStyle.Render("Enter to send · Esc to cancel"),
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
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
