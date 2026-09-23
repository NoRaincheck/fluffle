package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type chatMode int

const (
	modeEmpty chatMode = iota
	modeThreadList
	modeMessageList
)

type chatModel struct {
	mode         chatMode
	threads      []store.Thread
	messages     []store.Message
	cursor       int
	selectedThd  int64
	channelTitle string
	threadTitle  string
	offset       int
	width        int
	height       int
}

func (m chatModel) Init() tea.Cmd { return nil }

func (m *chatModel) SetThreads(threads []store.Thread, channelName string) {
	if threads == nil {
		threads = []store.Thread{}
	}
	m.mode = modeThreadList
	m.threads = threads
	m.messages = nil
	m.channelTitle = channelName
	m.threadTitle = ""
	m.cursor = 0
}

func (m *chatModel) SetMessages(msgs []store.Message, channelName, threadTitle string, threadID int64) {
	if msgs == nil {
		msgs = []store.Message{}
	}
	m.mode = modeMessageList
	m.messages = msgs
	m.threads = nil
	m.channelTitle = channelName
	m.threadTitle = threadTitle
	m.selectedThd = threadID
	if m.cursor >= len(msgs) {
		m.cursor = len(msgs) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *chatModel) SetEmpty(channelName string) {
	m.mode = modeEmpty
	m.channelTitle = channelName
	m.threadTitle = ""
	m.threads = nil
	m.messages = nil
	m.cursor = 0
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
			max := 0
			switch m.mode {
			case modeThreadList:
				max = len(m.threads) - 1
			case modeMessageList:
				max = len(m.messages) - 1
			}
			if m.cursor < max {
				m.cursor++
			}
		case tea.KeyHome:
			m.cursor = 0
		case tea.KeyEnd:
			switch m.mode {
			case modeThreadList:
				m.cursor = len(m.threads) - 1
			case modeMessageList:
				m.cursor = len(m.messages) - 1
			}
		}
	}
	return nil
}

func (m chatModel) View() string {
	var lines []string
	header := ""
	switch m.mode {
	case modeThreadList:
		if m.channelTitle != "" {
			header = fmt.Sprintf("# %s — threads (%d)", m.channelTitle, len(m.threads))
		}
	case modeMessageList:
		if m.threadTitle != "" {
			header = fmt.Sprintf("# %s › %s", m.channelTitle, m.threadTitle)
		} else if m.channelTitle != "" {
			header = fmt.Sprintf("# %s", m.channelTitle)
		}
	default:
		if m.channelTitle != "" {
			header = fmt.Sprintf("# %s", m.channelTitle)
		}
	}
	if header != "" {
		lines = append(lines, chatHeaderStyle.Render(header))
		lines = append(lines, chatHeaderStyle.Render(strings.Repeat("─", min(m.width-2, 60))))
	}
	switch m.mode {
	case modeEmpty:
		lines = append(lines, chatMsgStyle.Render("  no threads yet — press n to create one"))
	case modeThreadList:
		if len(m.threads) == 0 {
			lines = append(lines, chatMsgStyle.Render("  no threads — press n to create"))
		} else {
			for i, th := range m.threads {
				line := fmt.Sprintf("  # %s", th.Title)
				if i == m.cursor {
					line = chatMsgSelectedStyle.Render("▸ " + strings.TrimPrefix(line, "  "))
				} else {
					line = chatMsgStyle.Render(line)
				}
				lines = append(lines, line)
			}
		}
	case modeMessageList:
		if len(m.messages) == 0 {
			lines = append(lines, chatMsgStyle.Render("  no messages yet — press c to post"))
		} else {
			for i, msg := range m.messages {
				line := m.renderMessage(msg, i)
				lines = append(lines, line)
			}
		}
	}
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
	reply := ""
	if msg.ParentID.Valid {
		reply = " ↳"
	}
	content := truncate(msg.Content, m.width-30)
	line := fmt.Sprintf("[%s] %s%s: %s", timeStr, author, reply, content)
	if idx == m.cursor {
		return chatMsgSelectedStyle.Render("▸ " + line)
	}
	return chatMsgStyle.Render("  " + line)
}

func (m chatModel) SelectedMessage() *store.Message {
	if m.mode != modeMessageList {
		return nil
	}
	if m.cursor >= 0 && m.cursor < len(m.messages) {
		return &m.messages[m.cursor]
	}
	return nil
}

func (m chatModel) SelectedThread() *store.Thread {
	if m.mode != modeThreadList {
		return nil
	}
	if m.cursor >= 0 && m.cursor < len(m.threads) {
		return &m.threads[m.cursor]
	}
	return nil
}

func (m chatModel) SelectedThreadID() int64 { return m.selectedThd }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
