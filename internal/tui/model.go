package tui

import (
	"fmt"
	"regexp"
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
	viewInboxDetail
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
	inboxSortLatestDesc inboxSort = iota
	inboxSortChannelThreadDesc
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
	detailThreadID    int64
	detailScroll      int
	detailCursor      int
	savedInboxCursor  int
	savedInboxScroll  int
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
			m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", totalGroups(msg.inbox), m.inboxStatusSuffix())
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
			m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", m.inboxTotalGroups(), m.inboxStatusSuffix())
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
			m.status = fmt.Sprintf("%s — no threads · Esc back", name)
		} else {
			m.status = fmt.Sprintf("%s — %d threads · ↑↓ nav · Enter open · Esc back", name, len(msg.threads))
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
		if m.view == viewInboxDetail && m.detailThreadID == msg.threadID {
			m.detailCursor = 0
			m.detailScroll = 0
		} else {
			m.cursor = 0
			m.scroll = 0
			m.view = viewMessages
		}
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
			m.status = fmt.Sprintf("%s › %s — no messages · r reply · Esc back", chName, threadTitle)
		} else {
			m.status = fmt.Sprintf("%s › %s — %d messages · ↑↓ nav · r reply · Esc back", chName, threadTitle, len(msg.messages))
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
		m.compose.width = max(30, msg.Width*80/100)
		m.compose.height = 4
		m.filter.width = max(30, msg.Width*80/100)
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
		if m.view == viewInboxDetail {
			if m.detailCursor > 0 {
				m.detailCursor--
			}
			m.clampDetailScroll()
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
		}
		m.clampCursor()
		return m, m.maybeFetchPreview()
	case tea.KeyDown:
		if m.view == viewInboxDetail {
			sorted := sortedMessagesAsc(m.messages)
			_, replies := threadSplit(sorted)
			max := len(replies) - 1
			if max < 0 {
				max = 0
			}
			if m.detailCursor < max {
				m.detailCursor++
			}
			m.clampDetailScroll()
			return m, nil
		}
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
		case viewInbox:
			filtered := m.inboxFilteredSorted()
			if len(filtered) == 0 || m.cursor < 0 || m.cursor >= len(filtered) {
				return m, nil
			}
			im := filtered[m.cursor]
			m.prevView = m.view
			m.hasPrev = true
			m.savedInboxCursor = m.cursor
			m.savedInboxScroll = m.scroll
			m.detailThreadID = im.ThreadID
			ch := store.Channel{ID: im.ChannelID, Name: im.ChannelName}
			for _, c := range m.channels {
				if c.ID == im.ChannelID {
					ch = c
					break
				}
			}
			m.selectedChannel = &ch
			m.selectedThread = &store.Thread{ID: im.ThreadID, Title: im.ThreadTitle, ChannelID: im.ChannelID}
			m.view = viewInboxDetail
			m.detailCursor = 0
			m.detailScroll = 0
			m.scroll = 0
			if m.previewThreadID == im.ThreadID && len(m.previewMessages) > 0 {
				m.messages = m.previewMessages
			}
			m.status = fmt.Sprintf("%s › %s · %d messages · ↑↓ scroll · r reply · Esc back", im.ChannelName, im.ThreadTitle, len(m.messages))
			return m, m.fetchMessages(im.ThreadID)
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
		case viewInboxDetail:
			m.view = viewInbox
			m.hasPrev = false
			m.detailThreadID = 0
			m.detailScroll = 0
			m.detailCursor = 0
			m.cursor = m.savedInboxCursor
			m.scroll = m.savedInboxScroll
			m.clampCursor()
			filtered := m.inboxFilteredSorted()
			if len(m.inbox) == 0 {
				m.status = "inbox — no messages · q quit"
			} else if len(filtered) == 0 {
				m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", m.inboxTotalGroups(), m.inboxStatusSuffix())
			} else {
				m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
			}
			return m, m.maybeFetchPreview()
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
				m.previewThreadID = 0
				filtered := m.inboxFilteredSorted()
				if len(m.inbox) == 0 {
					m.status = "inbox — no messages · q quit"
				} else if len(filtered) == 0 {
					m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", m.inboxTotalGroups(), m.inboxStatusSuffix())
				} else {
					m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
				}
				return m, tea.Batch(m.fetchInbox(), m.maybeFetchPreview())
			}
			m.view = viewThreads
			m.cursor = 0
			m.scroll = 0
			m.hasPrev = false
			if m.selectedChannel != nil {
				m.status = fmt.Sprintf("%s — %d threads · Enter open · Esc back", m.selectedChannel.Name, len(m.threads))
			}
			return m, nil
		case viewThreads:
			if m.hasPrev && m.prevView == viewInbox {
				m.view = viewInbox
				m.cursor = 0
				m.scroll = 0
				m.hasPrev = false
				m.previewThreadID = 0
				filtered := m.inboxFilteredSorted()
				if len(m.inbox) == 0 {
					m.status = "inbox — no messages · q quit"
				} else if len(filtered) == 0 {
					m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", m.inboxTotalGroups(), m.inboxStatusSuffix())
				} else {
					m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
				}
				return m, tea.Batch(m.fetchInbox(), m.maybeFetchPreview())
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
		if m.view == viewInboxDetail {
			if m.detailCursor > 0 {
				m.detailCursor--
			}
			m.clampDetailScroll()
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
		}
		m.clampCursor()
		return m, m.maybeFetchPreview()
	case "j", "down":
		if m.view == viewInboxDetail {
			sorted := sortedMessagesAsc(m.messages)
			_, replies := threadSplit(sorted)
			max := len(replies) - 1
			if max < 0 {
				max = 0
			}
			if m.detailCursor < max {
				m.detailCursor++
			}
			m.clampDetailScroll()
			return m, nil
		}
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
			if m.inboxSort == inboxSortLatestDesc {
				m.inboxSort = inboxSortChannelThreadDesc
			} else {
				m.inboxSort = inboxSortLatestDesc
			}
			m.cursor = 0
			m.scroll = 0
			filtered := m.inboxFilteredSorted()
			if len(m.inbox) == 0 {
				m.status = "inbox — no messages · q quit"
			} else if len(filtered) == 0 {
				m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", m.inboxTotalGroups(), m.inboxStatusSuffix())
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
	case "g":
		if m.view == viewInbox {
			m.cursor = 0
			m.scroll = 0
			m.clampCursor()
			return m, m.maybeFetchPreview()
		}
		if m.view == viewInboxDetail {
			m.detailCursor = 0
			m.detailScroll = 0
			m.clampDetailScroll()
			return m, nil
		}
		return m, nil
	case "G":
		if m.view == viewInbox {
			filtered := m.inboxFilteredSorted()
			if len(filtered) > 0 {
				m.cursor = len(filtered) - 1
			} else {
				m.cursor = 0
			}
			m.clampCursor()
			return m, m.maybeFetchPreview()
		}
		if m.view == viewInboxDetail {
			sorted := sortedMessagesAsc(m.messages)
			_, replies := threadSplit(sorted)
			if len(replies) > 0 {
				m.detailCursor = len(replies) - 1
			} else {
				m.detailCursor = 0
			}
			m.clampDetailScroll()
			if len(replies) > 0 {
				_, total, _, _ := m.threadReplyLineCounts(m.width)
				h := m.height - 4
				visible := h - threadHeaderOverhead(m.width, m.messages)
				if visible < 1 {
					visible = 1
				}
				if total > visible {
					m.detailScroll = total - visible
				}
			}
			return m, nil
		}
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
		m.view = viewInboxDetail
		m.detailThreadID = threadID
		m.detailCursor = 0
		m.detailScroll = 0
		m.scroll = 0
		m.compose.Open(composeModeReply, fmt.Sprintf("Reply in %s › %s", channelName, threadTitle), m.height)
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
		m.compose.Open(composeModeReply, fmt.Sprintf("Reply in %s › %s", chName, th.Title), m.height)
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
		m.compose.Open(composeModeReply, fmt.Sprintf("Reply in %s › %s", chName, m.selectedThread.Title), m.height)
		m.compose.state.threadID = m.selectedThread.ID
		m.compose.state.parentID = 0
		return m, nil
	case viewInboxDetail:
		if m.selectedThread == nil {
			m.status = "no thread — Esc back"
			return m, nil
		}
		chName := ""
		if m.selectedChannel != nil {
			chName = m.selectedChannel.Name
		}
		m.compose.Open(composeModeReply, fmt.Sprintf("Reply in %s › %s", chName, m.selectedThread.Title), m.height)
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
	if m.hasPrev && m.prevView == viewInbox {
		m.previewThreadID = 0
		return m, tea.Batch(m.fetchMessages(threadID), m.fetchInbox())
	}
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
		base := m.baseView()
		filterView := m.filter.View()
		return placeOverlay(base, filterView, m.width, m.height)
	}
	if m.compose.IsActive() {
		base := m.baseView()
		composeView := m.compose.View()
		return placeOverlay(base, composeView, m.width, m.height)
	}
	return m.baseView()
}

func (m model) baseView() string {
	if m.view == viewInboxDetail {
		contentW := m.width - 2
		if contentW < 20 {
			contentW = 20
		}
		contentH := m.height - 6
		if contentH < 5 {
			contentH = 5
		}
		inner := m.renderInboxDetail(contentW, contentH)
		content := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(treeBorder).Render(inner)
		headerStyle := lipgloss.NewStyle().Foreground(accent).Bold(true).Width(m.width)
		header := headerStyle.Render(" fluffle ")
		help := m.helpView()
		helpStyle := lipgloss.NewStyle().Width(m.width).Render(help)
		status := statusStyle.Width(m.width).Render(m.status)
		footer := lipgloss.JoinVertical(lipgloss.Left, helpStyle, status)
		full := lipgloss.JoinVertical(lipgloss.Left, header, content, footer)
		return full
	}
	var content string
	if m.preview && m.width >= 80 && m.view != viewInboxDetail {
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
		contentW := m.width - 2
		if contentW < 20 {
			contentW = 20
		}
		contentH := m.height - 6
		if contentH < 5 {
			contentH = 5
		}
		inner := m.renderListWithWidth(contentW, contentH)
		content = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(treeBorder).Render(inner)
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

var ansiRegexp = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripAnsi(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

func placeOverlay(base, overlay string, width, height int) string {
	if width <= 0 {
		width = lipgloss.Width(base)
	}
	if height <= 0 {
		height = lipgloss.Height(base)
	}
	overlayW := lipgloss.Width(overlay)
	overlayH := lipgloss.Height(overlay)
	if overlayW >= width {
		overlayW = width - 2
	}
	if overlayH >= height {
		overlayH = height - 2
	}
	x0 := (width - overlayW) / 2
	if x0 < 0 {
		x0 = 0
	}
	baseLines := strings.Split(base, "\n")
	y0 := (len(baseLines) - overlayH) / 2
	if y0 < 0 {
		y0 = 0
	}
	if height > len(baseLines) {
		alt := (height - overlayH) / 2
		if alt < y0 {
			y0 = alt
		}
		if y0 < 0 {
			y0 = 0
		}
	}
	overlayLines := strings.Split(overlay, "\n")
	innerWidth := width - 2
	innerX0 := (innerWidth - overlayW) / 2
	if innerX0 < 0 {
		innerX0 = 0
	}
	for i, ol := range overlayLines {
		y := y0 + i
		if y < 0 || y >= len(baseLines) {
			continue
		}
		stripped := stripAnsi(baseLines[y])
		isBoxLine := false
		var leftChar, rightChar string
		if len(stripped) > 0 {
			trimmed := strings.TrimSpace(stripped)
			if strings.HasPrefix(trimmed, "╭") || strings.HasPrefix(trimmed, "│") || strings.HasPrefix(trimmed, "╰") {
				if lipgloss.Width(baseLines[y]) == width {
					isBoxLine = true
					runes := []rune(trimmed)
					if len(runes) > 0 {
						leftChar = string(runes[0])
						rightChar = string(runes[len(runes)-1])
					}
				}
			}
		}
		if isBoxLine && y > 0 && y < len(baseLines)-3 {
			if leftChar == "" {
				leftChar = "│"
			}
			if rightChar == "" {
				rightChar = "│"
			}
			leftBorder := lipgloss.NewStyle().Foreground(treeBorder).Render(leftChar)
			rightBorder := lipgloss.NewStyle().Foreground(treeBorder).Render(rightChar)
			line := leftBorder + strings.Repeat(" ", innerX0) + ol + strings.Repeat(" ", innerWidth-innerX0-overlayW) + rightBorder
			baseLines[y] = line
		} else {
			line := strings.Repeat(" ", x0) + ol
			baseLines[y] = line
		}
	}
	return strings.Join(baseLines, "\n")
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
	title := fmt.Sprintf("%s › %s", chName, thName)
	if title == " › " {
		title = "Thread"
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
		nameWidth := 15
		contentWidth := max(minContentWidth, w-timeWidth-seqWidth-nameWidth-12)
		for i, msg := range msgs {
			prefix := "  "
			if i == m.cursor {
				prefix = "> "
			}
			tStr := formatTime(msg.CreatedAt)
			tStr = fmt.Sprintf("%*s", timeWidth, tStr)
			nameStyle := getNameStyle(msg.AuthorType)
			name := truncate(msg.Name, nameWidth)
			nameRendered := nameStyle.Render(name)
			seqStr := fmt.Sprintf("#%-4d", msg.Seq)
			content := truncate(msg.Content, contentWidth)
			line := fmt.Sprintf("%s%s  %s  %s  %s", prefix, tStr, seqStr, nameRendered, content)
			if i == m.cursor {
				line = chatMsgSelectedStyle.Render(line)
			} else {
				line = chatMsgStyle.Render(line)
			}
			allItems = append(allItems, line)
		}
	}
	visibleCap := h - 2
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
	headerStyleNoMargin := chatHeaderStyle.MarginBottom(0)
	sep := headerStyleNoMargin.Render(strings.Repeat("─", sepLen))
	lines := []string{headerStyleNoMargin.Render(title), sep}
	lines = append(lines, visible...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lipgloss.NewStyle().Width(boxW).Render(strings.Join(lines, "\n"))
}

func sortedMessagesDesc(msgs []store.Message) []store.Message {
	if msgs == nil {
		return nil
	}
	out := make([]store.Message, len(msgs))
	copy(out, msgs)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ta, errA := time.Parse(time.RFC3339, a.CreatedAt)
		tb, errB := time.Parse(time.RFC3339, b.CreatedAt)
		if errA == nil && errB == nil {
			if !ta.Equal(tb) {
				return ta.After(tb)
			}
		} else if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt > b.CreatedAt
		}
		return a.ID > b.ID
	})
	return out
}

func sortedMessagesAsc(msgs []store.Message) []store.Message {
	if msgs == nil {
		return nil
	}
	out := make([]store.Message, len(msgs))
	copy(out, msgs)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
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
	return out
}

func threadSplit(sortedAsc []store.Message) (*store.Message, []store.Message) {
	if len(sortedAsc) == 0 {
		return nil, nil
	}
	op := sortedAsc[0]
	if len(sortedAsc) == 1 {
		return &op, nil
	}
	replies := make([]store.Message, len(sortedAsc)-1)
	copy(replies, sortedAsc[1:])
	return &op, replies
}

func threadTitle(chName, thName string, msgs []store.Message) string {
	title := fmt.Sprintf("%s › %s", chName, thName)
	if title == " › " {
		title = "Thread"
	}
	if last := latestMessageTime(msgs); last != "" {
		title += fmt.Sprintf("  · last %s", formatTime(last))
	}
	if len(msgs) > 0 {
		n := len(msgs) - 1
		title += fmt.Sprintf("  · %d repl", n)
		if n == 1 {
			title += "y"
		} else {
			title += "ies"
		}
	}
	return title
}

const threadNumW = 4
const threadTimeW = 12
const threadNameW = 10

func threadReplyMsgWidth(w int, showRowNum bool) int {
	if showRowNum {
		return max(minContentWidth, w-threadNumW-threadTimeW-threadNameW-10)
	}
	return max(minContentWidth, w-threadTimeW-threadNameW-8)
}

func buildThreadOPlines(w int, op *store.Message) []string {
	if op == nil {
		return []string{"  (loading…)"}
	}
	headerPlain := fmt.Sprintf("Original Post #%d · %s · %s", op.Seq, formatTime(op.CreatedAt), op.Name)
	headerLine := threadOpHeaderStyle.Render(" " + truncate(headerPlain, max(0, w-2)) + " ")
	contentWidth := max(minContentWidth, w-4)
	wrapped := wrapText(strings.ReplaceAll(op.Content, "\n", " "), contentWidth)
	var lines []string
	lines = append(lines, headerLine)
	for _, wl := range wrapped {
		if wl == "" {
			lines = append(lines, chatMsgStyle.Render("  "))
		} else {
			lines = append(lines, chatMsgStyle.Render("  "+wl))
		}
	}
	return lines
}

func buildThreadReplyLines(w int, replies []store.Message, showRowNum bool, cursor int) []string {
	if len(replies) == 0 {
		return nil
	}
	msgW := threadReplyMsgWidth(w, showRowNum)
	var out []string
	for i, msg := range replies {
		tPlain := truncate(formatTime(msg.CreatedAt), threadTimeW)
		tPadded := fmt.Sprintf("%-*s", threadTimeW, tPlain)
		namePlain := formatFixedName(msg.Name, threadNameW)
		wrapped := wrapText(strings.ReplaceAll(msg.Content, "\n", " "), msgW)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for j, wl := range wrapped {
			var line string
			if showRowNum {
				if j == 0 {
					numStr := fmt.Sprintf("%*d", threadNumW, i+1)
					if i == cursor {
						prefix := ">"
						line = fmt.Sprintf("%s%s  %s  %s  %s", prefix, numStr, tPadded, namePlain, wl)
						line = chatMsgSelectedStyle.Render(line)
					} else {
						numRendered := threadRowNumStyle.Render(numStr)
						nameRendered := getNameStyle(msg.AuthorType).Render(namePlain)
						line = fmt.Sprintf(" %s  %s  %s  %s", numRendered, tPadded, nameRendered, wl)
						line = chatMsgStyle.Render(line)
					}
				} else {
					indent := strings.Repeat(" ", 1+threadNumW+2+threadTimeW+2+threadNameW+2)
					line = indent + wl
					if i == cursor {
						line = chatMsgSelectedStyle.Render(line)
					} else {
						line = chatMsgStyle.Render(line)
					}
				}
			} else {
				if j == 0 {
					nameRendered := getNameStyle(msg.AuthorType).Render(namePlain)
					line = fmt.Sprintf("  %s  %s  %s", tPadded, nameRendered, wl)
					line = chatMsgStyle.Render(line)
				} else {
					indent := strings.Repeat(" ", 2+threadTimeW+2+threadNameW+2)
					line = indent + wl
					line = chatMsgStyle.Render(line)
				}
			}
			out = append(out, line)
		}
	}
	return out
}

func (m model) renderThreadView(w, h int, msgs []store.Message, chName, thName string, preview bool) string {
	if h < 5 {
		h = 5
	}
	sorted := sortedMessagesAsc(msgs)
	op, replies := threadSplit(sorted)
	title := threadTitle(chName, thName, sorted)
	titleRendered := threadTitleStyle.Render(truncate(title, w))
	sep := threadSepStyle.Render(strings.Repeat("─", max(0, w-2)))
	opLines := buildThreadOPlines(w, op)
	repliesHeaderRaw := ""
	if preview {
		repliesHeaderRaw = fmt.Sprintf("  %-*s  %-*s  %s", threadTimeW, "TIME", threadNameW, "NAME", "MESSAGE")
	} else {
		repliesHeaderRaw = fmt.Sprintf("  %*s  %-*s  %-*s  %s", threadNumW, "#", threadTimeW, "TIME", threadNameW, "NAME", "MESSAGE")
	}
	repliesHeaderRaw = truncate(repliesHeaderRaw, w)
	repliesHeader := threadRepliesHeaderStyle.Render(repliesHeaderRaw)
	repliesSep := threadSepStyle.Render(strings.Repeat("─", max(0, w-2)))

	var replyLines []string
	if op == nil {
		replyLines = nil
	} else if len(replies) == 0 {
		replyLines = []string{chatMsgStyle.Render("  (no replies — press r to reply)")}
	} else {
		cursor := -1
		if !preview {
			cursor = m.detailCursor
		}
		replyLines = buildThreadReplyLines(w, replies, !preview, cursor)
	}

	headerCount := 1 + 1 + len(opLines) + 1 + 1
	if op == nil {
		headerCount = 1 + 1 + len(opLines)
	} else {
		headerCount = 1 + 1 + len(opLines) + 1 + 1
	}
	visibleCap := h - headerCount
	if visibleCap < 1 {
		visibleCap = 1
	}

	var visibleReplies []string
	if preview {
		if len(replyLines) <= visibleCap {
			visibleReplies = replyLines
		} else {
			hidden := len(replyLines) - visibleCap + 1
			if hidden < 1 {
				hidden = 1
			}
			head := (visibleCap - 1) / 2
			tail := visibleCap - 1 - head
			if head < 0 {
				head = 0
			}
			if tail < 0 {
				tail = 0
			}
			var truncated []string
			if head > 0 && head <= len(replyLines) {
				truncated = append(truncated, replyLines[:head]...)
			}
			midLine := threadSepStyle.Render(fmt.Sprintf("  … (%d hidden) …", hidden))
			truncated = append(truncated, midLine)
			if tail > 0 && len(replyLines)-tail >= 0 {
				truncated = append(truncated, replyLines[len(replyLines)-tail:]...)
			}
			visibleReplies = truncated
		}
	} else {
		total := len(replyLines)
		start := m.detailScroll
		if start < 0 {
			start = 0
		}
		if start >= total && total > 0 {
			start = max(0, total-visibleCap)
		}
		end := start + visibleCap
		if end > total {
			end = total
		}
		if total == 0 {
			visibleReplies = replyLines
		} else {
			visibleReplies = replyLines[start:end]
		}
	}

	boxW := max(20, w)
	var lines []string
	lines = append(lines, titleRendered, sep)
	lines = append(lines, opLines...)
	if op != nil {
		lines = append(lines, repliesHeader, repliesSep)
		lines = append(lines, visibleReplies...)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lipgloss.NewStyle().Width(boxW).Render(strings.Join(lines, "\n"))
}

func (m model) renderInboxDetail(w, h int) string {
	chName := ""
	if m.selectedChannel != nil {
		chName = m.selectedChannel.Name
	}
	thName := ""
	if m.selectedThread != nil {
		thName = m.selectedThread.Title
	}
	return m.renderThreadView(w, h, m.messages, chName, thName, false)
}

func (m model) renderList() string {
	return m.renderListWithWidth(m.width, m.height-4)
}

func (m model) listHeight() int {
	if m.preview && m.width >= 80 {
		h := m.height - 6
		if h < 5 {
			h = 5
		}
		return h
	}
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	return h
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
					prefix = "> "
				}
				label := ch.Name
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
		title = fmt.Sprintf("Threads in %s%s", chName, lastStr)
		if len(m.threads) == 0 {
			allItems = []string{"  (no threads)"}
		} else {
			for i, th := range m.threads {
				prefix := "  "
				if i == m.cursor {
					prefix = "> "
				}
				line := prefix + fmt.Sprintf("%s  · %s", th.Title, formatTime(th.CreatedAt))
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
		title = fmt.Sprintf("%s › %s%s", chName, thName, lastStr)
		if len(m.messages) == 0 {
			allItems = []string{"  (no messages — press r to reply)"}
		} else {
			timeWidth := 8
			seqWidth := 6
			nameWidth := 15
			contentWidth := max(minContentWidth, w-timeWidth-seqWidth-nameWidth-12)

			for i, msg := range m.messages {
				prefix := "  "
				if i == m.cursor {
					prefix = "> "
				}

				tStr := formatTime(msg.CreatedAt)
				tStr = fmt.Sprintf("%*s", timeWidth, tStr)
				nameStyle := getNameStyle(msg.AuthorType)
				name := truncate(msg.Name, nameWidth)
				nameRendered := nameStyle.Render(name)

				seqStr := fmt.Sprintf("#%-4d", msg.Seq)
				content := truncate(msg.Content, contentWidth)
				line := fmt.Sprintf("%s%s  %s  %s  %s", prefix, tStr, seqStr, nameRendered, content)

				if i == m.cursor {
					line = chatMsgSelectedStyle.Render(line)
				} else {
					line = chatMsgStyle.Render(line)
				}
				allItems = append(allItems, line)
			}
		}
	}
	visibleCap := h - 2
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
	headerStyleNoMargin := chatHeaderStyle.MarginBottom(0)
	sep := headerStyleNoMargin.Render(strings.Repeat("─", sepLen))
	lines := []string{headerStyleNoMargin.Render(title), sep}
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
	groupedAll, counts := groupInboxByChannelThread(m.inbox)
	totalGroups := len(groupedAll)
	title := fmt.Sprintf("Inbox — %d messages", len(filtered))
	if totalGroups > 0 && len(filtered) != totalGroups {
		title = fmt.Sprintf("Inbox — %d/%d messages", len(filtered), totalGroups)
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
		headerStyleNoMargin := chatHeaderStyle.MarginBottom(0)
		sep := headerStyleNoMargin.Render(strings.Repeat("─", max(0, boxW-2)))
		empty := "  (no messages)"
		if len(m.inbox) > 0 {
			empty = "  (no messages — filtered, press f to clear)"
		}
		lines := []string{headerStyleNoMargin.Render(title), sep, empty}
		for len(lines) < h {
			lines = append(lines, "")
		}
		if len(lines) > h {
			lines = lines[:h]
		}
		return lipgloss.NewStyle().Width(max(20, w)).Render(strings.Join(lines, "\n"))
	}
	timeW := 11
	chanW := 12
	threadW := 16
	nameW := 12
	visibleCap := h - 3
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
	var maxID int64
	for _, im := range visibleInbox {
		if im.ID > maxID {
			maxID = im.ID
		}
	}
	idW := len(fmt.Sprintf("%d", maxID))
	if idW < 1 {
		idW = 1
	}
	contentW := max(minContentWidth, w-idW-timeW-chanW-threadW-nameW-14)
	headerRowRaw := fmt.Sprintf("  %0*d %*s  %-12s  %-16s  %-12s  %s", idW, maxID, timeW, "TIME", "CHANNEL", "THREAD", "NAME", "CONTENT")
	headerRowRaw = truncate(headerRowRaw, w)
	headerRow := lipgloss.NewStyle().Foreground(chatHeaderFg).Bold(true).Render(headerRowRaw)
	boxW := max(20, w)
	sep2 := lipgloss.NewStyle().Foreground(chatHeaderFg).Render(strings.Repeat("─", max(0, boxW-2)))
	var rows []string
	for i, im := range visibleInbox {
		globalIdx := start + i
		prefix := "  "
		if globalIdx == m.cursor {
			prefix = "> "
		}
		tStr := fmt.Sprintf("%*s", timeW, formatTime(im.CreatedAt))
		chanS := truncate(im.ChannelName, chanW)
		thrS := truncate(im.ThreadTitle, threadW)
		name := truncate(im.Name, nameW)
		base := strings.ReplaceAll(im.Content, "\n", " ")
		more := ""
		if cnt := counts[inboxKey(im)]; cnt > 1 {
			more = fmt.Sprintf(" (%d+)", cnt-1)
		}
		moreW := len(more)
		baseAvail := contentW - moreW
		if more != "" && baseAvail < 1 {
			baseAvail = 1
		}
		if baseAvail < 0 {
			baseAvail = 0
		}
		baseTrunc := truncate(base, baseAvail)
		if more == "" {
			baseTrunc = truncate(base, contentW)
		}
		var contentRendered string
		if more != "" {
			contentRendered = baseTrunc + inboxMoreStyle.Render(more)
		} else {
			contentRendered = baseTrunc
		}
		contentPlainLen := len(baseTrunc) + moreW
		if contentPlainLen > contentW {
			contentPlainLen = contentW
		}
		lineRendered := fmt.Sprintf("%s%0*d %s  %-12s  %-16s  %-12s  %s", prefix, idW, im.ID, tStr, chanS, thrS, name, contentRendered)
		plainLen := 2 + idW + 1 + timeW + 2 + chanW + 2 + threadW + 2 + nameW + 2 + contentPlainLen
		if plainLen > w {
			excess := plainLen - w
			if excess < len(baseTrunc) {
				baseTrunc = truncate(baseTrunc, max(0, len(baseTrunc)-excess))
				if more != "" {
					contentRendered = baseTrunc + inboxMoreStyle.Render(more)
				} else {
					contentRendered = baseTrunc
				}
				lineRendered = fmt.Sprintf("%s%0*d %s  %-12s  %-16s  %-12s  %s", prefix, idW, im.ID, tStr, chanS, thrS, name, contentRendered)
			} else {
				lineRendered = truncate(lineRendered, w)
			}
		}
		var line string
		coloredName := getNameStyle(im.AuthorType).Render(name)
		lineRendered = strings.Replace(lineRendered, name, coloredName, 1)
		if globalIdx == m.cursor {
			line = chatMsgSelectedStyle.Width(w).Render(lineRendered)
		} else {
			line = chatMsgStyle.Render(lineRendered)
		}
		rows = append(rows, line)
	}
	lines := []string{chatHeaderStyle.MarginBottom(0).Render(title), headerRow, sep2}
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
			var msgs []store.Message
			if m.previewThreadID == im.ThreadID && len(m.previewMessages) > 0 {
				ids := make(map[int64]bool)
				for _, pm := range m.previewMessages {
					ids[pm.ID] = true
				}
				msgs = make([]store.Message, 0, len(m.previewMessages)+1)
				msgs = append(msgs, m.previewMessages...)
				if !ids[im.ID] {
					msgs = append(msgs, im.Message)
				}
				msgs = sortedMessagesAsc(msgs)
			} else if len(m.previewMessages) == 0 && m.previewThreadID != im.ThreadID {
				msgs = []store.Message{im.Message}
			} else if len(m.previewMessages) == 0 {
				msgs = []store.Message{im.Message}
			} else {
				msgs = m.previewMessages
				if len(msgs) == 0 {
					msgs = []store.Message{im.Message}
				}
			}
			return m.renderThreadView(w, h, msgs, im.ChannelName, im.ThreadTitle, true)
		}
	case viewChannels:
		if len(m.channels) == 0 || m.cursor < 0 || m.cursor >= len(m.channels) {
			title = "[PREVIEW]"
			items = []string{"  (no channel)"}
		} else {
			ch := m.channels[m.cursor]
			title = fmt.Sprintf("Preview: %s", ch.Name)
			if len(m.previewThreads) == 0 {
				if m.previewChannelID == ch.ID {
					items = []string{"  (no threads)"}
				} else {
					items = []string{"  (loading…)"}
				}
			} else {
				for _, th := range m.previewThreads {
					line := fmt.Sprintf("  %s  · %s", th.Title, formatTime(th.CreatedAt))
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
			chName := ""
			if m.selectedChannel != nil {
				chName = m.selectedChannel.Name
			}
			msgs := m.previewMessages
			if len(msgs) == 0 && m.previewThreadID != th.ID {
				msgs = nil
			}
			return m.renderThreadView(w, h, msgs, chName, th.Title, true)
		}
	case viewMessages:
		if len(m.messages) == 0 || m.cursor < 0 || m.cursor >= len(m.messages) {
			title = "[PREVIEW]"
			items = []string{"  (no message)"}
		} else {
			msg := m.messages[m.cursor]
			title = fmt.Sprintf("[PREVIEW MESSAGE #%d] %s", msg.ID, truncate(msg.Name, 30))
			full := fmt.Sprintf("  %s", msg.Content)
			wrapped := truncate(full, w-4)
			items = []string{chatMsgStyle.Render(wrapped), "", chatMsgStyle.Render(fmt.Sprintf("  · %s  · %s", formatTime(msg.CreatedAt), msg.Name))}
		}
	}
	headerStyleNoMargin := chatHeaderStyle.MarginBottom(0)
	headerRendered := headerStyleNoMargin.Render(title)
	headerLines := strings.Split(headerRendered, "\n")
	sep := headerStyleNoMargin.Render(strings.Repeat("─", w))
	lines := append([]string{}, headerLines...)
	lines = append(lines, sep)
	if len(items) > 0 {
		if len(lines)+len(items) > h && m.view == viewInbox {
			dashIdx := -1
			for i, it := range items {
				if strings.Contains(it, "─") {
					dashIdx = i
					break
				}
			}
			if dashIdx >= 0 {
				keep := h - len(lines) - dashIdx - 1
				if keep < 0 {
					keep = 0
				}
				if keep < len(items)-dashIdx-1 {
					tail := items[len(items)-keep:]
					items = append(items[:dashIdx+1], tail...)
				}
			}
		}
		lines = append(lines, items...)
	}
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

func filterThreadReplies(msgs []store.Message, threadID int64, seq int64, parentID int64) []store.Message {
	var filtered []store.Message
	for _, msg := range msgs {
		if msg.ThreadID != threadID {
			continue
		}
		if msg.ParentID.Valid && msg.ParentID.Int64 == parentID {
			filtered = append(filtered, msg)
		}
	}
	if len(filtered) > 0 {
		return filtered
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
	return filtered
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
	case viewInboxDetail:
		return
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
	h := m.listHeight()
	visible := h - 2
	if m.view == viewInbox {
		visible = h - 3
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

func (m model) detailLineCounts(w int) ([]int, int, int, []store.Message) {
	return m.threadReplyLineCounts(w)
}

func (m model) threadReplyLineCounts(w int) ([]int, int, int, []store.Message) {
	sorted := sortedMessagesAsc(m.messages)
	_, replies := threadSplit(sorted)
	msgW := threadReplyMsgWidth(w, true)
	if len(replies) == 0 {
		return nil, 0, msgW, replies
	}
	counts := make([]int, len(replies))
	total := 0
	for i, msg := range replies {
		wrapped := wrapText(strings.ReplaceAll(msg.Content, "\n", " "), msgW)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		counts[i] = len(wrapped)
		total += counts[i]
	}
	return counts, total, msgW, replies
}

func threadHeaderOverhead(w int, msgs []store.Message) int {
	sorted := sortedMessagesAsc(msgs)
	op, _ := threadSplit(sorted)
	opLines := buildThreadOPlines(w, op)
	if op == nil {
		return 2 + len(opLines)
	}
	return 4 + len(opLines)
}

func (m *model) clampDetailScroll() {
	sorted := sortedMessagesAsc(m.messages)
	_, replies := threadSplit(sorted)
	if len(replies) == 0 {
		m.detailScroll = 0
		if m.detailCursor < 0 {
			m.detailCursor = 0
		}
		if m.detailCursor > 0 {
			m.detailCursor = 0
		}
		return
	}
	if m.detailCursor < 0 {
		m.detailCursor = 0
	}
	if m.detailCursor >= len(replies) {
		m.detailCursor = len(replies) - 1
	}
	counts, total, _, _ := m.threadReplyLineCounts(m.width)
	lineStart := 0
	for i := 0; i < m.detailCursor && i < len(counts); i++ {
		lineStart += counts[i]
	}
	cursorCount := 1
	if m.detailCursor >= 0 && m.detailCursor < len(counts) {
		cursorCount = counts[m.detailCursor]
	}
	h := m.height - 4
	visible := h - threadHeaderOverhead(m.width, m.messages)
	if visible < 1 {
		visible = 1
	}
	if lineStart < m.detailScroll {
		m.detailScroll = lineStart
	}
	if lineStart+cursorCount > m.detailScroll+visible {
		m.detailScroll = lineStart + cursorCount - visible
	}
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
	if total > 0 && m.detailScroll+visible > total {
		m.detailScroll = total - visible
		if m.detailScroll < 0 {
			m.detailScroll = 0
		}
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
	case inboxSortLatestDesc:
		return "latest ↓"
	case inboxSortChannelThreadDesc:
		return "channel A→Z, thread A→Z, time ↓"
	}
	return ""
}

func inboxKey(im store.InboxMessage) string {
	if im.ChannelID != 0 && im.ThreadID != 0 {
		return fmt.Sprintf("%d:%d", im.ChannelID, im.ThreadID)
	}
	return fmt.Sprintf("%s:%s", im.ChannelName, im.ThreadTitle)
}

func inboxIsLater(a, b store.InboxMessage) bool {
	ta, errA := time.Parse(time.RFC3339, a.CreatedAt)
	tb, errB := time.Parse(time.RFC3339, b.CreatedAt)
	if errA == nil && errB == nil {
		if !ta.Equal(tb) {
			return ta.After(tb)
		}
	} else if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID > b.ID
}

func groupInboxByChannelThread(msgs []store.InboxMessage) ([]store.InboxMessage, map[string]int) {
	if len(msgs) == 0 {
		return nil, nil
	}
	groups := make(map[string]store.InboxMessage)
	counts := make(map[string]int)
	for _, im := range msgs {
		k := inboxKey(im)
		counts[k]++
		if cur, ok := groups[k]; !ok {
			groups[k] = im
		} else if inboxIsLater(im, cur) {
			groups[k] = im
		}
	}
	out := make([]store.InboxMessage, 0, len(groups))
	for _, v := range groups {
		out = append(out, v)
	}
	return out, counts
}

func totalGroups(msgs []store.InboxMessage) int {
	g, _ := groupInboxByChannelThread(msgs)
	return len(g)
}

func (m *model) inboxTotalGroups() int { return totalGroups(m.inbox) }

func (m *model) inboxFilteredSorted() []store.InboxMessage {
	if len(m.inbox) == 0 {
		return nil
	}
	grouped, _ := groupInboxByChannelThread(m.inbox)
	filtered := make([]store.InboxMessage, 0, len(grouped))
	for _, im := range grouped {
		if m.inboxFilterChan != "" && !strings.Contains(strings.ToLower(im.ChannelName), strings.ToLower(m.inboxFilterChan)) {
			continue
		}
		if m.inboxFilterThread != "" && !strings.Contains(strings.ToLower(im.ThreadTitle), strings.ToLower(m.inboxFilterThread)) {
			continue
		}
		filtered = append(filtered, im)
	}
	switch m.inboxSort {
	case inboxSortLatestDesc:
		sort.SliceStable(filtered, func(i, j int) bool {
			a, b := filtered[i], filtered[j]
			taTime, errA := time.Parse(time.RFC3339, a.CreatedAt)
			tbTime, errB := time.Parse(time.RFC3339, b.CreatedAt)
			if errA == nil && errB == nil {
				if !taTime.Equal(tbTime) {
					return taTime.After(tbTime)
				}
			} else if a.CreatedAt != b.CreatedAt {
				return a.CreatedAt > b.CreatedAt
			}
			return a.ID > b.ID
		})
	case inboxSortChannelThreadDesc:
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
					return taTime.After(tbTime)
				}
			} else if a.CreatedAt != b.CreatedAt {
				return a.CreatedAt > b.CreatedAt
			}
			return a.ID > b.ID
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
		parts = []string{hintKeyStyle.Render("↑↓/j/k") + " nav", hintKeyStyle.Render("Enter") + " view", hintKeyStyle.Render("r") + " reply", hintKeyStyle.Render("v") + " sort", hintKeyStyle.Render("f") + " filter", previewHint, hintKeyStyle.Render("q") + " quit"}
	case viewInboxDetail:
		parts = []string{hintKeyStyle.Render("↑↓/j/k") + " scroll", hintKeyStyle.Render("g/G") + " top/bottom", hintKeyStyle.Render("r") + " reply", hintKeyStyle.Render("Esc") + " back", previewHint, hintKeyStyle.Render("q") + " quit"}
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

func formatFixedName(s string, w int) string {
	rs := []rune(s)
	if len(rs) < w {
		return s + strings.Repeat(" ", w-len(rs))
	}
	if len(rs) == w {
		return s
	}
	if w <= 2 {
		return string(rs[:w])
	}
	return string(rs[:w-2]) + ".."
}

func wrapText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := words[0]
		if len(cur) > width {
			for len(cur) > width {
				out = append(out, cur[:width])
				cur = cur[width:]
			}
		}
		for _, w := range words[1:] {
			if len(w) > width {
				if cur != "" {
					out = append(out, cur)
					cur = ""
				}
				for len(w) > width {
					out = append(out, w[:width])
					w = w[width:]
				}
				cur = w
				continue
			}
			if cur == "" {
				cur = w
			} else if len(cur)+1+len(w) <= width {
				cur += " " + w
			} else {
				out = append(out, cur)
				cur = w
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

func formatTime(t string) string {
	if t == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return t
	}
	return parsed.Format("Jan 02 15:04")
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
