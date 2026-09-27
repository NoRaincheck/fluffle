package tui

import "github.com/charmbracelet/lipgloss"

var (
	accent      = lipgloss.Color("#89b4fa")
	dim         = lipgloss.Color("#646478")
	humanColor  = lipgloss.Color("#89b4fa")
	agentColor  = lipgloss.Color("#b7b4fa")
	systemColor = lipgloss.Color("#646478")
	fg          = lipgloss.Color("#cdd6f4")
	sep         = lipgloss.Color("#585062")
	selBg       = lipgloss.Color("#313244")
	selFg       = lipgloss.Color("#f5f5f5")
	statusBg    = lipgloss.Color("#181825")
	modalBorder = lipgloss.Color("#89b4fa")
	modalFg     = lipgloss.Color("#f5f5f5")
	errColor    = lipgloss.Color("#f38ba8")
)

var (
	rowSelectedStyle  = lipgloss.NewStyle().Foreground(selFg).Background(selBg)
	titleStyle        = lipgloss.NewStyle().Foreground(accent).Bold(true)
	threadHeaderStyle = lipgloss.NewStyle().Foreground(sep)
	threadBodyStyle   = lipgloss.NewStyle().Foreground(fg)
	sepStyle          = lipgloss.NewStyle().Foreground(sep)
	dimStyle          = lipgloss.NewStyle().Foreground(dim)
	placeholderStyle  = lipgloss.NewStyle().Foreground(dim).Italic(true)
	humanNameStyle    = lipgloss.NewStyle().Foreground(humanColor)
	agentNameStyle    = lipgloss.NewStyle().Foreground(agentColor)
	systemNameStyle   = lipgloss.NewStyle().Foreground(systemColor)
	modalTitleStyle   = lipgloss.NewStyle().Foreground(accent).Bold(true)
	modalHintStyle    = lipgloss.NewStyle().Foreground(dim)
	modalErrorStyle   = lipgloss.NewStyle().Foreground(errColor)
	statusStyle       = lipgloss.NewStyle().Background(statusBg).Foreground(accent).Padding(0, 1)
	hintKeyStyle      = lipgloss.NewStyle().Foreground(accent).Bold(true)
)

// nameStyle colours an author by author_type. This is the only colour rule
// in the app.
func nameStyle(authorType string) lipgloss.Style {
	switch authorType {
	case "human":
		return humanNameStyle
	case "agent":
		return agentNameStyle
	default:
		return systemNameStyle
	}
}
