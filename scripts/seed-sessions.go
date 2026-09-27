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
	statesThread  = "session states"
	verboseThread = "verbose run"
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

func specs() []devseed.SessionSpec {
	return []devseed.SessionSpec{
		{
			Channel: agentsChannel, Orphaned: true, Thread: statesThread,
			TriggerSeq: 1, Agent: "replacer", Status: store.SessionFailed,
			ReplyMode: "auto", Command: "replacer run",
			ExitCode: 2, Error: "runner exited 2: context deadline exceeded",
			Duration: 8 * time.Minute,
			Events: []devseed.Event{
				{Type: store.SessionEventPrompt, Content: "the inbox wraps on bytes, not display width"},
				{Type: store.SessionEventStdout, Content: "reading internal/tui/inbox_layout.go"},
				{Type: store.SessionEventStderr, Content: "panic: negative wrap index"},
				{Type: store.SessionEventError, Content: "runner exited 2: context deadline exceeded"},
			},
		},
		{
			Channel: agentsChannel, Orphaned: true, Thread: statesThread,
			TriggerSeq: 2, Agent: "replacer", Status: store.SessionCanceled,
			ReplyMode: "auto", Command: "replacer run", Duration: 30 * time.Second,
			Events: []devseed.Event{
				{Type: store.SessionEventPrompt, Content: "the filter matches on channel but not on thread"},
				{Type: store.SessionEventStdout, Content: "canceled by a human"},
			},
		},
		{
			Channel: agentsChannel, Orphaned: true, Thread: statesThread,
			TriggerSeq: 2, Agent: "summarizer", Status: store.SessionSucceeded,
			ReplyMode: "auto", Command: "summarizer run", Duration: 20 * time.Second,
			Events: []devseed.Event{
				{Type: store.SessionEventPrompt, Content: "summarize the filter bug"},
				{Type: store.SessionEventStdout, Content: "filter matches channel, then channel/thread"},
				{Type: store.SessionEventExit, Content: "exit 0"},
			},
		},
		{
			Channel: agentsChannel, Orphaned: true, Thread: statesThread,
			TriggerSeq: 3, Agent: "replacer", Status: store.SessionSucceeded,
			ReplyMode: "auto", Command: "replacer run", Duration: 3 * time.Minute,
			ReplyToSeq: 6,
			Events: []devseed.Event{
				{Type: store.SessionEventPrompt, Content: "run the full suite on main"},
				{Type: store.SessionEventStdout, Content: "ok  internal/tui"},
				{Type: store.SessionEventStdout, Content: "ok  internal/apiserver"},
				{Type: store.SessionEventExit, Content: "exit 0"},
			},
		},
		{
			Channel: agentsChannel, Orphaned: true, Thread: statesThread,
			TriggerSeq: 4, Agent: "summarizer", Status: store.SessionSucceeded,
			ReplyMode: "stdout", Command: "summarizer run", Duration: 45 * time.Second,
			Events: []devseed.Event{
				{Type: store.SessionEventPrompt, Content: "what is the state of the wrap bug"},
				{Type: store.SessionEventStdout, Content: "fixed in 9717ecd"},
				{Type: store.SessionEventExit, Content: "exit 0"},
			},
		},
		{
			Channel: agentsChannel, Orphaned: true, Thread: statesThread,
			TriggerSeq: 5, Agent: "replacer", Status: store.SessionSucceeded,
			ReplyMode: "cli", Command: "replacer run", Duration: 15 * time.Second,
			Events: []devseed.Event{
				{Type: store.SessionEventPrompt, Content: "cancel this one, it is stale"},
				{Type: store.SessionEventStdout, Content: "nothing to do"},
				{Type: store.SessionEventExit, Content: "exit 0"},
			},
		},
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
