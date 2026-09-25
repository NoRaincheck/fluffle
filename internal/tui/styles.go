package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	treeBg     = lipgloss.Color("#1e1e2e")
	treeFg     = lipgloss.Color("#cdd6f4")
	treeSelBg  = lipgloss.Color("#313244")
	treeSelFg  = lipgloss.Color("#f5f5f5")
	treeBorder = lipgloss.Color("#45475a")

	chatBg       = lipgloss.Color("#282838")
	chatFg       = lipgloss.Color("#b4bed2")
	chatSelBg    = lipgloss.Color("#313244")
	chatSelFg    = lipgloss.Color("#f5f5f5")
	chatHeaderFg = lipgloss.Color("#89b4fa")
	chatBorder   = lipgloss.Color("#45475a")

	modalBg      = lipgloss.Color("#313244")
	modalFg      = lipgloss.Color("#f5f5f5")
	modalBorder  = lipgloss.Color("#89b4fa")
	modalOverlay = lipgloss.Color("#0a0a10")

	statusBg = lipgloss.Color("#181825")
	statusFg = lipgloss.Color("#89b4fa")

	accent      = lipgloss.Color("#89b4fa")
	dim         = lipgloss.Color("#646478")
	humanColor  = lipgloss.Color("#89b4fa")
	agentColor  = lipgloss.Color("#b7b4fa")
	systemColor = lipgloss.Color("#646478")

	threadBg         = lipgloss.Color("#1e1a16")
	threadFg         = lipgloss.Color("#cdd6f4")
	threadOpBorder   = lipgloss.Color("#fab387")
	threadOpHeaderFg = lipgloss.Color("#fab387")
	threadRepliesFg  = lipgloss.Color("#f9e2af")
	threadSepFg      = lipgloss.Color("#585062")
)

var (
	treeStyle = lipgloss.NewStyle().
			Width(28).
			Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(treeBorder).
			Background(treeBg).
			Padding(0, 1)

	treeTitleStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)

	treeItemStyle = lipgloss.NewStyle().
			Foreground(treeFg)

	treeItemSelectedStyle = lipgloss.NewStyle().
				Foreground(treeSelFg).
				Background(treeSelBg)

	chatStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, false).
			BorderForeground(chatBorder).
			Background(chatBg).
			Padding(0, 1)

	chatHeaderStyle = lipgloss.NewStyle().
			Foreground(chatHeaderFg).
			Bold(true).
			MarginBottom(1)

	chatMsgStyle = lipgloss.NewStyle().
			Foreground(chatFg)

	chatMsgSelectedStyle = lipgloss.NewStyle().
				Foreground(chatSelFg).
				Background(chatSelBg)

	chatReplyStyle = lipgloss.NewStyle().
			Foreground(dim).
			PaddingLeft(2)

	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(modalBorder).
			Background(modalBg).
			Foreground(modalFg).
			Padding(1, 2).
			Width(60)

	modalOverlayStyle = lipgloss.NewStyle().
				Background(modalOverlay)

	modalTitleStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true).
			MarginBottom(1)

	modalHintStyle = lipgloss.NewStyle().
			Foreground(dim).
			MarginTop(1)

	statusStyle = lipgloss.NewStyle().
			Width(0).
			Height(1).
			Background(statusBg).
			Foreground(statusFg).
			Padding(0, 1)

	hintStyle = lipgloss.NewStyle().
			Width(0).
			Height(1).
			Background(statusBg).
			Foreground(dim).
			Padding(0, 1)

	hintKeyStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)
	humanNameStyle  = lipgloss.NewStyle().Foreground(humanColor)
	agentNameStyle  = lipgloss.NewStyle().Foreground(agentColor)
	systemNameStyle = lipgloss.NewStyle().Foreground(systemColor)
	inboxMoreStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fab387")).Italic(true)
	inboxReplyStyle = lipgloss.NewStyle().Foreground(threadRepliesFg)

	threadTitleStyle = lipgloss.NewStyle().
				Foreground(threadOpHeaderFg).
				Bold(true)

	threadOpStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(threadOpBorder).
			Foreground(threadFg).
			Padding(0, 1)

	threadOpHeaderStyle = lipgloss.NewStyle().
				Foreground(threadOpHeaderFg).
				Bold(true)

	threadRepliesHeaderStyle = lipgloss.NewStyle().
					Foreground(threadRepliesFg).
					Bold(true)

	threadSepStyle = lipgloss.NewStyle().
			Foreground(threadSepFg)

	threadRowNumStyle = lipgloss.NewStyle().
				Foreground(threadSepFg)

	threadRowNumSelectedStyle = lipgloss.NewStyle().
					Foreground(threadRepliesFg).
					Background(chatSelBg)
)

func getNameStyle(authorType string) lipgloss.Style {
	switch authorType {
	case "human":
		return humanNameStyle
	case "agent":
		return agentNameStyle
	default:
		return systemNameStyle
	}
}
