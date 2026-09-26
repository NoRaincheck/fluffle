package session

import (
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/store"
)

func strptr(s string) *string { return &s }

func entryFor(name, reply string) agentcfg.Entry {
	return agentcfg.Entry{Agent: agentcfg.Agent{Name: name, Reply: reply, TimeoutSecs: 300}, Source: "test"}
}

func TestBuildPromptContainsContextAndRequest(t *testing.T) {
	lines := []jsonl.Line{
		{Type: "message", Seq: 1, Role: "user", Name: "alice", AuthorType: "human", Content: "first", Timestamp: "2026-01-01T00:00:00Z"},
		{Type: "message", Seq: 2, Role: "assistant", Name: "probe", AuthorType: "agent", Content: "prior answer", Timestamp: "2026-01-01T00:01:00Z"},
		{Type: "reaction", MessageSeq: 1, Name: "bob", AuthorType: "human", Emoji: "👀", Timestamp: "2026-01-01T00:02:00Z"},
	}
	tc := store.ThreadContext{ThreadID: 7, ThreadTitle: "hello", ChannelID: 2, ChannelName: "general", RepoAbsPath: strptr("/repo")}
	trigger := store.Message{ID: 9, ThreadID: 7, Seq: 3, Name: "alice", AuthorType: "human", Role: "user", Content: "@probe do xyz"}

	got, err := buildPrompt(promptInput{
		Agent:         entryFor("probe", "auto"),
		Thread:        tc,
		History:       lines,
		Trigger:       trigger,
		NeedsReplying: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Thread", "general", "hello", "/repo",
		"## History", "prior answer", "👀",
		"## Request", "#3", "@probe do xyz",
		"## Replying", "flf message send --thread 7", "--agent-id probe",
		"stdout is posted for you",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q\n---\n%s", want, got)
		}
	}
}

func TestBuildPromptThreadsTheReplyToTheTrigger(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:         entryFor("probe", "cli"),
		Thread:        store.ThreadContext{ThreadID: 7, ThreadTitle: "t", ChannelName: "c"},
		Trigger:       store.Message{ID: 9, ThreadID: 7, Seq: 3, Content: "@probe hi"},
		NeedsReplying: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--reply-to-seq 3") {
		t.Fatalf("prompt must thread the reply to the trigger seq:\n%s", got)
	}
	if strings.Contains(got, "--reply-to ") {
		t.Fatalf("reply-to and reply-to-seq are mutually exclusive:\n%s", got)
	}
}

func TestBuildPromptOmitsReplyingBlockForStdout(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:         entryFor("probe", "stdout"),
		Thread:        store.ThreadContext{ThreadID: 7, ThreadTitle: "t", ChannelName: "c", RepoAbsPath: strptr("/repo")},
		Trigger:       store.Message{ID: 1, ThreadID: 7, Seq: 1, Content: "@probe hi"},
		NeedsReplying: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "## Replying") {
		t.Fatalf("stdout mode must not include the replying block:\n%s", got)
	}
}

func TestBuildPromptCliModeHasNoStdoutFallbackLine(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:         entryFor("probe", "cli"),
		Thread:        store.ThreadContext{ThreadID: 7, ThreadTitle: "t", ChannelName: "c"},
		Trigger:       store.Message{ID: 1, ThreadID: 7, Seq: 1, Content: "@probe hi"},
		NeedsReplying: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "## Replying") {
		t.Fatalf("cli mode must include the replying block:\n%s", got)
	}
	if strings.Contains(got, "stdout is posted for you") {
		t.Fatalf("cli mode must not promise a stdout fallback:\n%s", got)
	}
}

func TestBuildPromptSystemPromptComesFirst(t *testing.T) {
	entry := entryFor("probe", "stdout")
	entry.SystemPrompt = "be terse"
	got, err := buildPrompt(promptInput{
		Agent:   entry,
		Thread:  store.ThreadContext{ThreadID: 1, ThreadTitle: "t", ChannelName: "c"},
		Trigger: store.Message{ID: 1, ThreadID: 1, Seq: 1, Content: "@probe hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "be terse") {
		t.Fatalf("system prompt must come first:\n%s", got)
	}
}

func TestBuildPromptDescribesOrphanRepo(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:   entryFor("probe", "stdout"),
		Thread:  store.ThreadContext{ThreadID: 1, ThreadTitle: "t", ChannelName: "c"},
		Trigger: store.Message{ID: 1, ThreadID: 1, Seq: 1, Content: "@probe hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "repository: none") {
		t.Fatalf("orphan repo must be described as none:\n%s", got)
	}
}

func TestBuildPromptEmptyHistorySaysSo(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:   entryFor("probe", "stdout"),
		Thread:  store.ThreadContext{ThreadID: 1, ThreadTitle: "t", ChannelName: "c"},
		Trigger: store.Message{ID: 1, ThreadID: 1, Seq: 1, Content: "@probe hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(empty)") {
		t.Fatalf("empty history must be explicit:\n%s", got)
	}
}

func TestDeliverPromptSubstitutesArgv(t *testing.T) {
	args, stdin := deliverPrompt([]string{"-p", "{prompt}", "--flag"}, "PROMPT")
	if len(args) != 3 || args[0] != "-p" || args[1] != "PROMPT" || args[2] != "--flag" {
		t.Fatalf("args = %#v", args)
	}
	if stdin != "" {
		t.Fatalf("stdin = %q, want empty when a placeholder is present", stdin)
	}
}

func TestDeliverPromptUsesStdinWithoutPlaceholder(t *testing.T) {
	args, stdin := deliverPrompt([]string{"--json"}, "PROMPT")
	if len(args) != 1 || args[0] != "--json" {
		t.Fatalf("args = %#v", args)
	}
	if stdin != "PROMPT" {
		t.Fatalf("stdin = %q, want PROMPT", stdin)
	}
}

func TestDeliverPromptReplacesEveryOccurrence(t *testing.T) {
	args, _ := deliverPrompt([]string{"{prompt}", "{prompt}"}, "P")
	if args[0] != "P" || args[1] != "P" {
		t.Fatalf("args = %#v", args)
	}
}

func TestEnvSliceIsNilWhenEmpty(t *testing.T) {
	if envSlice(nil) != nil {
		t.Fatal("envSlice(nil) must be nil")
	}
	got := envSlice(map[string]string{"A": "1"})
	if len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("envSlice = %#v", got)
	}
}
