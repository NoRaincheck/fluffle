package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type chatModel struct {
	messages     []store.Message
	cursor       int
	selectedMsg  int64 // selected message ID for reply context
	selectedThd  int64 // selected thread ID
	channelTitle string
	threadTitle  string
	offset       int
	width        int
	height       int
}

func (m chatModel) Init() tea.Cmd {
	return nil
}

func (m *chatModel) SetMessages(msgs []store.Message) {
	m.messages = msgs
	if m.cursor >= len(msgs) {
		m.cursor = len(msgs) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *chatModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *chatModel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch key := msg.Key(); key.Code {
		case tea.KeyUp, 'k':
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown, 'j':
			if m.cursor < len(m.messages)-1 {
				m.cursor++
			}
		case tea.KeyHome:
			m.cursor = 0
		case tea.KeyEnd:
			m.cursor = len(m.messages) - 1
		}
	}
	return nil
}

func (m chatModel) View() string {
	var lines []string

	// Header
	header := ""
	if m.threadTitle != "" {
		header = fmt.Sprintf("# %s > %s", m.channelTitle, m.threadTitle)
	} else if m.channelTitle != "" {
		header = fmt.Sprintf("# %s", m.channelTitle)
	}
	if header != "" {
		lines = append(lines, chatHeaderStyle.Render(header))
		lines = append(lines, chatHeaderStyle.Render(strings.Repeat("─", min(m.width-2, 60))))
	}

	if len(m.messages) == 0 {
		lines = append(lines, chatMsgStyle.Render("  no messages yet"))
	} else {
		for i, msg := range m.messages {
			line := m.renderMessage(msg, i)
			lines = append(lines, line)
		}
	}

	// Truncate to height
	for len(lines) > m.height {
		lines = lines[1:]
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m chatModel) renderMessage(msg store.Message, idx int) string {
	timeStr := formatTime(msg.CreatedAt)
	author := truncate(msg.Author, 12)
	content := truncate(msg.Content, m.width-30)

	line := fmt.Sprintf("[%s] %s: %s", timeStr, author, content)

	if idx == m.cursor {
		return chatMsgSelectedStyle.Render("▸ " + line)
	}
	return chatMsgStyle.Render("  " + line)
}

func (m chatModel) SelectedMessage() *store.Message {
	if m.cursor >= 0 && m.cursor < len(m.messages) {
		return &m.messages[m.cursor]
	}
	return nil
}

func (m chatModel) SelectedThreadID() int64 {
	return m.selectedThd
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
