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

type viewKind int

const (
	viewHome viewKind = iota
	viewChannel
	viewThread
	viewModal
)

type model struct {
	focus             focus
	currentView       viewKind
	quitting          bool
	tree              treeModel
	chat              chatModel
	compose           composeModel
	status            string
	api               *apiClient
	channels          []store.Channel
	threads           map[int64][]store.Thread
	selectedChannelID int64
	selectedThreadID  int64
	threadsByChannel  map[int64][]store.Thread
	viewStack         []viewKind
	treeHeight        int
	chatHeight        int
	width             int
	height            int
}

func New(base string) tea.Model {
	m := model{
		focus:            focusTree,
		currentView:      viewHome,
		api:              NewAPIClient(base),
		threadsByChannel: make(map[int64][]store.Thread),
		threads:          make(map[int64][]store.Thread),
	}
	m.tree = newTreeModel()
	m.chat = chatModel{width: 80, height: 20}
	m.compose = composeModel{width: 60, height: 6}
	return m
}

func (m model) Init() tea.Cmd { return m.fetchChannels() }

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

func (m *model) createThread(channelID int64, title string) tea.Cmd {
	return func() tea.Msg {
		id, err := m.api.CreateThread(nil, channelID, title)
		return threadCreatedMsg{channelID: channelID, threadID: id, title: title, err: err}
	}
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
		if len(msg.channels) > 0 {
			m.status = fmt.Sprintf("%d channels — ↑↓ navigate, Enter open, n new thread, q quit", len(msg.channels))
		} else {
			m.status = "no channels — run: flf channel create --orphaned --name demo"
		}
		return m, nil

	case threadsFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.threadsByChannel[msg.channelID] = msg.threads
		m.threads[msg.channelID] = msg.threads
		m.tree.SetThreads(msg.channelID, msg.threads)
		chName := ""
		for _, ch := range m.channels {
			if ch.ID == msg.channelID {
				chName = ch.Name
				break
			}
		}
		m.selectedChannelID = msg.channelID
		m.currentView = viewChannel
		m.chat.SetThreads(msg.threads, chName)
		m.status = fmt.Sprintf("channel %s: %d threads — Enter thread, n new, Esc back", chName, len(msg.threads))
		return m, nil

	case threadCreatedMsg:
		if msg.err != nil {
			m.compose.SetError(msg.err.Error())
			return m, nil
		}
		m.compose.Close()
		m.status = fmt.Sprintf("thread %q created", msg.title)
		return m, m.fetchThreads(msg.channelID)

	case messagesFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		chName := ""
		thTitle := ""
		for _, ch := range m.channels {
			for _, th := range m.threadsByChannel[ch.ID] {
				if th.ID == msg.threadID {
					chName = ch.Name
					thTitle = th.Title
					break
				}
			}
		}
		if chName == "" {
			for _, ch := range m.channels {
				if ch.ID == m.selectedChannelID {
					chName = ch.Name
					break
				}
			}
		}
		m.selectedThreadID = msg.threadID
		m.currentView = viewThread
		viewStackPush := m.currentView
		_ = viewStackPush
		m.chat.SetMessages(msg.messages, chName, thTitle, msg.threadID)
		m.status = fmt.Sprintf("thread %s: %d messages — c post, r reply, Esc back", thTitle, len(msg.messages))
		return m, nil

	case composeSendMsg:
		return m.handleComposeSend(msg)

	case tea.WindowSizeMsg:
		panelHeight := msg.Height - 2
		if panelHeight < 2 {
			panelHeight = 2
		}
		m.width = msg.Width
		m.height = msg.Height
		m.treeHeight = panelHeight
		m.chatHeight = panelHeight
		m.tree.SetSize(msg.Width, m.treeHeight)
		m.chat.SetSize(msg.Width, m.chatHeight)
		m.compose.width = msg.Width
		m.compose.height = panelHeight / 2
		if m.compose.height < 3 {
			m.compose.height = 3
		}
		return m, nil

	case tea.KeyMsg:
		if m.compose.IsActive() {
			if msg.Key().Code == 'c' && msg.Key().Mod&tea.ModCtrl != 0 {
				m.quitting = true
				return m, tea.Quit
			}
			cmd := m.compose.Update(msg)
			return m, cmd
		}
		return m.handleKey(msg)
	}

	if m.compose.IsActive() {
		cmd := m.compose.Update(msg)
		return m, cmd
	}

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
		if m.splitActive() {
			if m.focus == focusTree {
				m.focus = focusChat
			} else {
				m.focus = focusTree
			}
			return m, nil
		}
		return m, nil

	case tea.KeyEnter:
		return m.handleEnter()

	case tea.KeyEscape:
		return m.handleEsc()

	default:
		if key.Code == 'c' && key.Mod&tea.ModCtrl != 0 {
			m.quitting = true
			return m, tea.Quit
		}
		if key.Code == tea.KeyEscape {
			return m.handleEsc()
		}
	}

	switch key.Text {
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "c":
		return m.handleCompose(msg)
	case "n":
		return m.handleNewThread()
	case "r":
		return m.handleReply()
	case "/":
		m.tree.SetFilter("")
		return m, nil
	case "s":
		m.tree.ToggleSort()
		return m, nil
	}

	// Forward navigation to focused pane (already done for tree/chat via Update fallback, but ensure tree nav works)
	switch m.focus {
	case focusTree:
		m.tree.Update(msg)
	case focusChat:
		m.chat.Update(msg)
	}
	return m, nil
}

func (m *model) handleEnter() (tea.Model, tea.Cmd) {
	if m.focus == focusChat {
		// In chat pane: if thread list mode, open selected thread
		if m.chat.mode == modeThreadList {
			th := m.chat.SelectedThread()
			if th != nil {
				return m, m.fetchMessages(th.ID)
			}
		} else if m.chat.mode == modeMessageList {
			// reply to selected message
			return m.handleReply()
		}
		return m, nil
	}
	// FocusTree: dispatch based on tree selection
	th := m.tree.SelectedThread()
	if th != nil && th.ID != 0 {
		return m, m.fetchMessages(th.ID)
	}
	ch := m.tree.SelectedChannel()
	if ch != nil {
		m.selectedChannelID = ch.ID
		return m, m.fetchThreads(ch.ID)
	}
	// Repo header selected: no-op
	m.status = "select a channel (🗨) — ↑↓ navigate, Enter open"
	return m, nil
}

func (m *model) handleEsc() (tea.Model, tea.Cmd) {
	if m.currentView == viewThread {
		m.currentView = viewChannel
		chName := ""
		for _, ch := range m.channels {
			if ch.ID == m.selectedChannelID {
				chName = ch.Name
				break
			}
		}
		threads := m.threadsByChannel[m.selectedChannelID]
		m.chat.SetThreads(threads, chName)
		m.status = "back to threads — Enter thread, n new, Esc home"
		return m, nil
	}
	if m.currentView == viewChannel {
		m.currentView = viewHome
		m.chat.SetEmpty("")
		m.selectedChannelID = 0
		m.selectedThreadID = 0
		m.status = "home — ↑↓ channels, Enter open"
		return m, nil
	}
	return m, nil
}

func (m *model) handleNewThread() (tea.Model, tea.Cmd) {
	ch := m.tree.SelectedChannel()
	if ch == nil && m.selectedChannelID != 0 {
		for _, c := range m.channels {
			if c.ID == m.selectedChannelID {
				ch = &c
				break
			}
		}
	}
	if ch == nil {
		m.status = "select a channel first — ↑↓ then n"
		return m, nil
	}
	m.selectedChannelID = ch.ID
	m.compose.Open(composeModeNewThread, fmt.Sprintf("New thread in #%s", ch.Name), m.compose.height)
	m.compose.state.threadID = 0
	m.compose.state.parentID = 0
	// stash channel id for create
	m.compose.state.threadID = ch.ID
	return m, nil
}

func (m *model) handleCompose(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.currentView {
	case viewThread:
		msgItem := m.chat.SelectedMessage()
		if msgItem != nil && m.chat.mode == modeMessageList {
			// default to reply if message selected, else plain post
			// Use c for plain, r for reply explicitly
			m.compose.Open(composeModeMessage, fmt.Sprintf("# %s › %s", m.chat.channelTitle, m.chat.threadTitle), m.compose.height)
			m.compose.state.threadID = m.selectedThreadID
			m.compose.state.parentID = 0
			return m, nil
		}
		m.compose.Open(composeModeMessage, fmt.Sprintf("# %s › %s", m.chat.channelTitle, m.chat.threadTitle), m.compose.height)
		m.compose.state.threadID = m.selectedThreadID
		return m, nil
	case viewChannel:
		th := m.chat.SelectedThread()
		if th != nil {
			m.compose.Open(composeModeMessage, fmt.Sprintf("# %s › %s", m.chat.channelTitle, th.Title), m.compose.height)
			m.compose.state.threadID = th.ID
			return m, nil
		}
		if m.selectedChannelID != 0 {
			// No thread selected but channel view: prompt new thread
			return m.handleNewThread()
		}
	default:
		th := m.tree.SelectedThread()
		if th != nil {
			m.compose.Open(composeModeMessage, fmt.Sprintf("Reply in %s", th.Title), m.compose.height)
			m.compose.state.threadID = th.ID
			return m, nil
		}
		ch := m.tree.SelectedChannel()
		if ch != nil {
			if m.chat.mode == modeThreadList {
				th2 := m.chat.SelectedThread()
				if th2 != nil {
					m.compose.Open(composeModeMessage, fmt.Sprintf("# %s › %s", ch.Name, th2.Title), m.compose.height)
					m.compose.state.threadID = th2.ID
					return m, nil
				}
			}
			// fallback: new thread
			return m.handleNewThread()
		}
	}
	m.status = "select a channel or thread first"
	return m, nil
}

func (m *model) handleReply() (tea.Model, tea.Cmd) {
	if m.chat.mode != modeMessageList {
		m.status = "open a thread first (Enter)"
		return m, nil
	}
	msgItem := m.chat.SelectedMessage()
	if msgItem == nil {
		m.status = "select a message to reply"
		return m, nil
	}
	m.compose.Open(composeModeReply, "Reply to: "+truncate(msgItem.Content, 40), m.compose.height)
	m.compose.state.threadID = msgItem.ThreadID
	m.compose.state.parentID = msgItem.ID
	return m, nil
}

func (m *model) handleComposeSend(msg composeSendMsg) (tea.Model, tea.Cmd) {
	if msg.text == "" {
		m.compose.SetError("message cannot be empty")
		return m, nil
	}
	m.compose.ClearError()

	if msg.mode == composeModeNewThread {
		channelID := m.compose.state.threadID
		if channelID == 0 {
			channelID = m.selectedChannelID
		}
		if channelID == 0 {
			ch := m.tree.SelectedChannel()
			if ch != nil {
				channelID = ch.ID
			}
		}
		if channelID == 0 {
			m.compose.SetError("no channel selected")
			return m, nil
		}
		title := msg.text
		if len(title) > 60 {
			title = title[:60]
		}
		err := func() error {
			_, err := m.api.CreateThread(nil, channelID, title)
			return err
		}()
		if err != nil {
			m.compose.SetError(err.Error())
			return m, nil
		}
		m.compose.Close()
		m.status = fmt.Sprintf("thread %q created", title)
		return m, m.fetchThreads(channelID)
	}

	threadID := m.compose.state.threadID
	parentID := m.compose.state.parentID
	if threadID == 0 {
		th := m.tree.SelectedThread()
		if th != nil {
			threadID = th.ID
		}
	}
	if threadID == 0 {
		th := m.chat.SelectedThread()
		if th != nil {
			threadID = th.ID
		}
	}
	if threadID == 0 {
		threadID = m.selectedThreadID
	}
	if threadID == 0 {
		m.compose.SetError("no thread selected — press n to create one")
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
		treeView := m.tree.View()
		chatView := m.chat.View()
		dimmed := lipgloss.NewStyle().Inline(true).Render(treeView + "\n" + chatView)
		composeView := m.compose.View()
		content = lipgloss.NewStyle().Background(modalOverlay).Render(dimmed) + "\n" + center(composeView, m.compose.width)
	} else {
		treeView := m.tree.View()
		chatView := m.chat.View()
		if m.splitActive() {
			content = lipgloss.JoinHorizontal(lipgloss.Top,
				treeStyle.Width(36).Render(treeView),
				chatStyle.Width(max(20, m.width-38)).Render(chatView),
			)
		} else {
			if m.focus == focusTree {
				content = treeStyle.Width(m.width - 2).Render(treeView)
			} else {
				content = chatStyle.Width(m.width - 2).Render(chatView)
			}
		}
	}
	content += "\n" + shortcutsView(m.focus, m.currentView, m.splitActive())
	statusLine := m.status
	if statusLine != "" {
		content += "\n" + statusStyle.Render(statusLine)
	} else {
		content += "\n" + statusStyle.Render(" flf tui — q quit • Tab switch • Enter open • Esc back ")
	}
	return tea.NewView(content)
}

func shortcutsView(f focusKind, view viewKind, split bool) string {
	_ = f
	_ = view
	_ = split
	var parts []string
	parts = append(parts, hintKeyStyle.Render("q")+" quit")
	if split {
		parts = append(parts, hintKeyStyle.Render("Tab")+" switch")
	}
	parts = append(parts, hintKeyStyle.Render("↑↓/k/j")+" nav")
	parts = append(parts, hintKeyStyle.Render("Enter")+" open")
	parts = append(parts, hintKeyStyle.Render("Esc")+" back")
	parts = append(parts, hintKeyStyle.Render("n")+" new thread")
	parts = append(parts, hintKeyStyle.Render("c")+" post")
	parts = append(parts, hintKeyStyle.Render("r")+" reply")
	return hintStyle.Render(strings.Join(parts, "  "))
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

type focusKind = focus
