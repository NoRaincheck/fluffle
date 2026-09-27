package devseed

import (
	"fmt"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

const (
	triggerAt  = "2026-09-20T10:00:00Z"
	replyAt    = "2026-09-20T10:00:30Z"
	triggerSeq = int64(1)
	replySeq   = int64(2)
)

func fixture(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chID, err := s.CreateChannel("agents", "", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	thID, err := s.CreateThread(chID, "session states")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessageAt(thID, "alice", "human", "user", "@summarizer take a look", triggerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessageAt(thID, "summarizer", "agent", "assistant", "Looks fine to me.", replyAt); err != nil {
		t.Fatal(err)
	}
	return s
}

func baseSpec(status string) SessionSpec {
	return SessionSpec{
		Channel:    "agents",
		Orphaned:   true,
		Thread:     "session states",
		TriggerSeq: triggerSeq,
		Agent:      "summarizer",
		Status:     status,
		ReplyMode:  "auto",
		Command:    "summarizer run",
		Duration:   90 * time.Second,
	}
}

func TestStageEveryStatus(t *testing.T) {
	statuses := []string{
		store.SessionQueued,
		store.SessionRunning,
		store.SessionSucceeded,
		store.SessionFailed,
		store.SessionCanceled,
	}
	s := fixture(t)
	specs := make([]SessionSpec, len(statuses))
	for i, status := range statuses {
		specs[i] = baseSpec(status)
		specs[i].Agent = "agent-" + status
	}
	ids, err := Stage(s, specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(statuses) {
		t.Fatalf("got %d ids, want %d", len(ids), len(statuses))
	}
	got, err := s.ListSessions(threadIDOf(t, s))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(statuses) {
		t.Fatalf("stored %d sessions, want %d", len(got), len(statuses))
	}
	for i, want := range statuses {
		if got[i].Status != want {
			t.Errorf("session %d status = %q, want %q", got[i].ID, got[i].Status, want)
		}
		if got[i].AgentName != "agent-"+want {
			t.Errorf("session %d agent = %q, want %q", got[i].ID, got[i].AgentName, "agent-"+want)
		}
		if got[i].ReplyMode != "auto" {
			t.Errorf("session %d reply mode = %q", got[i].ID, got[i].ReplyMode)
		}
	}
}

func TestStageTwoAgentsOnOneTriggerMessage(t *testing.T) {
	s := fixture(t)
	replacer := baseSpec(store.SessionSucceeded)
	replacer.Agent = "replacer"
	summarizer := baseSpec(store.SessionFailed)
	summarizer.Agent = "summarizer"
	ids, err := Stage(s, []SessionSpec{replacer, summarizer})
	if err != nil {
		t.Fatal(err)
	}
	if ids[0] >= ids[1] {
		t.Errorf("second session id %d not after first %d", ids[1], ids[0])
	}
	got, err := s.GetSession(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.GetSession(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.TriggerMessageID != first.TriggerMessageID {
		t.Errorf("trigger message = %d, want shared %d", got.TriggerMessageID, first.TriggerMessageID)
	}
}

func TestStageRejectsDuplicateAgentOnOneTrigger(t *testing.T) {
	s := fixture(t)
	if _, err := Stage(s, []SessionSpec{baseSpec(store.SessionSucceeded), baseSpec(store.SessionFailed)}); err == nil {
		t.Fatal("expected error for duplicate agent on one trigger")
	}
}

func TestStageSetsTriggerMessageFromSeq(t *testing.T) {
	s := fixture(t)
	ids, err := Stage(s, []SessionSpec{baseSpec(store.SessionSucceeded)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListMessages(got.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.TriggerMessageID != msgs[0].ID {
		t.Errorf("trigger message = %d, want %d", got.TriggerMessageID, msgs[0].ID)
	}
}

func TestStageLinksAgentReply(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionSucceeded)
	spec.ReplyToSeq = replySeq
	ids, err := Stage(s, []SessionSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.ReplyMessageID == nil {
		t.Fatal("reply message not linked")
	}
	msgs, err := s.ListMessages(got.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if *got.ReplyMessageID != msgs[1].ID {
		t.Errorf("reply message = %d, want %d", *got.ReplyMessageID, msgs[1].ID)
	}
}

func TestStageOmitsReplyWhenUnset(t *testing.T) {
	s := fixture(t)
	ids, err := Stage(s, []SessionSpec{baseSpec(store.SessionSucceeded)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.ReplyMessageID != nil {
		t.Errorf("reply message = %d, want none", *got.ReplyMessageID)
	}
}

func TestStageFailedCarriesExitCodeAndError(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionFailed)
	spec.ExitCode = 2
	spec.Error = "runner exited 2: context deadline exceeded"
	ids, err := Stage(s, []SessionSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode == nil || *got.ExitCode != 2 {
		t.Errorf("exit code = %v, want 2", got.ExitCode)
	}
	if got.Error == nil || *got.Error != spec.Error {
		t.Errorf("error = %v, want %q", got.Error, spec.Error)
	}
}

func TestStageTimestampsFollowTrigger(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionSucceeded)
	spec.Duration = 90 * time.Second
	ids, err := Stage(s, []SessionSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt == nil {
		t.Fatal("started_at not set")
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at not set")
	}
	started, err := time.Parse(time.RFC3339, *got.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := time.Parse(time.RFC3339, *got.FinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	base, err := time.Parse(time.RFC3339, triggerAt)
	if err != nil {
		t.Fatal(err)
	}
	if !started.After(base) {
		t.Errorf("started_at %s not after trigger %s", started, base)
	}
	if elapsed := finished.Sub(started); elapsed != spec.Duration {
		t.Errorf("elapsed = %s, want %s", elapsed, spec.Duration)
	}
}

func TestStageQueuedHasNoStartOrFinish(t *testing.T) {
	s := fixture(t)
	ids, err := Stage(s, []SessionSpec{baseSpec(store.SessionQueued)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt != nil {
		t.Errorf("started_at = %v, want none", *got.StartedAt)
	}
	if got.FinishedAt != nil {
		t.Errorf("finished_at = %v, want none", *got.FinishedAt)
	}
}

func TestStageRunningHasNoFinish(t *testing.T) {
	s := fixture(t)
	ids, err := Stage(s, []SessionSpec{baseSpec(store.SessionRunning)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt == nil {
		t.Error("started_at not set on running session")
	}
	if got.FinishedAt != nil {
		t.Errorf("finished_at = %v, want none", *got.FinishedAt)
	}
}

func TestStageAppendsEventsInOrder(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionSucceeded)
	spec.Events = []Event{
		{Type: store.SessionEventPrompt, Content: "review the inbox layout patch"},
		{Type: store.SessionEventStdout, Content: "reading 12 messages"},
		{Type: store.SessionEventStderr, Content: "warning: thread 9 has no OP"},
		{Type: store.SessionEventStdout, Content: "done"},
		{Type: store.SessionEventExit, Content: "exit 0"},
	}
	ids, err := Stage(s, []SessionSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.ListSessionEvents(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(spec.Events) {
		t.Fatalf("got %d events, want %d", len(events), len(spec.Events))
	}
	for i, want := range spec.Events {
		if events[i].Seq != int64(i+1) {
			t.Errorf("event %d seq = %d, want %d", i, events[i].Seq, i+1)
		}
		if events[i].Type != want.Type {
			t.Errorf("event %d type = %q, want %q", i, events[i].Type, want.Type)
		}
		if events[i].Content != want.Content {
			t.Errorf("event %d content = %q, want %q", i, events[i].Content, want.Content)
		}
	}
}

func TestStageVerboseRunKeepsEveryEvent(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionSucceeded)
	for i := range 40 {
		spec.Events = append(spec.Events, Event{
			Type:    store.SessionEventStdout,
			Content: fmt.Sprintf("chunk %02d of the verbose run", i),
		})
	}
	ids, err := Stage(s, []SessionSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.ListSessionEvents(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 40 {
		t.Fatalf("got %d events, want 40", len(events))
	}
}

func TestStageRejectsUnknownChannel(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionSucceeded)
	spec.Channel = "nope"
	if _, err := Stage(s, []SessionSpec{spec}); err == nil {
		t.Fatal("expected error for unknown channel")
	}
}

func TestStageRejectsUnknownThread(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionSucceeded)
	spec.Thread = "nope"
	if _, err := Stage(s, []SessionSpec{spec}); err == nil {
		t.Fatal("expected error for unknown thread")
	}
}

func TestStageRejectsUnknownTriggerSeq(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionSucceeded)
	spec.TriggerSeq = 99
	if _, err := Stage(s, []SessionSpec{spec}); err == nil {
		t.Fatal("expected error for unknown trigger seq")
	}
}

func TestStageRejectsUnknownReplySeq(t *testing.T) {
	s := fixture(t)
	spec := baseSpec(store.SessionSucceeded)
	spec.ReplyToSeq = 99
	if _, err := Stage(s, []SessionSpec{spec}); err == nil {
		t.Fatal("expected error for unknown reply seq")
	}
}

func TestStageRejectsBadStatus(t *testing.T) {
	s := fixture(t)
	if _, err := Stage(s, []SessionSpec{baseSpec("bogus")}); err == nil {
		t.Fatal("expected error for bad status")
	}
}

func TestStageNoSpecs(t *testing.T) {
	s := fixture(t)
	ids, err := Stage(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("got %d ids, want 0", len(ids))
	}
}

func threadIDOf(t *testing.T, s *store.Store) int64 {
	t.Helper()
	channels, err := s.ListChannels("", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range channels {
		if ch.Name != "agents" {
			continue
		}
		threads, err := s.ListThreads(ch.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range threads {
			if th.Title == "session states" {
				return th.ID
			}
		}
	}
	t.Fatal("fixture thread not found")
	return 0
}
