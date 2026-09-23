package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type treeSort int

const (
	sortLastUpdate treeSort = iota
	sortAlphaName
	sortAlphaRepo
)

type treeKind int

const (
	kindRepo treeKind = iota
	kindChannel
	kindThread
)

type treeItem struct {
	label     string
	depth     int
	kind      treeKind
	expanded  bool
	channel   *store.Channel
	thread    *store.Thread
	channelID int64
}

type treeModel struct {
	items            []treeItem
	cursor           int
	width            int
	height           int
	filter           string
	sortBy           treeSort
	channels         []store.Channel
	threadsByChannel map[int64][]store.Thread
	channelExpanded  map[int64]bool
}

func newTreeModel() treeModel {
	return treeModel{
		sortBy:           sortLastUpdate,
		threadsByChannel: make(map[int64][]store.Thread),
		channelExpanded:  make(map[int64]bool),
	}
}

func (m treeModel) Init() tea.Cmd { return nil }

func (m *treeModel) SetChannels(channels []store.Channel) {
	m.channels = channels
	m.rebuild()
}

func (m *treeModel) SetThreads(channelID int64, threads []store.Thread) {
	if threads == nil {
		threads = []store.Thread{}
	}
	m.threadsByChannel[channelID] = threads
	m.channelExpanded[channelID] = true
	m.rebuild()
}

func (m *treeModel) rebuild() {
	m.items = buildTree(m.channels, m.threadsByChannel, m.channelExpanded)
	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *treeModel) SetFilter(f string) { m.filter = f }
func (m *treeModel) ToggleSort()        { m.sortBy = (m.sortBy + 1) % 3 }
func (m *treeModel) SetSize(w, h int)   { m.width = w; m.height = h }

func (m *treeModel) Update(msg tea.Msg) tea.Cmd {
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
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case tea.KeyHome:
			m.cursor = 0
		case tea.KeyEnd:
			m.cursor = len(m.items) - 1
		}
	}
	return nil
}

func (m treeModel) View() string {
	if len(m.items) == 0 {
		return lipgloss.NewStyle().Padding(0, 1).Render("no channels — press n to create")
	}
	lines := make([]string, 0, m.height)
	for i, item := range m.items {
		if i >= m.height {
			break
		}
		prefix := "  "
		if i == m.cursor {
			prefix = "▸ "
		}
		indent := strings.Repeat("  ", item.depth)
		label := indent + prefix + item.label
		var line string
		if i == m.cursor {
			line = treeItemSelectedStyle.Render(label)
		} else {
			line = treeItemStyle.Render(label)
		}
		lines = append(lines, line)
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m treeModel) SelectedChannel() *store.Channel {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	item := m.items[m.cursor]
	if item.kind == kindChannel && item.channel != nil {
		return item.channel
	}
	if item.kind == kindThread {
		for _, ch := range m.channels {
			if ch.ID == item.channelID {
				c := ch
				return &c
			}
		}
	}
	return nil
}

func (m treeModel) SelectedThread() *store.Thread {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	item := m.items[m.cursor]
	if item.kind == kindThread && item.thread != nil {
		return item.thread
	}
	return nil
}

func buildTree(channels []store.Channel, threadsByChannel map[int64][]store.Thread, expanded map[int64]bool) []treeItem {
	repoMap := make(map[string][]store.Channel)
	var repoOrder []string
	for _, ch := range channels {
		repo := ch.RepoAbsPath
		if repo == "" {
			repo = "__orphaned__"
		}
		if _, exists := repoMap[repo]; !exists {
			repoOrder = append(repoOrder, repo)
		}
		repoMap[repo] = append(repoMap[repo], ch)
	}
	items := make([]treeItem, 0)
	if orphans, ok := repoMap["__orphaned__"]; ok {
		items = append(items, treeItem{label: "🗂 Orphaned", depth: 0, kind: kindRepo, expanded: true})
		for _, ch := range orphans {
			items = append(items, channelItems(ch, 1, threadsByChannel, expanded)...)
		}
	}
	for _, repo := range repoOrder {
		if repo == "__orphaned__" {
			continue
		}
		channels := repoMap[repo]
		branchMap := make(map[string][]store.Channel)
		var branchOrder []string
		for _, ch := range channels {
			branch := ch.RepoHeadBranch
			if branch == "" {
				branch = "main"
			}
			if _, exists := branchMap[branch]; !exists {
				branchOrder = append(branchOrder, branch)
			}
			branchMap[branch] = append(branchMap[branch], ch)
		}
		repoLabel := repo
		if idx := strings.LastIndex(repo, "/"); idx >= 0 {
			repoLabel = repo[idx+1:]
		}
		for _, branch := range branchOrder {
			items = append(items, treeItem{label: fmt.Sprintf("📂 %s (%s)", repoLabel, branch), depth: 0, kind: kindRepo, expanded: true})
			for _, ch := range branchMap[branch] {
				items = append(items, channelItems(ch, 1, threadsByChannel, expanded)...)
			}
		}
	}
	return items
}

func channelItems(ch store.Channel, depth int, threadsByChannel map[int64][]store.Thread, expanded map[int64]bool) []treeItem {
	chCopy := ch
	isExpanded := expanded[ch.ID]
	icon := "🗨"
	if isExpanded && len(threadsByChannel[ch.ID]) > 0 {
		icon = "🗨▾"
	} else if len(threadsByChannel[ch.ID]) > 0 {
		icon = "🗨▸"
	}
	items := []treeItem{{label: fmt.Sprintf("%s %s", icon, ch.Name), depth: depth, kind: kindChannel, channel: &chCopy, channelID: ch.ID, expanded: isExpanded}}
	if isExpanded {
		for _, th := range threadsByChannel[ch.ID] {
			thCopy := th
			items = append(items, treeItem{label: fmt.Sprintf("  # %s", th.Title), depth: depth + 1, kind: kindThread, thread: &thCopy, channelID: ch.ID})
		}
		if len(threadsByChannel[ch.ID]) == 0 {
			items = append(items, treeItem{label: "    (no threads — press n)", depth: depth + 1, kind: kindThread, channelID: ch.ID})
		}
	}
	return items
}
