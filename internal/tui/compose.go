package tui

import (
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type composeMode int

const (
	composeModeMessage composeMode = iota
	composeModeReply
	composeModeNewThread
)

type composeState struct {
	mode     composeMode
	context  string // thread title or "reply to: ..."
	text     string
	cursor   int
	error    string
	threadID int64
	parentID int64
}

type composeModel struct {
	state  composeState
	active bool
	width  int
	height int
}

func (m composeModel) Init() tea.Cmd {
	return nil
}

func (m *composeModel) Open(mode composeMode, context string, maxH int) {
	m.active = true
	m.state = composeState{
		mode:    mode,
		context: context,
		text:    "",
		cursor:  0,
		error:   "",
	}
	m.width = 60
	// Minimum 5 lines (header + input + error/hint + padding), scale up to available space
	m.height = 5
	if maxH > 5 {
		m.height = minInt(maxH-2, 12)
	}
}

func (m *composeModel) Close() {
	m.active = false
	m.state = composeState{}
}

func (m *composeModel) IsActive() bool {
	return m.active
}

func (m *composeModel) Text() string {
	return m.state.text
}

func (m *composeModel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch key := msg.Key(); key.Code {
		case tea.KeyEscape:
			m.Close()
			return nil
		case tea.KeyEnter:
			if m.state.text != "" {
				return func() tea.Msg {
					return composeSendMsg{text: m.state.text, mode: m.state.mode, context: m.state.context}
				}
			}
			return nil
		case tea.KeyBackspace:
			if m.state.cursor > 0 && len(m.state.text) > 0 {
				m.state.text = m.state.text[:m.state.cursor-1] + m.state.text[m.state.cursor:]
				m.state.cursor--
			}
		case tea.KeyDelete:
			if m.state.cursor < len(m.state.text) {
				m.state.text = m.state.text[:m.state.cursor] + m.state.text[m.state.cursor+1:]
			}
		case tea.KeyLeft:
			if m.state.cursor > 0 {
				m.state.cursor--
			}
		case tea.KeyRight:
			if m.state.cursor < len(m.state.text) {
				m.state.cursor++
			}
		default:
			if len(key.Text) == 1 {
				m.state.text = m.state.text[:m.state.cursor] + key.Text + m.state.text[m.state.cursor:]
				m.state.cursor++
			}
		}
	}
	return nil
}

func (m composeModel) View() string {
	if !m.IsActive() {
		return ""
	}

	lines := make([]string, 0, m.height)

	// Context header
	lines = append(lines, modalTitleStyle.Render(m.state.context))

	// Input line
	cursorChar := "│"
	if m.state.cursor >= len(m.state.text) {
		cursorChar = "│ "
	}
	inputLine := "▸ " + m.state.text + cursorChar
	lines = append(lines, inputLine)

	// Error if any
	if m.state.error != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("204")).Render("  "+m.state.error))
	} else {
		lines = append(lines, "")
	}

	// Hints
	switch m.state.mode {
	case composeModeMessage:
		lines = append(lines, modalHintStyle.Render("Enter to send, Esc to cancel"))
	case composeModeReply:
		lines = append(lines, modalHintStyle.Render("Enter to reply, Esc to cancel"))
	case composeModeNewThread:
		lines = append(lines, modalHintStyle.Render("Enter to create thread, Esc to cancel"))
	}

	// Pad to height
	for len(lines) < m.height {
		lines = append(lines, "")
	}

	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(modalBorder).
		Background(modalBg).
		Foreground(modalFg).
		Padding(0, 2).
		Width(m.width).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

type channelsFetchedMsg struct {
	channels []store.Channel
	err      error
}

type threadsFetchedMsg struct {
	channelID int64
	threads   []store.Thread
	err       error
}

type messagesFetchedMsg struct {
	threadID int64
	messages []store.Message
	err      error
}

type composeSendMsg struct {
	text    string
	mode    composeMode
	context string
}

type threadCreatedMsg struct {
	channelID int64
	threadID  int64
	title     string
	err       error
}

func (m *composeModel) SetError(err string) {
	m.state.error = err
}

func (m *composeModel) ClearError() {
	m.state.error = ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func composeContext(mode composeMode, context string) string {
	switch mode {
	case composeModeMessage:
		return context
	case composeModeReply:
		preview := truncate(context, 40)
		return "Reply to: " + preview
	default:
		return ""
	}
}
