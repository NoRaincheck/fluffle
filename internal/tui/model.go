package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type viewKind int

const (
	viewChannels viewKind = iota
	viewThreads
	viewMessages
)

const (
	minContentWidth   = 10
	ellipsisReserve   = 3
	composeTitleLimit = 80
)

type model struct {
	width, height    int
	quitting         bool
	view             viewKind
	cursor           int
	status           string
	api              *apiClient
	channels         []store.Channel
	threads          []store.Thread
	messages         []store.Message
	selectedChannel  *store.Channel
	selectedThread   *store.Thread
	compose          composeModel
	preview          bool
	previewThreads   []store.Thread
	previewMessages  []store.Message
	previewChannelID int64
	previewThreadID  int64
}

func New(base string) tea.Model {
	m := model{
		view: viewChannels,
		api:  NewAPIClient(base),
	}
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
	case channelsFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.channels = msg.channels
		m.cursor = 0
		if len(msg.channels) == 0 {
			m.status = "no channels — n to create (orphaned) or flf channel create --orphaned --name demo"
		} else {
			m.status = fmt.Sprintf("%d channels — ↑↓ nav · Enter open · n new thread · q quit", len(msg.channels))
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
			m.status = fmt.Sprintf("#%s — no threads · n new · Esc back", name)
		} else {
			m.status = fmt.Sprintf("#%s — %d threads · ↑↓ nav · Enter open · n new · Esc back", name, len(msg.threads))
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
		chName := ""
		if m.selectedChannel != nil {
			chName = m.selectedChannel.Name
		}
		if len(msg.messages) == 0 {
			m.status = fmt.Sprintf("#%s › %s — no messages · c post · Esc back", chName, threadTitle)
		} else {
			m.status = fmt.Sprintf("#%s › %s — %d messages · ↑↓ nav · c post · Esc back", chName, threadTitle, len(msg.messages))
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
		m.compose.height = msg.Height/2 - 2
		if m.compose.height < 5 {
			m.compose.height = 5
		}
		if m.compose.height > 12 {
			m.compose.height = 12
		}
		if msg.Width >= 100 && !m.preview {
			m.preview = true
			return m, m.maybeFetchPreview()
		}
		if m.preview {
			return m, m.maybeFetchPreview()
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
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	switch key.Code {
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, m.maybeFetchPreview()
	case tea.KeyDown:
		max := 0
		switch m.view {
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
		case viewMessages:
			return m.handlePost()
		}
	case tea.KeyEscape:
		switch m.view {
		case viewMessages:
			m.view = viewThreads
			m.cursor = 0
			if m.selectedChannel != nil {
				m.status = fmt.Sprintf("#%s — %d threads · Enter open · n new · Esc back", m.selectedChannel.Name, len(m.threads))
			}
			return m, nil
		case viewThreads:
			m.view = viewChannels
			m.cursor = 0
			m.selectedChannel = nil
			m.selectedThread = nil
			m.threads = nil
			m.messages = nil
			m.status = fmt.Sprintf("%d channels — Enter open · q quit", len(m.channels))
			return m, nil
		}
	default:
		if key.Code == 'c' && key.Mod&tea.ModCtrl != 0 {
			m.quitting = true
			return m, tea.Quit
		}
	}
	switch key.Text {
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, m.maybeFetchPreview()
	case "j":
		max := 0
		switch m.view {
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
		return m, m.maybeFetchPreview()
	case "n":
		return m.handleNewThread()
	case "c":
		return m.handlePost()
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

func (m *model) handleNewThread() (tea.Model, tea.Cmd) {
	var ch *store.Channel
	if m.view == viewThreads || m.view == viewMessages {
		ch = m.selectedChannel
	} else if m.view == viewChannels && len(m.channels) > 0 && m.cursor >= 0 && m.cursor < len(m.channels) {
		c := m.channels[m.cursor]
		ch = &c
		m.selectedChannel = ch
	}
	if ch == nil {
		m.status = "select a channel first — ↑↓ then n"
		return m, nil
	}
	m.compose.Open(composeModeNewThread, fmt.Sprintf("New thread in #%s", ch.Name), m.height)
	m.compose.state.threadID = ch.ID
	m.compose.state.parentID = 0
	return m, nil
}

func (m *model) handlePost() (tea.Model, tea.Cmd) {
	if m.view != viewMessages {
		if m.view == viewThreads && len(m.threads) > 0 && m.cursor >= 0 && m.cursor < len(m.threads) {
			th := m.threads[m.cursor]
			m.selectedThread = &th
			m.view = viewMessages
			return m, m.fetchMessages(th.ID)
		}
		m.status = "open a thread first — Enter on thread, or n for new thread"
		return m, nil
	}
	if m.selectedThread == nil {
		m.status = "no thread — Esc back, n new thread"
		return m, nil
	}
	chName := ""
	if m.selectedChannel != nil {
		chName = m.selectedChannel.Name
	}
	m.compose.Open(composeModeMessage, fmt.Sprintf("#%s › %s", chName, m.selectedThread.Title), m.height)
	m.compose.state.threadID = m.selectedThread.ID
	m.compose.state.parentID = 0
	return m, nil
}

func (m *model) handleReply() (tea.Model, tea.Cmd) {
	if m.view != viewMessages {
		return m.handlePost()
	}
	if m.selectedThread == nil {
		m.status = "no thread — Esc back, n new thread"
		return m, nil
	}
	if len(m.messages) > 0 && (m.cursor < 0 || m.cursor >= len(m.messages)) {
		m.cursor = 0
	}
	chName := ""
	if m.selectedChannel != nil {
		chName = m.selectedChannel.Name
	}
	m.compose.Open(composeModeMessage, fmt.Sprintf("Reply in #%s › %s — appends to end", chName, m.selectedThread.Title), m.height)
	m.compose.state.threadID = m.selectedThread.ID
	m.compose.state.parentID = 0
	return m, nil
}

func (m *model) maybeFetchPreview() tea.Cmd {
	if !m.preview || m.width < 80 {
		return nil
	}
	switch m.view {
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
	if msg.mode == composeModeNewThread {
		channelID := m.compose.state.threadID
		if channelID == 0 {
			if m.selectedChannel == nil {
				m.compose.SetError("no channel selected")
				return m, nil
			}
			channelID = m.selectedChannel.ID
		}
		if channelID == 0 {
			m.compose.SetError("no channel selected")
			return m, nil
		}
		title := msg.text
		if len(title) > composeTitleLimit {
			title = title[:composeTitleLimit]
		}
		_, err := m.api.CreateThread(nil, channelID, title)
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
	if threadID == 0 && m.selectedThread != nil {
		threadID = m.selectedThread.ID
	}
	if threadID == 0 {
		m.compose.SetError("no thread — n to create")
		return m, nil
	}
	if err := m.api.SendMessage(nil, threadID, parentID, msg.text); err != nil {
		m.compose.SetError(err.Error())
		return m, nil
	}
	m.compose.Close()
	m.status = "sent"
	return m, m.fetchMessages(threadID)
}

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	if m.compose.IsActive() {
		bg := m.renderList()
		composeView := m.compose.View()
		content := lipgloss.JoinVertical(lipgloss.Left, bg, "", center(composeView, m.width))
		return tea.NewView(content)
	}
	var content string
	if m.preview && m.width >= 80 {
		leftW := m.width/2 - 1
		rightW := m.width - leftW - 3
		if leftW < 20 {
			leftW = 20
		}
		if rightW < 20 {
			rightW = 20
		}
		left := m.renderListWithWidth(leftW)
		right := m.renderPreview(rightW)
		content = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	} else {
		content = m.renderList()
	}
	help := m.helpView()
	status := ""
	if m.status != "" {
		status = statusStyle.Render(m.status)
	} else {
		status = statusStyle.Render(" q quit · ↑↓/j/k nav · Enter open · Esc back · n new thread · c post ")
	}
	full := lipgloss.JoinVertical(lipgloss.Left, content, "", help, status)
	return tea.NewView(full)
}

func (m model) renderList() string {
	return m.renderListWithWidth(m.width)
}

func (m model) renderListWithWidth(w int) string {
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	var title string
	var items []string
	switch m.view {
	case viewChannels:
		last := latestChannelTime(m.channels)
		lastStr := ""
		if last != "" {
			lastStr = fmt.Sprintf("  · last %s", formatTime(last))
		}
		title = "Channels" + lastStr
		if len(m.channels) == 0 {
			items = []string{"  (no channels — press n after selecting, or run: flf channel create --orphaned --name demo)"}
		} else {
			for i, ch := range m.channels {
				prefix := "  "
				if i == m.cursor {
					prefix = "▸ "
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
				items = append(items, line)
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
			items = []string{"  (no threads — press n to create)"}
		} else {
			for i, th := range m.threads {
				prefix := "  "
				if i == m.cursor {
					prefix = "▸ "
				}
				line := prefix + fmt.Sprintf("# %s  · %s", th.Title, formatTime(th.CreatedAt))
				if i == m.cursor {
					line = treeItemSelectedStyle.Render(line)
				} else {
					line = treeItemStyle.Render(line)
				}
				items = append(items, line)
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
			items = []string{"  (no messages — press c to post, r to reply (appends to end))"}
		} else {
			for i, msg := range m.messages {
				prefix := "  "
				if i == m.cursor {
					prefix = "▸ "
				}
				reply := ""
				if msg.ParentID.Valid {
					reply = " ↳ "
				}
				tStr := formatTime(msg.CreatedAt)
				author := truncate(msg.Author, 14)
				content := truncate(msg.Content, max(minContentWidth, w-30))
				line := fmt.Sprintf("%s[%s] %s%s: %s", prefix, tStr, author, reply, content)
				if i == m.cursor {
					line = chatMsgSelectedStyle.Render(line)
				} else {
					line = chatMsgStyle.Render(line)
				}
				items = append(items, line)
			}
		}
	}
	header := chatHeaderStyle.Render(title)
	sepLen := min(max(0, w-4), 60)
	if sepLen < 0 {
		sepLen = 0
	}
	sep := chatHeaderStyle.Render(strings.Repeat("─", sepLen))
	lines := []string{header, sep}
	lines = append(lines, items...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(treeBorder).Padding(0, 1).Width(max(20, w-4)).Render(strings.Join(lines, "\n"))
	return box
}

func (m model) renderPreview(w int) string {
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	var title string
	var items []string
	switch m.view {
	case viewChannels:
		if len(m.channels) == 0 || m.cursor < 0 || m.cursor >= len(m.channels) {
			title = "Preview"
			items = []string{"  (no channel)"}
		} else {
			ch := m.channels[m.cursor]
			title = fmt.Sprintf("Preview: #%s", ch.Name)
			if len(m.previewThreads) == 0 {
				if m.previewChannelID == ch.ID {
					items = []string{"  (no threads — press n in main view)"}
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
			title = "Preview"
			items = []string{"  (no thread)"}
		} else {
			th := m.threads[m.cursor]
			title = fmt.Sprintf("Preview: %s", th.Title)
			if len(m.previewMessages) == 0 {
				if m.previewThreadID == th.ID {
					items = []string{"  (no messages — press c in main view)"}
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
			title = "Preview"
			items = []string{"  (no message)"}
		} else {
			msg := m.messages[m.cursor]
			title = fmt.Sprintf("Preview: %s", truncate(msg.Author, 20))
			full := fmt.Sprintf("  %s", msg.Content)
			wrapped := truncate(full, w-4)
			items = []string{chatMsgStyle.Render(wrapped), "", chatMsgStyle.Render(fmt.Sprintf("  · %s  · %s", formatTime(msg.CreatedAt), msg.Author))}
		}
	}
	header := chatHeaderStyle.Render(title)
	sepLen := min(max(0, w-4), 60)
	if sepLen < 0 {
		sepLen = 0
	}
	sep := chatHeaderStyle.Render(strings.Repeat("─", sepLen))
	lines := []string{header, sep}
	lines = append(lines, items...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(chatBorder).Padding(0, 1).Width(max(20, w-4)).Render(strings.Join(lines, "\n"))
	return box
}

func (m model) helpView() string {
	var parts []string
	previewHint := hintKeyStyle.Render("L") + " preview"
	if m.preview {
		previewHint = hintKeyStyle.Render("L") + " hide preview"
	}
	switch m.view {
	case viewChannels:
		parts = []string{hintKeyStyle.Render("↑↓/j/k") + " nav", hintKeyStyle.Render("Enter") + " open", hintKeyStyle.Render("n") + " new thread", previewHint, hintKeyStyle.Render("q") + " quit"}
	case viewThreads:
		parts = []string{hintKeyStyle.Render("↑↓") + " nav", hintKeyStyle.Render("Enter") + " open", hintKeyStyle.Render("n") + " new thread", hintKeyStyle.Render("Esc") + " back", previewHint, hintKeyStyle.Render("q") + " quit"}
	case viewMessages:
		parts = []string{hintKeyStyle.Render("↑↓") + " nav", hintKeyStyle.Render("c") + " post (appends)", hintKeyStyle.Render("Esc") + " back", previewHint, hintKeyStyle.Render("q") + " quit"}
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
