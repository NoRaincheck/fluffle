//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/NoRaincheck/fluffle/internal/client"
	"github.com/NoRaincheck/fluffle/internal/devseed"
	"github.com/NoRaincheck/fluffle/internal/store"
)

const (
	agentsChannel = "agents"
	verboseThread = "verbose-run"
)

func verboseEvents() []devseed.Event {
	events := []devseed.Event{
		{Type: store.SessionEventPrompt, Content: "run the whole suite with -v and paste the output"},
	}
	for i := range 38 {
		events = append(events, devseed.Event{
			Type:    store.SessionEventStdout,
			Content: fmt.Sprintf("=== RUN   TestSomething%02d", i),
		})
	}
	return append(events,
		devseed.Event{Type: store.SessionEventStdout, Content: "ok  github.com/NoRaincheck/fluffle/internal/tui"},
		devseed.Event{Type: store.SessionEventExit, Content: "exit 0"},
	)
}

// One staged session, not seven. The rewrite left the TUI with no session pane
// and no key that opens one, so a thread whose only purpose was to show failed
// and canceled and succeeded side by side has nothing to show: the states were
// never drawn, they were columns in a view that no longer exists. A long
// transcript is still worth staging, because `flf agent session --id N` reads
// it, and because it is the one run the TUI cannot show but a person can.
func specs() []devseed.SessionSpec {
	return []devseed.SessionSpec{
		{
			Channel: agentsChannel, Orphaned: true, Thread: verboseThread,
			TriggerSeq: 1, Agent: "replacer", Status: store.SessionSucceeded,
			ReplyMode: "auto", Command: "replacer run -v",
			Duration: 12 * time.Minute, Events: verboseEvents(),
		},
	}
}

func main() {
	home := client.FluffleHome()
	path := filepath.Join(home, "fluffle.db")
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "no database at %s: %v\n", path, err)
		os.Exit(2)
	}
	s, err := store.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", path, err)
		os.Exit(2)
	}
	defer s.Close()
	ids, err := devseed.Stage(s, specs())
	if err != nil {
		fmt.Fprintf(os.Stderr, "stage sessions: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("staged %d sessions\n", len(ids))
}
