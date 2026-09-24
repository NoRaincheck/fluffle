package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type viewKind int

const (
	viewInbox viewKind = iota
	viewChannels
	viewThreads
	viewMessages
)

const (
	minContentWidth   = 10
	ellipsisReserve   = 3
	composeTitleLimit = 80
)

type inboxSort int

const (
	inboxSortLatest inboxSort = iota
	inboxSortChannelThread
)

type model struct {
	width, height     int
	quitting          bool
	view              viewKind
	cursor            int
	scroll            int
	status            string
	api               *apiClient
	channels          []store.Channel
	threads           []store.Thread
	messages          []store.Message
	inbox             []store.InboxMessage
	selectedChannel   *store.Channel
	selectedThread    *store.Thread
	compose           composeModel
	filter            filterModel
	inboxSort         inboxSort
	inboxFilterChan   string
	inboxFilterThread string
	preview           bool
	previewThreads    []store.Thread
	previewMessages   []store.Message
	previewChannelID  int64
	previewThreadID   int64
	prevView          viewKind
	hasPrev           bool
}

func New(base string) tea.Model {
	m := model{
		view: viewChannels,
		api:  NewAPIClient(base),
	}
	m.compose = composeModel{width: 60, height: 4}
	return m
}

func (m model) Init() tea.Cmd { return m.fetchInbox() }

type inboxFetchedMsg struct {
	inbox []store.InboxMessage
	err   error
}

func (m *model) fetchInbox() tea.Cmd {
	return func() tea.Msg {
		msgs, err := m.api.ListInbox(nil, 100)
		return inboxFetchedMsg{inbox: msgs, err: err}
	}
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

func (m *model) fetchPreviewThreads(channelID int64) tea.Cmd {
	return func() tea.Msg {
		threads, err := m.api.ListThreads(nil, channelID)
		return previewThreadsFetchedMsg{channelID: channelID, threads: threads, err: err}
	}
}

func (m *model) fetchPreviewMessages(threadID int64) tea.Cmd {
	return func() tea.Msg {
		msgs, err := m.api.ListMessages(nil, threadID)
		return previewMessagesFetchedMsg{threadID: threadID, messages: msgs, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case inboxFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.inbox = msg.inbox
		m.cursor = 0
		m.scroll = 0
		m.view = viewInbox
		filtered := m.inboxFilteredSorted()
		if len(msg.inbox) == 0 {
			m.status = "inbox — no messages · q quit"
		} else if len(filtered) == 0 {
			m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", len(msg.inbox), m.inboxStatusSuffix())
		} else {
			m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
		}
		return m, m.maybeFetchPreview()
	case filterAppliedMsg:
		ch, th := parseInboxFilter(msg.text)
		m.inboxFilterChan = ch
		m.inboxFilterThread = th
		m.cursor = 0
		m.scroll = 0
		filtered := m.inboxFilteredSorted()
		if len(m.inbox) == 0 {
			m.status = "inbox — no messages · q quit"
		} else if len(filtered) == 0 {
			m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", len(m.inbox), m.inboxStatusSuffix())
		} else {
			m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
		}
		return m, m.maybeFetchPreview()
	case channelsFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.channels = msg.channels
		m.cursor = 0
		m.scroll = 0
		if len(msg.channels) == 0 {
			m.status = "no channels — run: flf channel create --orphaned --name demo"
		} else {
			m.status = fmt.Sprintf("%d channels — ↑↓ nav · Enter open · q quit", len(msg.channels))
		}
		if m.preview && len(msg.channels) > 0 {
			return m, m.maybeFetchPreview()
		}
		return m, nil

	case threadsFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.threads = msg.threads
		m.cursor = 0
		m.scroll = 0
		m.view = viewThreads
		name := ""
		for _, ch := range m.channels {
			if ch.ID == msg.channelID {
				c := ch
				m.selectedChannel = &c
				name = ch.Name
				break
			}
		}
		if len(msg.threads) == 0 {
			m.status = fmt.Sprintf("#%s — no threads · Esc back", name)
		} else {
			m.status = fmt.Sprintf("#%s — %d threads · ↑↓ nav · Enter open · Esc back", name, len(msg.threads))
		}
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
		m.messages = msg.messages
		m.cursor = 0
		m.scroll = 0
		m.view = viewMessages
		threadTitle := ""
		for _, th := range m.threads {
			if th.ID == msg.threadID {
				t := th
				m.selectedThread = &t
				threadTitle = th.Title
				break
			}
		}
		if threadTitle == "" && m.selectedThread != nil && m.selectedThread.ID == msg.threadID {
			threadTitle = m.selectedThread.Title
		}
		if threadTitle == "" {
			for _, im := range m.inbox {
				if im.ThreadID == msg.threadID {
					threadTitle = im.ThreadTitle
					break
				}
			}
		}
		chName := ""
		if m.selectedChannel != nil {
			chName = m.selectedChannel.Name
		}
		if len(msg.messages) == 0 {
			m.status = fmt.Sprintf("#%s › %s — no messages · r reply · Esc back", chName, threadTitle)
		} else {
			m.status = fmt.Sprintf("#%s › %s — %d messages · ↑↓ nav · r reply · Esc back", chName, threadTitle, len(msg.messages))
		}
		return m, nil

	case previewThreadsFetchedMsg:
		if msg.err != nil {
			return m, nil
		}
		m.previewThreads = msg.threads
		m.previewChannelID = msg.channelID
		return m, nil

	case previewMessagesFetchedMsg:
		if msg.err != nil {
			return m, nil
		}
		m.previewMessages = msg.messages
		m.previewThreadID = msg.threadID
		return m, nil

	case composeSendMsg:
		return m.handleComposeSend(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.compose.width = max(30, msg.Width-4)
		m.compose.height = 4
		m.filter.width = max(30, msg.Width-4)
		m.filter.height = 4
		if msg.Width >= 100 && !m.preview {
			m.preview = true
			return m, m.maybeFetchPreview()
		}
		if m.preview {
			return m, m.maybeFetchPreview()
		}
		return m, nil

	case tea.KeyMsg:
		if m.filter.IsActive() {
			if msg.String() == "ctrl+c" {
				m.quitting = true
				return m, tea.Quit
			}
			cmd := m.filter.Update(msg)
			return m, cmd
		}
		if m.compose.IsActive() {
			if msg.String() == "ctrl+c" {
				m.quitting = true
				return m, tea.Quit
			}
			cmd := m.compose.Update(msg)
			return m, cmd
		}
		return m.handleKey(msg)
	}

	if m.filter.IsActive() {
		cmd := m.filter.Update(msg)
		return m, cmd
	}
	if m.compose.IsActive() {
		cmd := m.compose.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	}
	switch msg.Type {
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		m.clampCursor()
		return m, m.maybeFetchPreview()
	case tea.KeyDown:
		max := 0
		switch m.view {
		case viewInbox:
			max = len(m.inboxFilteredSorted()) - 1
		case viewChannels:
			max = len(m.channels) - 1
		case viewThreads:
			max = len(m.threads) - 1
		case viewMessages:
			max = len(m.messages) - 1
		}
		if m.cursor < max {
			m.cursor++
		}
		m.clampCursor()
		return m, m.maybeFetchPreview()
	case tea.KeyEnter:
		switch m.view {
		case viewChannels:
			if len(m.channels) == 0 || m.cursor < 0 || m.cursor >= len(m.channels) {
				return m, nil
			}
			ch := m.channels[m.cursor]
			m.selectedChannel = &ch
			return m, m.fetchThreads(ch.ID)
		case viewThreads:
			if len(m.threads) == 0 || m.cursor < 0 || m.cursor >= len(m.threads) {
				return m, nil
			}
			th := m.threads[m.cursor]
			m.selectedThread = &th
			return m, m.fetchMessages(th.ID)
		}
	case tea.KeyEsc:
		switch m.view {
		case viewInbox:
			return m, nil
		case viewMessages:
			if m.compose.IsActive() {
				m.compose.Close()
				return m, nil
			}
			if m.hasPrev && m.prevView == viewInbox {
				m.view = viewInbox
				m.cursor = 0
				m.scroll = 0
				m.hasPrev = false
				filtered := m.inboxFilteredSorted()
				if len(m.inbox) == 0 {
					m.status = "inbox — no messages · q quit"
				} else if len(filtered) == 0 {
					m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", len(m.inbox), m.inboxStatusSuffix())
				} else {
					m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
				}
				return m, m.maybeFetchPreview()
			}
			m.view = viewThreads
			m.cursor = 0
			m.scroll = 0
			m.hasPrev = false
			if m.selectedChannel != nil {
				m.status = fmt.Sprintf("#%s — %d threads · Enter open · Esc back", m.selectedChannel.Name, len(m.threads))
			}
			return m, nil
		case viewThreads:
			if m.hasPrev && m.prevView == viewInbox {
				m.view = viewInbox
				m.cursor = 0
				m.scroll = 0
				m.hasPrev = false
				filtered := m.inboxFilteredSorted()
				if len(m.inbox) == 0 {
					m.status = "inbox — no messages · q quit"
				} else if len(filtered) == 0 {
					m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", len(m.inbox), m.inboxStatusSuffix())
				} else {
					m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
				}
				return m, m.maybeFetchPreview()
			}
			m.view = viewChannels
			m.cursor = 0
			m.scroll = 0
			m.selectedChannel = nil
			m.selectedThread = nil
			m.threads = nil
			m.messages = nil
			m.hasPrev = false
			m.status = fmt.Sprintf("%d channels — Enter open · q quit", len(m.channels))
			return m, nil
		}
	}
	switch msg.String() {
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.clampCursor()
		return m, m.maybeFetchPreview()
	case "j", "down":
		max := 0
		switch m.view {
		case viewInbox:
			max = len(m.inboxFilteredSorted()) - 1
		case viewChannels:
			max = len(m.channels) - 1
		case viewThreads:
			max = len(m.threads) - 1
		case viewMessages:
			max = len(m.messages) - 1
		}
		if m.cursor < max {
			m.cursor++
		}
		m.clampCursor()
		return m, m.maybeFetchPreview()
	case "r":
		return m.handleThreadReply()
	case "v":
		if m.view == viewInbox {
			if m.inboxSort == inboxSortLatest {
				m.inboxSort = inboxSortChannelThread
			} else {
				m.inboxSort = inboxSortLatest
			}
			m.cursor = 0
			m.scroll = 0
			filtered := m.inboxFilteredSorted()
			if len(m.inbox) == 0 {
				m.status = "inbox — no messages · q quit"
			} else if len(filtered) == 0 {
				m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", len(m.inbox), m.inboxStatusSuffix())
			} else {
				m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
			}
			return m, m.maybeFetchPreview()
		}
		return m, nil
	case "f":
		if m.view == viewInbox {
			cur := ""
			if m.inboxFilterChan != "" {
				cur = m.inboxFilterChan
				if m.inboxFilterThread != "" {
					cur += "/" + m.inboxFilterThread
				}
			}
			m.filter.Open(cur, m.width)
			return m, nil
		}
		return m, nil
	case "L":
		m.preview = !m.preview
		if m.preview {
			m.status = "preview on — L to hide"
			return m, m.maybeFetchPreview()
		}
		m.status = "preview off — L to show"
		return m, nil
	case "l":
		m.preview = !m.preview
		if m.preview {
			m.status = "preview on — L to hide"
			return m, m.maybeFetchPreview()
		}
		m.status = "preview off — L to show"
		return m, nil
	}
	return m, nil
}

func (m *model) handleThreadReply() (tea.Model, tea.Cmd) {
	switch m.view {
	case viewInbox:
		filtered := m.inboxFilteredSorted()
		if len(filtered) == 0 || m.cursor < 0 || m.cursor >= len(filtered) {
			if len(m.inbox) == 0 {
				m.status = "no message — inbox empty"
			} else {
				m.status = "no message — filtered"
			}
			return m, nil
		}
		im := filtered[m.cursor]
		channelName := im.ChannelName
		threadTitle := im.ThreadTitle
		threadID := im.ThreadID
		channelID := im.ChannelID
		ch := store.Channel{ID: channelID, Name: channelName}
		for _, c := range m.channels {
			if c.ID == channelID {
				ch = c
				break
			}
		}
		m.selectedChannel = &ch
		m.selectedThread = &store.Thread{ID: threadID, Title: threadTitle, ChannelID: channelID}
		if m.previewThreadID == threadID && len(m.previewMessages) > 0 {
			m.messages = m.previewMessages
		} else {
			m.messages = []store.Message{im.Message}
		}
		m.prevView = m.view
		m.hasPrev = true
		m.view = viewMessages
		m.cursor = 0
		m.scroll = 0
		m.compose.Open(composeModeReply, fmt.Sprintf("Reply in #%s › %s", channelName, threadTitle), m.height)
		m.compose.state.threadID = threadID
		m.compose.state.parentID = 0
		return m, m.fetchMessages(threadID)
	case viewThreads:
		if len(m.threads) == 0 || m.cursor < 0 || m.cursor >= len(m.threads) {
			m.status = "no thread selected"
			return m, nil
		}
		th := m.threads[m.cursor]
		m.selectedThread = &th
		if m.previewThreadID == th.ID && len(m.previewMessages) > 0 {
			m.messages = m.previewMessages
		} else if len(m.messages) == 0 || m.messages[0].ThreadID != th.ID {
			m.messages = nil
		}
		m.prevView = m.view
		m.hasPrev = true
		m.view = viewMessages
		m.cursor = 0
		m.scroll = 0
		chName := ""
		if m.selectedChannel != nil {
			chName = m.selectedChannel.Name
		}
		m.compose.Open(composeModeReply, fmt.Sprintf("Reply in #%s › %s", chName, th.Title), m.height)
		m.compose.state.threadID = th.ID
		m.compose.state.parentID = 0
		return m, m.fetchMessages(th.ID)
	case viewMessages:
		if m.selectedThread == nil {
			m.status = "no thread — Esc back"
			return m, nil
		}
		chName := ""
		if m.selectedChannel != nil {
			chName = m.selectedChannel.Name
		}
		m.compose.Open(composeModeReply, fmt.Sprintf("Reply in #%s › %s", chName, m.selectedThread.Title), m.height)
		m.compose.state.threadID = m.selectedThread.ID
		m.compose.state.parentID = 0
		return m, nil
	default:
		m.status = "open a thread first — Enter on channel, select thread, then r to reply"
		return m, nil
	}
}

func (m *model) maybeFetchPreview() tea.Cmd {
	if !m.preview || m.width < 80 {
		return nil
	}
	switch m.view {
	case viewInbox:
		filtered := m.inboxFilteredSorted()
		if len(filtered) == 0 || m.cursor < 0 || m.cursor >= len(filtered) {
			return nil
		}
		im := filtered[m.cursor]
		if im.ThreadID == m.previewThreadID {
			return nil
		}
		return m.fetchPreviewMessages(im.ThreadID)
	case viewChannels:
		if len(m.channels) == 0 || m.cursor < 0 || m.cursor >= len(m.channels) {
			return nil
		}
		ch := m.channels[m.cursor]
		if ch.ID == m.previewChannelID {
			return nil
		}
		return m.fetchPreviewThreads(ch.ID)
	case viewThreads:
		if len(m.threads) == 0 || m.cursor < 0 || m.cursor >= len(m.threads) {
			return nil
		}
		th := m.threads[m.cursor]
		if th.ID == m.previewThreadID {
			return nil
		}
		return m.fetchPreviewMessages(th.ID)
	}
	return nil
}

func (m *model) handleComposeSend(msg composeSendMsg) (tea.Model, tea.Cmd) {
	if msg.text == "" {
		m.compose.SetError("cannot be empty")
		return m, nil
	}
	m.compose.ClearError()
	threadID := m.compose.state.threadID
	parentID := m.compose.state.parentID
	if threadID == 0 && m.selectedThread != nil {
		threadID = m.selectedThread.ID
	}
	if threadID == 0 {
		m.compose.SetError("no thread selected — press r on a thread to reply")
		return m, nil
	}
	if err := m.api.SendMessage(nil, threadID, parentID, msg.text); err != nil {
		m.compose.SetError(err.Error())
		return m, nil
	}
	m.compose.Close()
	m.status = "sent"
	if m.view == viewInbox {
		return m, m.fetchInbox()
	}
	return m, m.fetchMessages(threadID)
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	if m.filter.IsActive() {
		bg := m.renderList()
		filterView := m.filter.View()
		content := lipgloss.JoinVertical(lipgloss.Left, bg, "", center(filterView, m.width))
		return content
	}
	if m.compose.IsActive() {
		var bg string
		if m.compose.state.mode == composeModeReply && m.compose.state.threadID != 0 {
			bg = m.renderReplyBackground(m.width, m.height-4)
		} else {
			bg = m.renderList()
		}
		composeView := m.compose.View()
		content := lipgloss.JoinVertical(lipgloss.Left, bg, "", center(composeView, m.width))
		return content
	}
	var content string
	if m.preview && m.width >= 80 {
		contentW := m.width - 2
		leftW := contentW / 2
		rightW := contentW - leftW
		if leftW < 20 {
			leftW = 20
			rightW = contentW - leftW
		}
		if rightW < 20 {
			rightW = 20
			leftW = contentW - rightW
		}
		contentH := m.height - 6
		left := m.renderListWithWidth(leftW, contentH)
		right := m.renderPreview(rightW, contentH)
		joined := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
		content = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(treeBorder).Render(joined)
	} else {
		content = m.renderList()
	}
	headerStyle := lipgloss.NewStyle().Foreground(accent).Bold(true).Width(m.width)
	header := headerStyle.Render(" fluffle ")
	help := m.helpView()
	helpStyle := lipgloss.NewStyle().Width(m.width).Render(help)
	status := ""
	if m.status != "" {
		status = statusStyle.Width(m.width).Render(m.status)
	} else {
		status = statusStyle.Width(m.width).Render(" q quit · ↑↓/j/k nav · Enter open · Esc back · r reply ")
	}
	footer := lipgloss.JoinVertical(lipgloss.Left, helpStyle, status)
	full := lipgloss.JoinVertical(lipgloss.Left, header, content, footer)
	return full
}

func (m model) renderReplyBackground(w, h int) string {
	if h < 5 {
		h = 5
	}
	threadID := m.compose.state.threadID
	chName := ""
	if m.selectedChannel != nil {
		chName = m.selectedChannel.Name
	}
	thName := ""
	if m.selectedThread != nil && m.selectedThread.ID == threadID {
		thName = m.selectedThread.Title
	} else {
		for _, th := range m.threads {
			if th.ID == threadID {
				thName = th.Title
				break
			}
		}
		if thName == "" {
			for _, im := range m.inbox {
				if im.ThreadID == threadID {
					thName = im.ThreadTitle
					if chName == "" {
						chName = im.ChannelName
					}
					break
				}
			}
		}
	}
	var msgs []store.Message
	if len(m.messages) > 0 && m.messages[0].ThreadID == threadID {
		msgs = m.messages
	} else if m.previewThreadID == threadID && len(m.previewMessages) > 0 {
		msgs = m.previewMessages
	}
	title := fmt.Sprintf("#%s › %s", chName, thName)
	if title == "# › " {
		title = fmt.Sprintf("Thread #%d", threadID)
	} else {
		last := latestMessageTime(msgs)
		if last != "" {
			title += fmt.Sprintf("  · last %s", formatTime(last))
		}
	}
	var allItems []string
	if len(msgs) == 0 {
		allItems = []string{"  (loading thread…)"}
		if m.selectedThread != nil && threadID != 0 && len(m.messages) == 0 && m.previewThreadID != threadID {
			allItems = []string{"  (loading thread…)"}
		}
	} else {
		timeWidth := 8
		seqWidth := 6
		senderWidth := 15
		contentWidth := max(minContentWidth, w-timeWidth-seqWidth-senderWidth-12)
		for i, msg := range msgs {
			prefix := "  "
			if i == m.cursor {
				prefix = "▸ "
			}
			tStr := formatTime(msg.CreatedAt)
			tStr = fmt.Sprintf("%*s", timeWidth, tStr)
			authorStyle := getAuthorStyle(msg.AuthorType)
			author := truncate(msg.Author, senderWidth)
			authorRendered := authorStyle.Render(author)
			seqStr := fmt.Sprintf("#%-4d", msg.Seq)
			content := truncate(msg.Content, contentWidth)
			line := fmt.Sprintf("%s%s  %s  %s  %s", prefix, tStr, seqStr, authorRendered, content)
			if i == m.cursor {
				line = chatMsgSelectedStyle.Render(line)
			} else {
				line = chatMsgStyle.Render(line)
			}
			allItems = append(allItems, line)
		}
	}
	visibleCap := h - 1
	if visibleCap < 1 {
		visibleCap = 1
	}
	start := m.scroll
	if start < 0 {
		start = 0
	}
	if start >= len(allItems) {
		start = len(allItems) - 1
	}
	if start < 0 {
		start = 0
	}
	visible := allItems[start:]
	if len(visible) > visibleCap {
		visible = visible[:visibleCap]
	}
	boxW := max(20, w)
	sepLen := max(0, boxW-2)
	sep := chatHeaderStyle.Render(strings.Repeat("─", sepLen))
	lines := []string{chatHeaderStyle.Render(title), sep}
	lines = append(lines, visible...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lipgloss.NewStyle().Width(boxW).Render(strings.Join(lines, "\n"))
}

func (m model) renderList() string {
	return m.renderListWithWidth(m.width, m.height-4)
}

func (m model) renderListWithWidth(w, h int) string {
	if h < 5 {
		h = 5
	}
	var title string
	var allItems []string
	switch m.view {
	case viewInbox:
		return m.renderInboxWithWidth(w, h)
	case viewChannels:
		last := latestChannelTime(m.channels)
		lastStr := ""
		if last != "" {
			lastStr = fmt.Sprintf("  · last %s", formatTime(last))
		}
		title = "Channels" + lastStr
		if len(m.channels) == 0 {
			allItems = []string{"  (no channels — run: flf channel create --orphaned --name demo)"}
		} else {
			for i, ch := range m.channels {
				prefix := "  "
				if i == m.cursor {
					prefix = "▸ "
				}
				label := fmt.Sprintf("#%-4d %s", ch.ID, ch.Name)
				if ch.IsOrphaned {
					label += "  (orphaned)"
				} else {
					if ch.RepoHeadBranch != "" {
						label += fmt.Sprintf("  [%s]", ch.RepoHeadBranch)
					}
					if ch.RepoAbsPath != "" {
						short := ch.RepoAbsPath
						if idx := strings.LastIndex(short, "/"); idx >= 0 {
							short = short[idx+1:]
						}
						label += fmt.Sprintf("  %s", short)
					}
				}
				label += fmt.Sprintf("  · %s", formatTime(ch.CreatedAt))
				line := prefix + label
				if i == m.cursor {
					line = treeItemSelectedStyle.Render(line)
				} else {
					line = treeItemStyle.Render(line)
				}
				allItems = append(allItems, line)
			}
		}
	case viewThreads:
		chName := ""
		if m.selectedChannel != nil {
			chName = m.selectedChannel.Name
		}
		last := latestThreadTime(m.threads)
		lastStr := ""
		if last != "" {
			lastStr = fmt.Sprintf("  · last %s", formatTime(last))
		}
		title = fmt.Sprintf("Threads in #%s%s", chName, lastStr)
		if len(m.threads) == 0 {
			allItems = []string{"  (no threads)"}
		} else {
			for i, th := range m.threads {
				prefix := "  "
				if i == m.cursor {
					prefix = "▸ "
				}
				line := prefix + fmt.Sprintf("#%-4d %s  · %s", th.ID, th.Title, formatTime(th.CreatedAt))
				if i == m.cursor {
					line = treeItemSelectedStyle.Render(line)
				} else {
					line = treeItemStyle.Render(line)
				}
				allItems = append(allItems, line)
			}
		}
	case viewMessages:
		chName := ""
		if m.selectedChannel != nil {
			chName = m.selectedChannel.Name
		}
		thName := ""
		if m.selectedThread != nil {
			thName = m.selectedThread.Title
		}
		last := latestMessageTime(m.messages)
		lastStr := ""
		if last != "" {
			lastStr = fmt.Sprintf("  · last %s", formatTime(last))
		}
		title = fmt.Sprintf("#%s › %s%s", chName, thName, lastStr)
		if len(m.messages) == 0 {
			allItems = []string{"  (no messages — press r to reply)"}
		} else {
			timeWidth := 8
			seqWidth := 6
			senderWidth := 15
			contentWidth := max(minContentWidth, w-timeWidth-seqWidth-senderWidth-12)

			for i, msg := range m.messages {
				prefix := "  "
				if i == m.cursor {
					prefix = "▸ "
				}

				tStr := formatTime(msg.CreatedAt)
				tStr = fmt.Sprintf("%*s", timeWidth, tStr)
				authorStyle := getAuthorStyle(msg.AuthorType)
				author := truncate(msg.Author, senderWidth)
				authorRendered := authorStyle.Render(author)

				seqStr := fmt.Sprintf("#%-4d", msg.Seq)
				content := truncate(msg.Content, contentWidth)
				line := fmt.Sprintf("%s%s  %s  %s  %s", prefix, tStr, seqStr, authorRendered, content)

				if i == m.cursor {
					line = chatMsgSelectedStyle.Render(line)
				} else {
					line = chatMsgStyle.Render(line)
				}
				allItems = append(allItems, line)
			}
		}
	}
	visibleCap := h - 1
	if visibleCap < 1 {
		visibleCap = 1
	}
	start := m.scroll
	if start < 0 {
		start = 0
	}
	if start >= len(allItems) {
		start = len(allItems) - 1
	}
	if start < 0 {
		start = 0
	}
	visible := allItems[start:]
	if len(visible) > visibleCap {
		visible = visible[:visibleCap]
	}
	boxW := max(20, w)
	sepLen := max(0, boxW-2)
	sep := chatHeaderStyle.Render(strings.Repeat("─", sepLen))
	lines := []string{chatHeaderStyle.Render(title), sep}
	lines = append(lines, visible...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lipgloss.NewStyle().Width(boxW).Render(strings.Join(lines, "\n"))
}

func (m model) renderInboxWithWidth(w, h int) string {
	if h < 5 {
		h = 5
	}
	filtered := m.inboxFilteredSorted()
	title := fmt.Sprintf("Inbox — %d messages", len(filtered))
	if len(m.inbox) > 0 && len(filtered) != len(m.inbox) {
		title = fmt.Sprintf("Inbox — %d/%d messages", len(filtered), len(m.inbox))
	}
	if m.inboxFilterChan != "" {
		f := m.inboxFilterChan
		if m.inboxFilterThread != "" {
			f += "/" + m.inboxFilterThread
		}
		title += fmt.Sprintf(" · filter:%s", f)
	}
	title += fmt.Sprintf(" · sort:%s", inboxSortName(m.inboxSort))
	if len(filtered) == 0 {
		boxW := max(20, w)
		sep := chatHeaderStyle.Render(strings.Repeat("─", max(0, boxW-2)))
		empty := "  (no messages)"
		if len(m.inbox) > 0 {
			empty = "  (no messages — filtered, press f to clear)"
		}
		lines := []string{chatHeaderStyle.Render(title), sep, empty}
		for len(lines) < h {
			lines = append(lines, "")
		}
		if len(lines) > h {
			lines = lines[:h]
		}
		return lipgloss.NewStyle().Width(max(20, w)).Render(strings.Join(lines, "\n"))
	}
	timeW := 8
	chanW := 12
	threadW := 16
	senderW := 12
	contentW := max(minContentWidth, w-timeW-chanW-threadW-senderW-14)
	visibleCap := h - 2
	if visibleCap < 1 {
		visibleCap = 1
	}
	start := m.scroll
	if start < 0 {
		start = 0
	}
	if start >= len(filtered) {
		start = len(filtered) - 1
	}
	if start < 0 {
		start = 0
	}
	if start+visibleCap > len(filtered) {
		visibleCap = len(filtered) - start
	}
	visibleInbox := filtered[start : start+visibleCap]
	headerRow := fmt.Sprintf("  %*s  %-12s  %-16s  %-12s  %s", timeW, "TIME", "CHANNEL", "THREAD", "SENDER", "CONTENT")
	headerRow = lipgloss.NewStyle().Foreground(chatHeaderFg).Bold(true).Render(headerRow)
	boxW := max(20, w)
	sep2 := lipgloss.NewStyle().Foreground(chatHeaderFg).Render(strings.Repeat("─", max(0, boxW-2)))
	var rows []string
	for i, im := range visibleInbox {
		globalIdx := start + i
		prefix := "  "
		if globalIdx == m.cursor {
			prefix = "▸ "
		}
		tStr := fmt.Sprintf("%*s", timeW, formatTime(im.CreatedAt))
		chanS := truncate(fmt.Sprintf("#%d %s", im.ChannelID, im.ChannelName), chanW)
		thrS := truncate(fmt.Sprintf("#%d %s", im.ThreadID, im.ThreadTitle), threadW)
		author := truncate(im.Author, senderW)
		content := truncate(strings.ReplaceAll(im.Content, "\n", " "), contentW)
		line := fmt.Sprintf("%s%s  %-12s  %-16s  %-12s  %s", prefix, tStr, chanS, thrS, author, content)
		if globalIdx == m.cursor {
			line = chatMsgSelectedStyle.Render(line)
		} else {
			authorStyle := getAuthorStyle(im.AuthorType)
			_ = authorStyle
			line = chatMsgStyle.Render(line)
		}
		rows = append(rows, line)
	}
	lines := []string{chatHeaderStyle.Render(title), headerRow, sep2}
	lines = append(lines, rows...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lipgloss.NewStyle().Width(boxW).Render(strings.Join(lines, "\n"))
}

func (m model) renderPreview(w, h int) string {
	if h < 5 {
		h = 5
	}
	var title string
	var items []string
	switch m.view {
	case viewInbox:
		filtered := m.inboxFilteredSorted()
		if len(filtered) == 0 || m.cursor < 0 || m.cursor >= len(filtered) {
			title = "[PREVIEW]"
			items = []string{"  (no message)"}
		} else {
			im := filtered[m.cursor]
			title = fmt.Sprintf("[PREVIEW THREAD #%d]\n#%s › %s", im.ThreadID, im.ChannelName, truncate(im.ThreadTitle, 30))
			hPreview := m.height - 4
			if hPreview < 5 {
				hPreview = 5
			}
			hAvail := hPreview - 4
			Y := hAvail - 5*2 - 2
			if Y < 3 {
				Y = 3
			}
			if Y > 20 {
				Y = 20
			}
			split := strings.Split(im.Content, "\n")
			display := split
			remaining := 0
			truncated := false
			if len(split) > Y {
				display = split[:Y]
				remaining = len(split) - Y
				truncated = true
			}
			for _, l := range display {
				items = append(items, chatMsgStyle.Render("  "+truncate(l, max(minContentWidth, w-4))))
			}
			if truncated {
				items = append(items, chatMsgStyle.Render(fmt.Sprintf("  ... (+%d lines)", remaining)))
			}
			items = append(items, chatMsgStyle.Render(strings.Repeat("─", min(w-4, 40))))
			replies := lastNPreviewMessagesWithParent(m.previewMessages, im.ThreadID, im.Seq, im.ID, 5)
			if len(replies) == 0 {
				items = append(items, chatMsgStyle.Render("  (no replies)"))
			} else {
				for _, r := range replies {
					line := fmt.Sprintf("  [%s] %s: %s", formatTime(r.CreatedAt), truncate(r.Author, 12), truncate(r.Content, max(minContentWidth, w-20)))
					items = append(items, chatMsgStyle.Render(line))
				}
			}
		}
	case viewChannels:
		if len(m.channels) == 0 || m.cursor < 0 || m.cursor >= len(m.channels) {
			title = "[PREVIEW]"
			items = []string{"  (no channel)"}
		} else {
			ch := m.channels[m.cursor]
			title = fmt.Sprintf("Preview: #%s", ch.Name)
			if len(m.previewThreads) == 0 {
				if m.previewChannelID == ch.ID {
					items = []string{"  (no threads)"}
				} else {
					items = []string{"  (loading…)"}
				}
			} else {
				for _, th := range m.previewThreads {
					line := fmt.Sprintf("  # %s  · %s", th.Title, formatTime(th.CreatedAt))
					items = append(items, chatMsgStyle.Render(truncate(line, w-6)))
				}
			}
		}
	case viewThreads:
		if len(m.threads) == 0 || m.cursor < 0 || m.cursor >= len(m.threads) {
			title = "[PREVIEW]"
			items = []string{"  (no thread)"}
		} else {
			th := m.threads[m.cursor]
			title = fmt.Sprintf("[PREVIEW THREAD #%d] %s", th.ID, truncate(th.Title, 40))
			if len(m.previewMessages) == 0 {
				if m.previewThreadID == th.ID {
					items = []string{"  (no messages — press r to reply)"}
				} else {
					items = []string{"  (loading…)"}
				}
			} else {
				for _, msg := range m.previewMessages {
					tStr := formatTime(msg.CreatedAt)
					author := truncate(msg.Author, 12)
					content := truncate(msg.Content, max(minContentWidth, w-20))
					line := fmt.Sprintf("  [%s] %s: %s", tStr, author, content)
					items = append(items, chatMsgStyle.Render(line))
				}
			}
		}
	case viewMessages:
		if len(m.messages) == 0 || m.cursor < 0 || m.cursor >= len(m.messages) {
			title = "[PREVIEW]"
			items = []string{"  (no message)"}
		} else {
			msg := m.messages[m.cursor]
			title = fmt.Sprintf("[PREVIEW MESSAGE #%d] %s", msg.ID, truncate(msg.Author, 30))
			full := fmt.Sprintf("  %s", msg.Content)
			wrapped := truncate(full, w-4)
			items = []string{chatMsgStyle.Render(wrapped), "", chatMsgStyle.Render(fmt.Sprintf("  · %s  · %s", formatTime(msg.CreatedAt), msg.Author))}
		}
	}
	header := chatHeaderStyle.Render(title)
	previewHeaderStyle := chatHeaderStyle.MarginBottom(0)

	sep := previewHeaderStyle.Render(strings.Repeat("─", w))
	lines := []string{header, sep}
	lines = append(lines, items...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lipgloss.NewStyle().Width(max(20, w)).Render(strings.Join(lines, "\n"))
}

func lastNPreviewMessages(msgs []store.Message, threadID int64, seq int64, n int) []store.Message {
	return lastNPreviewMessagesWithParent(msgs, threadID, seq, 0, n)
}

func lastNPreviewMessagesWithParent(msgs []store.Message, threadID int64, seq int64, parentID int64, n int) []store.Message {
	var filtered []store.Message
	if parentID != 0 {
		for _, msg := range msgs {
			if msg.ThreadID != threadID {
				continue
			}
			if msg.ParentID.Valid && msg.ParentID.Int64 == parentID {
				filtered = append(filtered, msg)
			}
		}
		if len(filtered) > 0 {
			if len(filtered) <= n {
				return filtered
			}
			return filtered[len(filtered)-n:]
		}
	}
	for _, msg := range msgs {
		if msg.ThreadID != threadID {
			continue
		}
		if seq != 0 && msg.Seq <= seq {
			continue
		}
		filtered = append(filtered, msg)
	}
	if len(filtered) == 0 {
		for _, msg := range msgs {
			if msg.ThreadID == threadID {
				filtered = append(filtered, msg)
			}
		}
		if len(filtered) == 0 {
			return nil
		}
	}
	if len(filtered) <= n {
		return filtered
	}
	return filtered[len(filtered)-n:]
}

func (m *model) clampCursor() {
	total := 0
	switch m.view {
	case viewInbox:
		total = len(m.inboxFilteredSorted())
	case viewChannels:
		total = len(m.channels)
	case viewThreads:
		total = len(m.threads)
	case viewMessages:
		total = len(m.messages)
	}
	if total == 0 {
		m.cursor = 0
		m.scroll = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= total {
		m.cursor = total - 1
	}
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	visible := h - 1
	if m.view == viewInbox {
		visible = h - 2
	}
	if visible < 1 {
		visible = 1
	}
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+visible {
		m.scroll = m.cursor - visible + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.scroll+visible > total {
		m.scroll = total - visible
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func parseInboxFilter(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	if idx := strings.Index(text, "/"); idx >= 0 {
		ch := strings.TrimSpace(text[:idx])
		th := strings.TrimSpace(text[idx+1:])
		return ch, th
	}
	return text, ""
}

func inboxSortName(s inboxSort) string {
	switch s {
	case inboxSortChannelThread:
		return "channel/thread"
	default:
		return "latest"
	}
}

func (m *model) inboxFilteredSorted() []store.InboxMessage {
	if len(m.inbox) == 0 {
		return nil
	}
	filtered := make([]store.InboxMessage, 0, len(m.inbox))
	for _, im := range m.inbox {
		if m.inboxFilterChan != "" && !strings.Contains(strings.ToLower(im.ChannelName), strings.ToLower(m.inboxFilterChan)) {
			continue
		}
		if m.inboxFilterThread != "" && !strings.Contains(strings.ToLower(im.ThreadTitle), strings.ToLower(m.inboxFilterThread)) {
			continue
		}
		filtered = append(filtered, im)
	}
	switch m.inboxSort {
	case inboxSortChannelThread:
		sort.SliceStable(filtered, func(i, j int) bool {
			a, b := filtered[i], filtered[j]
			ca := strings.ToLower(a.ChannelName)
			cb := strings.ToLower(b.ChannelName)
			if ca != cb {
				return ca < cb
			}
			ta := strings.ToLower(a.ThreadTitle)
			tb := strings.ToLower(b.ThreadTitle)
			if ta != tb {
				return ta < tb
			}
			taTime, errA := time.Parse(time.RFC3339, a.CreatedAt)
			tbTime, errB := time.Parse(time.RFC3339, b.CreatedAt)
			if errA == nil && errB == nil {
				if !taTime.Equal(tbTime) {
					return taTime.Before(tbTime)
				}
			} else if a.CreatedAt != b.CreatedAt {
				return a.CreatedAt < b.CreatedAt
			}
			return a.ID < b.ID
		})
	default:
		sort.SliceStable(filtered, func(i, j int) bool {
			a, b := filtered[i], filtered[j]
			ta, errA := time.Parse(time.RFC3339, a.CreatedAt)
			tb, errB := time.Parse(time.RFC3339, b.CreatedAt)
			if errA == nil && errB == nil {
				if !ta.Equal(tb) {
					return ta.Before(tb)
				}
			} else if a.CreatedAt != b.CreatedAt {
				return a.CreatedAt < b.CreatedAt
			}
			return a.ID < b.ID
		})
	}
	return filtered
}

func (m *model) inboxStatusSuffix() string {
	parts := []string{fmt.Sprintf("sort:%s", inboxSortName(m.inboxSort))}
	if m.inboxFilterChan != "" {
		f := m.inboxFilterChan
		if m.inboxFilterThread != "" {
			f += "/" + m.inboxFilterThread
		}
		parts = append(parts, fmt.Sprintf("filter:%s", f))
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

func (m model) helpView() string {
	var parts []string
	previewHint := hintKeyStyle.Render("L") + " preview"
	if m.preview {
		previewHint = hintKeyStyle.Render("L") + " hide preview"
	}
	switch m.view {
	case viewInbox:
		parts = []string{hintKeyStyle.Render("↑↓/j/k") + " nav", hintKeyStyle.Render("r") + " reply", hintKeyStyle.Render("v") + " sort", hintKeyStyle.Render("f") + " filter", previewHint, hintKeyStyle.Render("q") + " quit"}
	case viewChannels:
		parts = []string{hintKeyStyle.Render("↑↓/j/k") + " nav", hintKeyStyle.Render("Enter") + " open", previewHint, hintKeyStyle.Render("q") + " quit"}
	case viewThreads:
		parts = []string{hintKeyStyle.Render("↑↓") + " nav", hintKeyStyle.Render("Enter") + " open", hintKeyStyle.Render("r") + " reply", hintKeyStyle.Render("Esc") + " back", previewHint, hintKeyStyle.Render("q") + " quit"}
	case viewMessages:
		parts = []string{hintKeyStyle.Render("↑↓") + " nav", hintKeyStyle.Render("r") + " reply", hintKeyStyle.Render("Esc") + " back", previewHint, hintKeyStyle.Render("q") + " quit"}
	}
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= ellipsisReserve {
		return s[:maxLen]
	}
	return s[:maxLen-ellipsisReserve] + "..."
}

func formatTime(t string) string {
	if t == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return t
	}
	now := time.Now()
	diff := now.Sub(parsed)
	if diff < time.Minute {
		return "now"
	}
	if diff < time.Hour {
		return fmt.Sprintf("%dm", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%dh", int(diff.Hours()))
	}
	return parsed.Format("01/02")
}

func latestChannelTime(channels []store.Channel) string {
	var latest time.Time
	var out string
	for _, ch := range channels {
		if t, err := time.Parse(time.RFC3339, ch.CreatedAt); err == nil {
			if t.After(latest) {
				latest = t
				out = ch.CreatedAt
			}
		}
	}
	return out
}

func latestThreadTime(threads []store.Thread) string {
	var latest time.Time
	var out string
	for _, th := range threads {
		if t, err := time.Parse(time.RFC3339, th.CreatedAt); err == nil {
			if t.After(latest) {
				latest = t
				out = th.CreatedAt
			}
		}
	}
	return out
}

func latestMessageTime(msgs []store.Message) string {
	var latest time.Time
	var out string
	for _, m := range msgs {
		if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			if t.After(latest) {
				latest = t
				out = m.CreatedAt
			}
		}
	}
	return out
}
