package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	context  string
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
	if m.width < 30 {
		m.width = 60
	}
	m.height = 4
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
		switch msg.Type {
		case tea.KeyEsc:
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
		case tea.KeyRunes:
			for _, r := range msg.Runes {
				m.state.text = m.state.text[:m.state.cursor] + string(r) + m.state.text[m.state.cursor:]
				m.state.cursor += len(string(r))
			}
		}
	}
	return nil
}

func (m composeModel) View() string {
	if !m.IsActive() {
		return ""
	}

	lines := make([]string, 0, 4)

	lines = append(lines, modalTitleStyle.Render(m.state.context))

	cursorChar := "│"
	if m.state.cursor >= len(m.state.text) {
		cursorChar = "│ "
	}
	inputLine := "▸ " + m.state.text + cursorChar
	lines = append(lines, inputLine)

	if m.state.error != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("204")).Render("  "+m.state.error))
	} else {
		lines = append(lines, "")
	}

	lines = append(lines, modalHintStyle.Render("Enter to reply, Esc to cancel"))

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

type previewThreadsFetchedMsg struct {
	channelID int64
	threads   []store.Thread
	err       error
}

type previewMessagesFetchedMsg struct {
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

type filterModel struct {
	active bool
	text   string
	cursor int
	width  int
	height int
}

func (m *filterModel) Open(initial string, maxW int) {
	m.active = true
	m.text = initial
	m.cursor = len(initial)
	if maxW < 30 {
		maxW = 60
	}
	m.width = maxW
	m.height = 4
}

func (m *filterModel) Close() {
	m.active = false
	m.text = ""
	m.cursor = 0
}

func (m *filterModel) IsActive() bool { return m.active }

func (m *filterModel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			m.Close()
			return nil
		case tea.KeyEnter:
			t := m.text
			m.Close()
			return func() tea.Msg { return filterAppliedMsg{text: t} }
		case tea.KeyBackspace:
			if m.cursor > 0 && len(m.text) > 0 {
				m.text = m.text[:m.cursor-1] + m.text[m.cursor:]
				m.cursor--
			}
		case tea.KeyDelete:
			if m.cursor < len(m.text) {
				m.text = m.text[:m.cursor] + m.text[m.cursor+1:]
			}
		case tea.KeyLeft:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyRight:
			if m.cursor < len(m.text) {
				m.cursor++
			}
		case tea.KeyRunes:
			for _, r := range msg.Runes {
				m.text = m.text[:m.cursor] + string(r) + m.text[m.cursor:]
				m.cursor += len(string(r))
			}
		}
	}
	return nil
}

func (m filterModel) View() string {
	if !m.IsActive() {
		return ""
	}
	title := modalTitleStyle.Render("Filter: channel[/thread]  (empty to clear)")
	cursorChar := "│"
	if m.cursor >= len(m.text) {
		cursorChar = "│ "
	}
	inputLine := "▸ " + m.text + cursorChar
	lines := []string{title, inputLine, "", modalHintStyle.Render("Enter to apply, Esc to cancel")}
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(modalBorder).
		Background(modalBg).
		Foreground(modalFg).
		Padding(0, 2).
		Width(m.width).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

type filterAppliedMsg struct{ text string }

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
	case composeModeNewThread:
		return context
	default:
		return context
	}
}
