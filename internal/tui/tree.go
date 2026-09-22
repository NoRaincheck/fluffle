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

type treeItem struct {
	label    string
	children []treeItem
	depth    int
	isLeaf   bool
	channel  *store.Channel
	thread   *store.Thread
	message  *store.Message
	expanded bool
}

type treeModel struct {
	items  []treeItem
	cursor int
	width  int
	height int
	filter string
	sortBy treeSort
}

func newTreeModel() treeModel {
	return treeModel{
		sortBy: sortLastUpdate,
	}
}

func (m treeModel) Init() tea.Cmd {
	return nil
}

func (m *treeModel) SetChannels(channels []store.Channel) {
	m.items = buildTree(channels)
	m.cursor = 0
	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
}

func (m *treeModel) SetFilter(f string) {
	m.filter = f
}

func (m *treeModel) ToggleSort() {
	m.sortBy = (m.sortBy + 1) % 3
}

func (m *treeModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

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
		case tea.KeyEnter:
			if m.cursor < len(m.items) {
				item := &m.items[m.cursor]
				if !item.isLeaf {
					item.expanded = !item.expanded
				}
			}
		}
	}
	return nil
}

func (m treeModel) View() string {
	if len(m.items) == 0 {
		return lipgloss.NewStyle().Padding(0, 1).Render("no channels")
	}

	lines := make([]string, 0, m.height)
	visible := m.visibleItems()

	for i, idx := range visible {
		if i >= m.height {
			break
		}
		item := &m.items[idx]
		var line string
		if idx == m.cursor {
			line = treeItemSelectedStyle.Render("▸ " + item.label)
		} else {
			line = treeItemStyle.Render("  " + item.label)
		}
		lines = append(lines, line)
	}

	for len(lines) < m.height {
		lines = append(lines, "")
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m treeModel) visibleItems() []int {
	visible := make([]int, 0, len(m.items))
	for i := range m.items {
		if m.isAncestorVisible(i) {
			visible = append(visible, i)
		}
	}
	return visible
}

func (m treeModel) isAncestorVisible(idx int) bool {
	// Simple: show all items; expansion only affects children rendering
	return true
}

func (m treeModel) SelectedChannel() *store.Channel {
	if m.cursor < len(m.items) {
		item := &m.items[m.cursor]
		if item.channel != nil {
			return item.channel
		}
	}
	return nil
}

func (m treeModel) SelectedThread() *store.Thread {
	if m.cursor < len(m.items) {
		item := &m.items[m.cursor]
		if item.thread != nil {
			return item.thread
		}
	}
	return nil
}

func (m treeModel) SelectedMessage() *store.Message {
	if m.cursor < len(m.items) {
		item := &m.items[m.cursor]
		if item.message != nil {
			return item.message
		}
	}
	return nil
}

func buildTree(channels []store.Channel) []treeItem {
	// Group by repo
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

	// Orphaned group
	if orphans, ok := repoMap["__orphaned__"]; ok {
		items = append(items, treeItem{
			label:    "🗂 Orphaned",
			depth:    0,
			isLeaf:   false,
			expanded: false,
		})
		for _, ch := range orphans {
			items = append(items, channelToTreeItem(ch, 1))
		}
	}

	// Regular repos
	for _, repo := range repoOrder {
		if repo == "__orphaned__" {
			continue
		}
		channels := repoMap[repo]

		// Group by branch
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
			items = append(items, treeItem{
				label:    fmt.Sprintf("📂 %s (%s)", repoLabel, branch),
				depth:    0,
				isLeaf:   false,
				expanded: false,
			})
			for _, ch := range branchMap[branch] {
				items = append(items, channelToTreeItem(ch, 1))
			}
		}
	}

	return items
}

func channelToTreeItem(ch store.Channel, depth int) treeItem {
	return treeItem{
		label:   fmt.Sprintf("🗨 %s", ch.Name),
		depth:   depth,
		isLeaf:  false,
		channel: &ch,
	}
}
