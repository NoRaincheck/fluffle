package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/client"
)

func Run() int {
	base, err := client.EnsureDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}

	m := New(base)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "TUI error:", err)
		return 1
	}
	return 0
}
