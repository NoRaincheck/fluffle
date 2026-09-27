package tui

import tea "github.com/charmbracelet/bubbletea"

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

func (m composeModel) view() string { return "" }

func (m *composeModel) update(msg tea.KeyMsg) tea.Cmd { return nil }
