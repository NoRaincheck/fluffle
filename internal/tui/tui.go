package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/client"
)

type RunResult struct {
	Code    string
	Message string
	OK      bool
}

func Run() RunResult {
	base, err := client.EnsureDaemon()
	if err != nil {
		return RunResult{Code: "DAEMON_DOWN", Message: err.Error()}
	}

	m := New(base)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return RunResult{Code: "TUI_ERROR", Message: err.Error()}
	}
	return RunResult{OK: true}
}
