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

	// Panel layout
	treeHeight int
	chatHeight int
	width      int
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
		return channelsFetchedMsg{channels: channels, err: err}
	}
}

func (m *model) fetchThreads(channelID int64) tea.Cmd {
	return func() tea.Msg {
		threads, err := m.api.ListThreads(nil, channelID)
		return threadsFetchedMsg{channelID: channelID, threads: threads, err: err}
	}
}

func (m *model) fetchMessages(threadID int64) tea.Cmd {
	return func() tea.Msg {
		msgs, err := m.api.ListMessages(nil, threadID)
		return messagesFetchedMsg{threadID: threadID, messages: msgs, err: err}
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
	case channelsFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.channels = msg.channels
		m.tree.SetChannels(msg.channels)
		return m, nil

	case threadsFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.threads = msg.threads
		m.renderThreadList(msg.channelID, msg.threads)
		return m, nil

	case messagesFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.chat.SetMessages(msg.messages)
		m.chat.selectedThd = msg.threadID
		return m, nil

	case tea.WindowSizeMsg:
		// Reserve 2 lines for shortcuts + status bar
		panelHeight := msg.Height - 2
		if panelHeight < 2 {
			panelHeight = 2
		}
		m.width = msg.Width
		m.treeHeight = panelHeight
		m.chatHeight = panelHeight
		m.tree.SetSize(msg.Width, m.treeHeight)
		m.chat.SetSize(msg.Width, m.chatHeight)
		m.compose.width = msg.Width
		m.compose.height = panelHeight / 2
		if m.compose.height < 3 {
			m.compose.height = 3
		}

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
			m.compose.Open(composeModeMessage, fmt.Sprintf("# %s > %s", m.chat.channelTitle, th.Title), m.compose.height)
			return m, nil
		}
		ch := m.tree.SelectedChannel()
		if ch != nil {
			m.compose.Open(composeModeMessage, ch.Name, m.compose.height)
			return m, nil
		}
	case focusChat:
		msgItem := m.chat.SelectedMessage()
		if msgItem != nil {
			m.compose.Open(composeModeReply, "Reply to: "+truncate(msgItem.Content, 40), m.compose.height)
			m.compose.state.threadID = msgItem.ThreadID
			m.compose.state.parentID = msgItem.ID
			return m, nil
		}
		// No message selected but we have threads
		if m.chat.selectedThd > 0 {
			m.compose.Open(composeModeMessage, fmt.Sprintf("# %s > %s", m.chat.channelTitle, m.chat.threadTitle), m.compose.height)
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

	threadID := m.compose.state.threadID
	parentID := m.compose.state.parentID

	if threadID == 0 {
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
		return m, nil
	}

	m.compose.Close()
	m.status = "message sent"

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
		// Side-by-side layout: tree | chat
		treeView := m.tree.View()
		chatView := m.chat.View()
		panelStyle := lipgloss.NewStyle().Width(m.width)
		content = panelStyle.Render(
			lipgloss.JoinHorizontal(lipgloss.Top,
				treeStyle.Render(treeView),
				chatStyle.Width(m.width-28).Render(chatView),
			),
		)
	}

	// Shortcuts bar
	content += "\n" + shortcutsView(m.focus)

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

func shortcutsView(focus focus) string {
	var parts []string

	parts = append(parts, hintKeyStyle.Render("q")+" quit")
	parts = append(parts, hintKeyStyle.Render("Tab")+" switch")
	parts = append(parts, hintKeyStyle.Render("c")+" compose")
	parts = append(parts, hintKeyStyle.Render("/")+" filter")
	parts = append(parts, hintKeyStyle.Render("s")+" sort")

	if focus == focusTree {
		parts = append(parts, hintKeyStyle.Render("↑↓")+" navigate")
		parts = append(parts, hintKeyStyle.Render("Enter")+" open")
	} else {
		parts = append(parts, hintKeyStyle.Render("↑↓")+" navigate")
		parts = append(parts, hintKeyStyle.Render("Enter")+" compose")
	}

	return hintStyle.Render(lipgloss.NewStyle().Width(0).Render(lipgloss.JoinHorizontal(lipgloss.Top, parts...)))
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
