package tui

import (
	"fmt"
	"sort"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type filterInput struct {
	active bool
	text   string
	cursor int
}

func (f filterInput) Init() tea.Cmd {
	return nil
}

func (f *filterInput) Update(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if !f.active {
			return
		}
		switch key := msg.Key(); key.Code {
		case tea.KeyEscape:
			f.active = false
			f.text = ""
			f.cursor = 0
		case tea.KeyBackspace:
			if f.cursor > 0 && len(f.text) > 0 {
				f.text = f.text[:f.cursor-1] + f.text[f.cursor:]
				f.cursor--
			}
		case tea.KeyDelete:
			if f.cursor < len(f.text) {
				f.text = f.text[:f.cursor] + f.text[f.cursor+1:]
			}
		case tea.KeyLeft:
			if f.cursor > 0 {
				f.cursor--
			}
		case tea.KeyRight:
			if f.cursor < len(f.text) {
				f.cursor++
			}
		default:
			if len(key.Text) == 1 && key.Text[0] != ' ' {
				f.text = f.text[:f.cursor] + key.Text + f.text[f.cursor:]
				f.cursor++
			}
		}
	}
}

func (f filterInput) View() string {
	if !f.active {
		return ""
	}
	prompt := "Filter: ["
	cursorChar := "│"
	if f.cursor >= len(f.text) {
		cursorChar = "│ "
	}
	return lipgloss.NewStyle().Foreground(accent).Render(prompt + f.text + cursorChar + "]")
}

func (f filterInput) Text() string {
	return f.text
}

type filterSortBar struct {
	filter filterInput
	sortBy treeSort
}

func (f *filterSortBar) Init() tea.Cmd {
	return nil
}

func (f *filterSortBar) Update(msg tea.Msg) {
	f.filter.Update(msg)
}

func (f *filterSortBar) ToggleSort() {
	f.sortBy = (f.sortBy + 1) % 3
}

func (f *filterSortBar) SortLabel() string {
	switch f.sortBy {
	case sortLastUpdate:
		return "sort: updated"
	case sortAlphaName:
		return "sort: name"
	case sortAlphaRepo:
		return "sort: repo"
	}
	return ""
}

func (f *filterSortBar) View() string {
	parts := []string{f.filter.View()}
	if label := f.SortLabel(); label != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(dim).Render(label))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func sortChannels(channels []store.Channel, sortBy treeSort) []store.Channel {
	sorted := make([]store.Channel, len(channels))
	copy(sorted, channels)
	switch sortBy {
	case sortLastUpdate:
		// Keep original order (by ID, which correlates with creation time)
	case sortAlphaName:
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Name < sorted[j].Name
		})
	case sortAlphaRepo:
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].RepoAbsPath != sorted[j].RepoAbsPath {
				return sorted[i].RepoAbsPath < sorted[j].RepoAbsPath
			}
			return sorted[i].Name < sorted[j].Name
		})
	}
	return sorted
}

func (m *treeModel) filterAndSort(channels []store.Channel) []treeItem {
	// Apply filter
	if m.filter != "" {
		channels = filterChannels(channels, m.filter)
	}
	// Apply sort
	channels = sortChannels(channels, m.sortBy)
	// Rebuild tree
	return buildTree(channels)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func formatTime(t string) string {
	if t == "" {
		return ""
	}
	// Try to parse RFC3339
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
