package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

var (
	treeBg     = color.RGBA{30, 30, 46, 255}
	treeFg     = color.RGBA{205, 214, 244, 255}
	treeSelBg  = color.RGBA{49, 50, 68, 255}
	treeSelFg  = color.RGBA{245, 245, 245, 255}
	treeBorder = color.RGBA{69, 71, 90, 255}

	chatBg       = color.RGBA{40, 40, 56, 255}
	chatFg       = color.RGBA{180, 190, 210, 255}
	chatSelBg    = color.RGBA{49, 50, 68, 255}
	chatSelFg    = color.RGBA{245, 245, 245, 255}
	chatHeaderFg = color.RGBA{137, 180, 250, 255}
	chatBorder   = color.RGBA{69, 71, 90, 255}

	modalBg      = color.RGBA{49, 50, 68, 255}
	modalFg      = color.RGBA{245, 245, 245, 255}
	modalBorder  = color.RGBA{137, 180, 250, 255}
	modalOverlay = color.RGBA{10, 10, 16, 180}

	statusBg = color.RGBA{24, 24, 37, 255}
	statusFg = color.RGBA{137, 180, 250, 255}

	accent = color.RGBA{137, 180, 250, 255}
	dim    = color.RGBA{100, 100, 120, 255}
)

var (
	treeStyle = lipgloss.NewStyle().
			Width(28).
			Height(1).
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
				Background(treeSelBg).
				PaddingLeft(1)

	chatStyle = lipgloss.NewStyle().
			Height(1).
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
				Background(chatSelBg).
				PaddingLeft(1)

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
)
