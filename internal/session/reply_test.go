package session

import (
	"strings"
	"testing"
	"time"

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
