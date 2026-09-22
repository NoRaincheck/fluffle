package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type focus int

const (
	focusTree focus = iota
	focusChat
)

type model struct {
	focus    focus
	quitting bool

	tree    treeModel
	chat    chatModel
	compose composeModel
	status  string

	api *apiClient

	channels []store.Channel
	threads  []store.Thread
}

func New(base string) tea.Model {
	m := model{
		focus: focusTree,
		api:   NewAPIClient(base),
	}
	m.tree = newTreeModel()
	m.chat = chatModel{width: 80, height: 20}
	m.compose = composeModel{width: 60, height: 6}
	return m
}

func (m model) Init() tea.Cmd {
	return m.fetchChannels()
}

func (m *model) fetchChannels() tea.Cmd {
	return func() tea.Msg {
		channels, err := m.api.ListChannels(nil, "", "")
		if err != nil {
			m.status = fmt.Sprintf("error: %v", err)
			return nil
		}
		m.channels = channels
		m.tree.SetChannels(channels)
		return nil
	}
}

func (m *model) fetchThreads(channelID int64) tea.Cmd {
	return func() tea.Msg {
		threads, err := m.api.ListThreads(nil, channelID)
		if err != nil {
			m.status = fmt.Sprintf("error: %v", err)
			return nil
		}
		m.threads = threads
		m.renderThreadList(channelID, threads)
		return nil
	}
}

func (m *model) fetchMessages(threadID int64) tea.Cmd {
	return func() tea.Msg {
		msgs, err := m.api.ListMessages(nil, threadID)
		if err != nil {
			m.status = fmt.Sprintf("error: %v", err)
			return nil
		}
		m.chat.SetMessages(msgs)
		m.chat.selectedThd = threadID
		return nil
	}
}

func (m *model) renderThreadList(channelID int64, threads []store.Thread) {
	if len(threads) == 0 {
		m.chat.SetMessages([]store.Message{})
		return
	}

	// Build a synthetic message list from threads
	msgs := make([]store.Message, 0, len(threads))
	for i, th := range threads {
		content := th.Title
		if len(th.Title) > 0 {
			// Get last message preview
			lastMsgs, err := m.api.ListMessages(nil, th.ID)
			if err == nil && len(lastMsgs) > 0 {
				last := lastMsgs[len(lastMsgs)-1]
				content = truncate(last.Author, 12) + ": " + truncate(last.Content, 40)
			}
		}
		msgs = append(msgs, store.Message{
			ID:        int64(i),
			ThreadID:  th.ID,
			Seq:       int64(i),
			Author:    th.Title,
			Content:   content,
			CreatedAt: th.CreatedAt,
		})
	}
	m.chat.SetMessages(msgs)
	m.chat.channelTitle = ""
	m.chat.threadTitle = ""
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.tree.SetSize(msg.Width, msg.Height)
		m.chat.SetSize(msg.Width, msg.Height)
		m.compose.width = msg.Width
		m.compose.height = msg.Height

	case composeSendMsg:
		return m.handleComposeSend(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Forward to compose if active
	if m.compose.IsActive() {
		m.compose.Update(msg)
		return m, nil
	}

	// Forward to focused panel
	switch m.focus {
	case focusTree:
		m.tree.Update(msg)
	case focusChat:
		m.chat.Update(msg)
	}

	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	switch key.Code {
	case tea.KeyTab:
		if m.compose.IsActive() {
			return m, nil
		}
		if m.focus == focusTree {
			m.focus = focusChat
		} else {
			m.focus = focusTree
		}

	case 'c':
		if key.Mod&tea.ModCtrl != 0 {
			m.quitting = true
			return m, tea.Quit
		}
		if m.compose.IsActive() {
			m.compose.Close()
			return m, nil
		}
		return m.handleCompose(msg)

	case 'q':
		m.quitting = true
		return m, tea.Quit

	case '/':
		if !m.compose.IsActive() {
			m.tree.SetFilter("") // toggle filter input
		}

	case 's':
		if !m.compose.IsActive() {
			m.tree.ToggleSort()
		}

	case tea.KeyEscape:
		if m.compose.IsActive() {
			m.compose.Close()
			return m, nil
		}
		// Deselect chat message
		if m.focus == focusChat && m.chat.cursor >= 0 {
			m.chat.cursor = -1
		}

	default:
		// Don't handle keys when compose is active (compose handles its own)
		if !m.compose.IsActive() {
			// Keys are forwarded to the focused panel above
		}
	}

	return m, nil
}

func (m *model) handleCompose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.focus {
	case focusTree:
		// On tree: check if we have a selected thread
		th := m.tree.SelectedThread()
		if th != nil {
			m.compose.Open(composeModeMessage, fmt.Sprintf("# %s > %s", m.chat.channelTitle, th.Title))
			return m, nil
		}
		ch := m.tree.SelectedChannel()
		if ch != nil {
			m.compose.Open(composeModeMessage, ch.Name)
			return m, nil
		}
	case focusChat:
		msgItem := m.chat.SelectedMessage()
		if msgItem != nil {
			m.compose.Open(composeModeReply, "Reply to: "+truncate(msgItem.Content, 40))
			m.compose.state.threadID = msgItem.ThreadID
			m.compose.state.parentID = msgItem.ID
			return m, nil
		}
		// No message selected but we have threads
		if m.chat.selectedThd > 0 {
			m.compose.Open(composeModeMessage, fmt.Sprintf("# %s > %s", m.chat.channelTitle, m.chat.threadTitle))
			m.compose.state.threadID = m.chat.selectedThd
			return m, nil
		}
	}

	return m, nil
}

func (m *model) handleComposeSend(msg composeSendMsg) (tea.Model, tea.Cmd) {
	if msg.text == "" {
		m.compose.SetError("message cannot be empty")
		return m, nil
	}

	m.compose.ClearError()

	// Determine thread and parent
	threadID := m.compose.state.threadID
	parentID := m.compose.state.parentID

	if threadID == 0 {
		// Try to find a thread from the tree or chat
		th := m.tree.SelectedThread()
		if th != nil {
			threadID = th.ID
		}
	}

	if threadID == 0 {
		m.compose.SetError("no thread selected")
		return m, nil
	}

	err := m.api.SendMessage(nil, threadID, parentID, msg.text)
	if err != nil {
		m.compose.SetError(err.Error())
		m.compose.Close()
		return m, nil
	}

	m.compose.Close()
	m.status = "message sent"

	// Refresh messages
	return m, m.fetchMessages(threadID)
}

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	var content string
	if m.compose.IsActive() {
		// Dim background
		treeView := m.tree.View()
		chatView := m.chat.View()
		dimmed := lipgloss.NewStyle().Inline(true).Render(treeView + "\n" + chatView)
		composeView := m.compose.View()
		content = lipgloss.NewStyle().
			Background(modalOverlay).
			Render(dimmed) + "\n" +
			center(composeView, m.compose.width)
	} else {
		treeView := m.tree.View()
		chatView := m.chat.View()
		content = treeView + "\n" + chatView
	}

	// Status bar
	statusLine := m.status
	m.status = "" // clear after showing
	if statusLine != "" {
		content += "\n" + statusStyle.Render(statusLine)
	} else {
		content += "\n" + statusStyle.Render(" flf tui  ")
	}

	return tea.NewView(content)
}

func center(s string, width int) string {
	lines := strings.Split(s, "\n")
	centered := make([]string, len(lines))
	for i, line := range lines {
		padding := (width - len(line)) / 2
		if padding < 0 {
			padding = 0
		}
		centered[i] = strings.Repeat(" ", padding) + line
	}
	return lipgloss.JoinVertical(lipgloss.Left, centered...)
}
