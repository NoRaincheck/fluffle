package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/store"
)

func cfgWith(reply string) string {
	return "[[agents]]\nname=\"probe\"\ncommand=\"/bin/probe\"\nreply=\"" + reply + "\"\ntimeout_secs=5\n"
}

func selfPost(t *testing.T, s *store.Store, thID int64, name, text string) {
	t.Helper()
	if _, err := s.AppendMessage(thID, name, "agent", "assistant", text); err != nil {
		t.Fatal(err)
	}
}

func TestStdoutModePostsTrimmedReplyParentedToTrigger(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("stdout"), &fakeRunner{stdout: "  the answer  \n"})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)

	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.Status)
	}
	if got.ReplyMessageID == nil {
		t.Fatal("no reply message recorded")
	}
	msg, err := s.MessageByID(*got.ReplyMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "the answer" {
		t.Fatalf("content = %q, want trimmed stdout", msg.Content)
	}
	if msg.Name != "probe" || msg.AuthorType != "agent" || msg.Role != "assistant" {
		t.Fatalf("attribution = %+v", msg)
	}
	if msg.ParentID.Int64 != msgID {
		t.Fatalf("reply parent = %d, want the trigger %d", msg.ParentID.Int64, msgID)
	}
}

func TestStdoutModeWithBlankOutputPostsNothingButSucceeds(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("stdout"), &fakeRunner{stdout: "   \n\t "})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.Status)
	}
	if got.ReplyMessageID != nil {
		t.Fatalf("unexpected reply %d", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestCliModeNeverPostsOnTheAgentsBehalf(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("cli"), &fakeRunner{stdout: "should be ignored"})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.ReplyMessageID != nil {
		t.Fatalf("cli mode posted a reply: %d", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 1 {
		t.Fatalf("cli mode appended a message: %+v", msgs)
	}
}

func TestCliModeWithSelfPostSucceeds(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("cli"), &fakeRunner{exit: 3})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() { selfPost(t, s, thID, "probe", "my own reply") }
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q error %v", got.Status, got.Error)
	}
	if got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("exit code = %v, want 3", got.ExitCode)
	}
	if got.ReplyMessageID != nil {
		t.Fatalf("cli mode recorded its own post as a reply: %d", *got.ReplyMessageID)
	}
}

func TestCliModeWithSilentAgentFails(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("cli"), &fakeRunner{stdout: "words but no post", exit: 4})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.Status != store.SessionFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "did not reply") {
		t.Fatalf("error = %v", got.Error)
	}
	if got.ExitCode == nil || *got.ExitCode != 4 {
		t.Fatalf("exit code = %v, want a failure to carry the code 4", got.ExitCode)
	}
	if !eventsContain(s, got.ID, store.SessionEventError, "did not reply") {
		t.Fatalf("no error event: %v", eventTypes(s, got.ID))
	}
}

func TestAutoModeWithSelfPostDoesNotDuplicate(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "duplicate risk"})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() { selfPost(t, s, thID, "probe", "the real reply") }
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.ReplyMessageID != nil {
		t.Fatalf("auto mode duplicated the reply: %d", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want trigger plus one agent post", msgs)
	}
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestAutoModeFallsBackToStdoutWhenSilent(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "fallback reply", exit: 5})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.ReplyMessageID == nil {
		t.Fatal("auto mode did not fall back to stdout")
	}
	msg, _ := s.MessageByID(*got.ReplyMessageID)
	if msg.Content != "fallback reply" {
		t.Fatalf("content = %q", msg.Content)
	}
	if got.ExitCode == nil || *got.ExitCode != 5 {
		t.Fatalf("exit code = %v, want 5", got.ExitCode)
	}
}

func TestAutoModeWaitsForALateAgentPost(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "this must not be posted"})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() {
		go func() {
			time.Sleep(400 * time.Millisecond)
			selfPost(t, s, thID, "probe", "the real reply")
		}()
	}
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)

	if got.ReplyMessageID != nil {
		t.Fatalf("the exit race produced a duplicate reply: %d", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want trigger plus the late agent post", msgs)
	}
}

func TestAnotherAgentsPostDoesNotCountAsSelfPost(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "should post"})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() { selfPost(t, s, thID, "someone-else", "not mine") }
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.ReplyMessageID == nil {
		t.Fatal("another agent's post must not suppress the stdout fallback")
	}
}

func TestHumanPostDoesNotCountAsSelfPost(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "should post"})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() {
		if _, err := s.AppendMessage(thID, "alice", "human", "user", "human follow up"); err != nil {
			t.Error(err)
		}
	}
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.ReplyMessageID == nil {
		t.Fatal("a human post must not suppress the stdout fallback")
	}
}

func TestASecondSessionDoesNotCountTheFirstSessionsEarlierPost(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "second stdout"})
	fr := m.runner.(*fakeRunner)
	var once sync.Once
	fr.onStart = func() { once.Do(func() { selfPost(t, s, thID, "probe", "first reply") }) }

	m.Start(thID, msgID, []string{"probe"})
	first := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if first.Status != store.SessionSucceeded || first.ReplyMessageID != nil {
		t.Fatalf("first session = %+v", first)
	}

	seq, err := s.AppendMessage(thID, "alice", "human", "user", "@probe again")
	if err != nil {
		t.Fatal(err)
	}
	secondTrigger, err := s.MessageIDBySeq(thID, seq)
	if err != nil {
		t.Fatal(err)
	}
	m.Start(thID, secondTrigger, []string{"probe"})
	sessions, err := s.ListSessions(thID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want 2", sessions)
	}
	second := waitSession(t, s, sessions[1].ID, 15*time.Second)

	if second.Status != store.SessionSucceeded {
		t.Fatalf("second status = %q error %v", second.Status, second.Error)
	}
	if second.ReplyMessageID == nil {
		t.Fatal("the second session counted the first session's post as its own reply")
	}
	msg, err := s.MessageByID(*second.ReplyMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "second stdout" {
		t.Fatalf("content = %q", msg.Content)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 4 {
		t.Fatalf("messages = %+v, want two triggers, the first post, and the second stdout fallback", msgs)
	}
}

func TestDetectionIsBoundedBySeqNotByWallClock(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "second stdout"})
	fr := m.runner.(*fakeRunner)
	var once sync.Once
	fr.onStart = func() {
		once.Do(func() {
			if _, err := s.AppendMessageAt(thID, "probe", "agent", "assistant", "first reply", "2999-01-01T00:00:00.000Z"); err != nil {
				t.Error(err)
			}
		})
	}

	m.Start(thID, msgID, []string{"probe"})
	first := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if first.ReplyMessageID != nil {
		t.Fatalf("first session = %+v", first)
	}

	seq, err := s.AppendMessage(thID, "alice", "human", "user", "@probe again")
	if err != nil {
		t.Fatal(err)
	}
	secondTrigger, err := s.MessageIDBySeq(thID, seq)
	if err != nil {
		t.Fatal(err)
	}
	m.Start(thID, secondTrigger, []string{"probe"})
	sessions, err := s.ListSessions(thID)
	if err != nil {
		t.Fatal(err)
	}
	second := waitSession(t, s, sessions[1].ID, 15*time.Second)

	if second.ReplyMessageID == nil {
		t.Fatal("detection is bounded by wall clock: a post stamped in the future suppressed the second session's reply")
	}
}

func TestCancelDuringReplyGraceCancelsAndPostsNothing(t *testing.T) {
	for _, reply := range []string{"auto", "cli"} {
		t.Run(reply, func(t *testing.T) {
			s, m, thID, msgID := harness(t, cfgWith(reply), &fakeRunner{stdout: "must not be posted", exit: 7})
			m.Start(thID, msgID, []string{"probe"})
			id := onlySession(t, s, thID).ID

			time.Sleep(200 * time.Millisecond)
			canceledAt := time.Now()
			if err := m.Cancel(id); err != nil {
				t.Fatal(err)
			}
			got := waitSession(t, s, id, 10*time.Second)
			elapsed := time.Since(canceledAt)

			if got.Status != store.SessionCanceled {
				t.Fatalf("status = %q error %v", got.Status, got.Error)
			}
			if got.ReplyMessageID != nil {
				t.Fatalf("reply %d was posted after the cancel", *got.ReplyMessageID)
			}
			msgs, _ := s.ListMessages(thID, 0)
			if len(msgs) != 1 {
				t.Fatalf("messages = %+v, want the trigger only", msgs)
			}
			if elapsed > time.Second {
				t.Fatalf("took %s to unwind, want the grace poll to return promptly", elapsed)
			}
		})
	}
}

func TestPostReplyOnACanceledContextCancelsAndAppendsNothing(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("stdout"), &fakeRunner{})
	entry := agentcfg.Entry{Agent: agentcfg.Agent{Name: "probe", Reply: "stdout", Command: "/bin/probe"}}
	tc := store.ThreadContext{ThreadID: thID}
	trigger, err := s.MessageByID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateSession(thID, msgID, "probe", store.SessionQueued, "stdout", "/bin/probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m.postReply(ctx, id, entry, tc, trigger, "must not be posted", int64ptr(9))

	got, err := s.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.SessionCanceled {
		t.Fatalf("status = %q error %v", got.Status, got.Error)
	}
	if got.ReplyMessageID != nil {
		t.Fatalf("reply %d was posted after the cancel", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want the trigger only", msgs)
	}
}

func TestAgentPostedSurfacesAPersistentStoreError(t *testing.T) {
	s, m, thID, _ := harness(t, cfgWith("cli"), &fakeRunner{})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	posted, err := m.agentPosted(t.Context(), thID, "probe", 0)
	if posted {
		t.Fatal("a failing count must not read as a reply")
	}
	if err == nil {
		t.Fatal("a persistent store error was swallowed")
	}
}
