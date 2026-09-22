package tui

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/NoRaincheck/fluffle/internal/client"
)

// Run starts the TUI. Returns exit code.
func Run() int {
	base, err := client.EnsureDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}

	m := New(base)
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "TUI error:", err)
		return 1
	}
	return 0
}
