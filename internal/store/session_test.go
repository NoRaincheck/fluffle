package store

import (
	"context"
	"errors"
	"testing"
)

func newSessionFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	chID, err := s.CreateChannel(context.Background(), "c", "/repo", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	thID, err := s.CreateThread(context.Background(), chID, "t")
	if err != nil {
		t.Fatal(err)
	}
	return s, thID
}

func triggerMessage(t *testing.T, s *Store, thID int64, content string) int64 {
	t.Helper()
	seq, err := s.AppendMessage(context.Background(), thID, "alice", "human", "user", content)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.MessageIDBySeq(context.Background(), thID, seq)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCreateSessionAndReadBack(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	cwd := "/repo"
	id, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "probe -p {prompt}", &cwd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != thID || got.TriggerMessageID != msgID || got.AgentName != "probe" {
		t.Fatalf("session = %+v", got)
	}
	if got.Status != SessionQueued || got.ReplyMode != "auto" {
		t.Fatalf("status/mode = %q/%q", got.Status, got.ReplyMode)
	}
	if got.Cwd == nil || *got.Cwd != "/repo" {
		t.Fatalf("cwd = %v", got.Cwd)
	}
	if got.StartedAt != nil || got.FinishedAt != nil || got.ReplyMessageID != nil {
		t.Fatalf("unexpected set fields: %+v", got)
	}
	if got.CreatedAt == "" {
		t.Fatal("CreatedAt empty")
	}
}

func TestCreateSessionIsIdempotentPerTriggerAndAgent(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	if _, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if _, err := s.CreateSession(context.Background(), thID, msgID, "other", SessionQueued, "auto", "c", nil); err != nil {
		t.Fatalf("a second agent for the same message should succeed: %v", err)
	}
}

func TestCreateSessionValidates(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	if _, err := s.CreateSession(context.Background(), thID, 9999, "probe", SessionQueued, "auto", "c", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing trigger: err = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateSession(context.Background(), thID, msgID, "  ", SessionQueued, "auto", "c", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank name: err = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateSession(context.Background(), thID, msgID, "probe", "weird", "auto", "c", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: err = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "nope", "c", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad reply mode: err = %v, want ErrInvalid", err)
	}
}

func TestCreateSessionRejectsCrossThreadTrigger(t *testing.T) {
	s, thID := newSessionFixture(t)
	chID, _ := s.ListChannels(context.Background(), "", false)
	other, _ := s.CreateThread(context.Background(), chID[0].ID, "other")
	msgID := triggerMessage(t, s, other, "hi")
	if _, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestGetSessionMissingIsNotFound(t *testing.T) {
	s, _ := newSessionFixture(t)
	if _, err := s.GetSession(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err := s.MarkSessionRunning(context.Background(), id, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSession(context.Background(), id)
	if got.Status != SessionRunning || got.StartedAt == nil || *got.StartedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("after running: %+v", got)
	}
	replySeq, err := s.AppendMessage(context.Background(), thID, "probe", "agent", "assistant", "the reply")
	if err != nil {
		t.Fatal(err)
	}
	replyID, _ := s.MessageIDBySeq(context.Background(), thID, replySeq)
	if err := s.SetSessionReply(context.Background(), id, replyID); err != nil {
		t.Fatal(err)
	}
	code := int64(0)
	if err := s.FinishSession(context.Background(), id, SessionSucceeded, &code, nil, "2026-01-01T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSession(context.Background(), id)
	if got.Status != SessionSucceeded || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("after finish: %+v", got)
	}
	if got.ReplyMessageID == nil || *got.ReplyMessageID != replyID {
		t.Fatalf("reply id = %v", got.ReplyMessageID)
	}
	if got.Error != nil {
		t.Fatalf("error = %v", got.Error)
	}
}

func TestMarkSessionRunningRejectsNonQueuedRow(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err := s.MarkSessionRunning(context.Background(), id, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	code := int64(0)
	if err := s.FinishSession(context.Background(), id, SessionSucceeded, &code, nil, "2026-01-01T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSessionRunning(context.Background(), id, "2026-01-01T00:02:00Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkSessionRunning on a succeeded row = %v, want ErrNotFound", err)
	}
	got, _ := s.GetSession(context.Background(), id)
	if got.Status != SessionSucceeded {
		t.Fatalf("status = %q, want succeeded: a finished session must not be resurrected", got.Status)
	}
}

func TestSessionUpdatesRejectMissingRow(t *testing.T) {
	s, _ := newSessionFixture(t)
	if err := s.MarkSessionRunning(context.Background(), 9999, "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkSessionRunning: %v", err)
	}
	if err := s.SetSessionReply(context.Background(), 9999, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetSessionReply: %v", err)
	}
}

func TestFinishSessionStoresError(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	msg := "agent timed out"
	if err := s.FinishSession(context.Background(), id, SessionFailed, nil, &msg, "2026-01-01T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSession(context.Background(), id)
	if got.Status != SessionFailed || got.Error == nil || *got.Error != msg {
		t.Fatalf("session = %+v", got)
	}
}

func TestFinishSessionRejectsNonTerminalStatus(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err := s.FinishSession(context.Background(), id, SessionRunning, nil, nil, "t"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestSessionEventsGetMonotonicSeq(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	none, err := s.ListSessionEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if none == nil {
		t.Fatal("empty event list must be an empty slice, not nil")
	}
	types := []string{SessionEventPrompt, SessionEventStdout, SessionEventStdout, SessionEventExit}
	for i, typ := range types {
		seq, _, err := s.AppendSessionEvent(context.Background(), id, typ, "chunk")
		if err != nil {
			t.Fatal(err)
		}
		if seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, seq, i+1)
		}
	}
	events, err := s.ListSessionEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(types) {
		t.Fatalf("events = %+v", events)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) || e.Type != types[i] || e.SessionID != id {
			t.Fatalf("event %d = %+v", i, e)
		}
		if e.CreatedAt == "" {
			t.Fatalf("event %d has no CreatedAt", i)
		}
	}
}

func TestAppendSessionEventRejectsBadTypeAndSession(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if _, _, err := s.AppendSessionEvent(context.Background(), id, "note", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad type: err = %v, want ErrInvalid", err)
	}
	if _, _, err := s.AppendSessionEvent(context.Background(), 9999, SessionEventStdout, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session: err = %v, want ErrNotFound", err)
	}
}

func TestListSessionsIsOldestFirstAndNeverNil(t *testing.T) {
	s, thID := newSessionFixture(t)
	empty, err := s.ListSessions(context.Background(), thID)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil {
		t.Fatal("empty result must be an empty slice, not nil")
	}
	msgID := triggerMessage(t, s, thID, "@a hi")
	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.CreateSession(context.Background(), thID, msgID, name, SessionQueued, "auto", "c", nil); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListSessions(context.Background(), thID)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if list[i].AgentName != want {
			t.Fatalf("session %d = %q, want %q", i, list[i].AgentName, want)
		}
	}
}

func TestListSessionsIsScopedToItsThread(t *testing.T) {
	s, thID := newSessionFixture(t)
	chID, err := s.ListChannels(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateThread(context.Background(), chID[0].ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.CreateThread(context.Background(), chID[0].ID, "empty")
	if err != nil {
		t.Fatal(err)
	}

	// The same agent name and status in both threads, so the only thing that
	// separates the rows is thread_id: a list that merely contains a thread's own
	// session passes against a query with no filter at all.
	create := func(threadID int64, names ...string) []int64 {
		t.Helper()
		msgID := triggerMessage(t, s, threadID, "@probe hi")
		ids := make([]int64, 0, len(names))
		for _, name := range names {
			id, err := s.CreateSession(context.Background(), threadID, msgID, name, SessionQueued, "auto", "c", nil)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	wantOwn := create(thID, "a1", "a2")
	wantOther := create(other, "a1", "a2")

	assertScoped := func(threadID int64, want []int64) {
		t.Helper()
		list, err := s.ListSessions(context.Background(), threadID)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]int64, 0, len(list))
		for i, sess := range list {
			if sess.ThreadID != threadID {
				t.Fatalf("ListSessions(%d)[%d] = session %d on thread %d", threadID, i, sess.ID, sess.ThreadID)
			}
			got = append(got, sess.ID)
		}
		if len(got) != len(want) {
			t.Fatalf("ListSessions(%d) = %v, want %v: sessions from other threads leaked in", threadID, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ListSessions(%d) = %v, want %v", threadID, got, want)
			}
		}
	}
	assertScoped(thID, wantOwn)
	assertScoped(other, wantOther)

	none, err := s.ListSessions(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	if none == nil {
		t.Fatal("empty result must be an empty slice, not nil")
	}
	if len(none) != 0 {
		t.Fatalf("ListSessions(%d) = %+v, want no sessions", empty, none)
	}
}

func TestMarkSessionRunningUnknownIDIsNotFound(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.MarkSessionRunning(context.Background(), 9999, "2026-01-01T00:00:00Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestMarkSessionRunningEmptyStartedAtStoresEmptyString(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSessionRunning(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != SessionRunning {
		t.Fatalf("status = %q, want running", got.Status)
	}
	if got.StartedAt == nil {
		t.Fatal("started_at is NULL, want the empty string: a zero timestamp must store '' and not SQL NULL")
	}
	if *got.StartedAt != "" {
		t.Fatalf("started_at = %q, want %q", *got.StartedAt, "")
	}
}

func TestSetSessionReplyZeroStoresZeroNotNull(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO messages(id, thread_id, seq, name, author_type, role, content) VALUES (0, ?, 2, 'human', 'human', 'user', 'seed')`, thID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionReply(context.Background(), id, 0); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReplyMessageID == nil {
		t.Fatal("reply_message_id is NULL, want 0: a zero reply id must store 0 and not SQL NULL")
	}
	if *got.ReplyMessageID != 0 {
		t.Fatalf("reply_message_id = %d, want 0", *got.ReplyMessageID)
	}
}

func TestSetSessionReplyRejectsMissingMessageTarget(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionReply(context.Background(), id, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing reply target error = %v, want ErrNotFound", err)
	}
	got, err := s.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReplyMessageID != nil {
		t.Fatalf("reply_message_id = %d, want NULL: a rejected reply target must not be stored", *got.ReplyMessageID)
	}
}

func TestCountAgentMessagesAfter(t *testing.T) {
	s, thID := newSessionFixture(t)
	humanSeq, _ := s.AppendMessage(context.Background(), thID, "alice", "human", "user", "human words")
	agentSeq, _ := s.AppendMessage(context.Background(), thID, "probe", "agent", "assistant", "agent words")
	if _, err := s.AppendMessage(context.Background(), thID, "probe", "human", "user", "human words"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(context.Background(), thID, "other", "agent", "assistant", "other agent words"); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountAgentMessagesAfter(context.Background(), thID, "probe", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count = %d, want 1: a human post by the agent's name and another agent's post must not count", n)
	}
	n, err = s.CountAgentMessagesAfter(context.Background(), thID, "probe", humanSeq)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count after the trigger = %d, want 1", n)
	}
	n, err = s.CountAgentMessagesAfter(context.Background(), thID, "probe", agentSeq)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("count after the agent post = %d, want 0: the bound is exclusive", n)
	}
}

func TestThreadContextAndMessageByID(t *testing.T) {
	s, thID := newSessionFixture(t)
	seq, _ := s.AppendMessage(context.Background(), thID, "alice", "human", "user", "hello")
	msgID, _ := s.MessageIDBySeq(context.Background(), thID, seq)
	tc, err := s.ThreadContext(context.Background(), thID)
	if err != nil {
		t.Fatal(err)
	}
	if tc.ThreadID != thID || tc.ThreadTitle != "t" || tc.ChannelName != "c" {
		t.Fatalf("ctx = %+v", tc)
	}
	if tc.RepoAbsPath == nil || *tc.RepoAbsPath != "/repo" {
		t.Fatalf("repo = %v", tc.RepoAbsPath)
	}
	m, err := s.MessageByID(context.Background(), msgID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Content != "hello" || m.Name != "alice" {
		t.Fatalf("message = %+v", m)
	}
	if _, err := s.MessageByID(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.ThreadContext(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ThreadContext: err = %v, want ErrNotFound", err)
	}
}

func TestThreadContextOrphanHasNoRepo(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chID, _ := s.CreateChannel(context.Background(), "orphan", "", "", "", "", true)
	thID, _ := s.CreateThread(context.Background(), chID, "t")
	tc, err := s.ThreadContext(context.Background(), thID)
	if err != nil {
		t.Fatal(err)
	}
	if tc.RepoAbsPath != nil {
		t.Fatalf("repo = %v, want nil", *tc.RepoAbsPath)
	}
}

func TestReconcileSessionsTerminatesOnlyNonTerminal(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@a hi")
	wasRunning, _ := s.CreateSession(context.Background(), thID, msgID, "a", SessionQueued, "auto", "c", nil)
	if err := s.MarkSessionRunning(context.Background(), wasRunning, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	msg2 := triggerMessage(t, s, thID, "@b hi")
	wasQueued, _ := s.CreateSession(context.Background(), thID, msg2, "b", SessionQueued, "auto", "c", nil)
	done, _ := s.CreateSession(context.Background(), thID, msgID, "done", SessionSucceeded, "auto", "c", nil)

	n, err := s.ReconcileSessions(context.Background(), "2026-01-01T00:10:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("reconciled = %d, want 2", n)
	}
	for _, want := range []struct {
		id      int64
		wantErr string
	}{
		{wasRunning, "daemon restarted while running"},
		{wasQueued, "daemon restarted while queued"},
	} {
		got, _ := s.GetSession(context.Background(), want.id)
		if got.Status != SessionCanceled {
			t.Fatalf("session %d status = %q", want.id, got.Status)
		}
		if got.FinishedAt == nil || *got.FinishedAt != "2026-01-01T00:10:00Z" {
			t.Fatalf("session %d finished_at = %v", want.id, got.FinishedAt)
		}
		if got.Error == nil {
			t.Fatalf("session %d has no error note, want %q", want.id, want.wantErr)
		}
		if *got.Error != want.wantErr {
			t.Fatalf("session %d error = %q, want %q", want.id, *got.Error, want.wantErr)
		}
	}
	untouched, _ := s.GetSession(context.Background(), done)
	if untouched.Status != SessionSucceeded {
		t.Fatalf("terminal session was modified: %q", untouched.Status)
	}
}

func TestReconcileSessionsEmptyFinishedAtStoresEmptyString(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, err := s.CreateSession(context.Background(), thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ReconcileSessions(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reconciled = %d, want 1", n)
	}
	got, err := s.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != SessionCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at is NULL, want the empty string: a zero timestamp must store '' and not SQL NULL")
	}
	if *got.FinishedAt != "" {
		t.Fatalf("finished_at = %q, want %q", *got.FinishedAt, "")
	}
}
