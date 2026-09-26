package session

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

const msStampLayout = "2006-01-02T15:04:05.000Z"

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
	s, m, thID, msgID := harness(t, cfgWith("cli"), &fakeRunner{})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() { selfPost(t, s, thID, "probe", "my own reply") }
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q error %v", got.Status, got.Error)
	}
	if got.ReplyMessageID != nil {
		t.Fatalf("cli mode recorded its own post as a reply: %d", *got.ReplyMessageID)
	}
}

func TestCliModeWithSilentAgentFails(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("cli"), &fakeRunner{stdout: "words but no post"})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.Status != store.SessionFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "did not reply") {
		t.Fatalf("error = %v", got.Error)
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
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "fallback reply"})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.ReplyMessageID == nil {
		t.Fatal("auto mode did not fall back to stdout")
	}
	msg, _ := s.MessageByID(*got.ReplyMessageID)
	if msg.Content != "fallback reply" {
		t.Fatalf("content = %q", msg.Content)
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

func TestSessionTimestampsSortExactlyAgainstMessageTimestamps(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("stdout"), &fakeRunner{})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)

	if got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatalf("timestamps = %v / %v", got.StartedAt, got.FinishedAt)
	}
	trigger, err := s.MessageByID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range []string{*got.StartedAt, *got.FinishedAt} {
		if len(ts) != len(trigger.CreatedAt) {
			t.Fatalf("session timestamp %q is %d chars, created_at %q is %d", ts, len(ts), trigger.CreatedAt, len(trigger.CreatedAt))
		}
		if _, err := time.Parse(msStampLayout, ts); err != nil {
			t.Fatalf("session timestamp %q is not a fixed 3-digit millisecond stamp: %v", ts, err)
		}
	}

	start, err := time.Parse(time.RFC3339, *got.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	after := start.Add(time.Millisecond).Format(msStampLayout)
	before := start.Add(-time.Millisecond).Format(msStampLayout)

	if _, err := s.AppendMessageAt(thID, "probe", "agent", "assistant", "just after the start", after); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountAgentMessagesSince(thID, "probe", *got.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("agent messages %d at or after the session start, want 1", n)
	}

	if _, err := s.AppendMessageAt(thID, "probe", "agent", "assistant", "just before the start", before); err != nil {
		t.Fatal(err)
	}
	n, err = s.CountAgentMessagesSince(thID, "probe", *got.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("agent messages %d, want the pre-session post to stay excluded", n)
	}
}

func TestASecondSessionInTheSameSecondDoesNotCountTheFirstSessionsPost(t *testing.T) {
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
	t.Logf("first started_at = %s, second started_at = %s", *first.StartedAt, *second.StartedAt)

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
