package store

import "testing"

func TestCreateAndListAnchoredChannel(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.CreateChannel("auth-refactor", "/tmp/proj", "git@x:y.git", "abc123", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected nonzero id")
	}
	got, err := s.ListChannels("/tmp/proj", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "auth-refactor" {
		t.Fatalf("got %+v", got)
	}
}

func TestArbitraryOrphanChannel(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	if _, err := s.CreateChannel("scratch", "", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListChannels("", false)
	if len(got) != 0 {
		t.Fatalf("orphans must hide without flag: %+v", got)
	}
	got, _ = s.ListChannels("", true)
	if len(got) != 1 {
		t.Fatalf("orphans must show with flag: %+v", got)
	}
}

func TestAppendAssignsSeqInOrder(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	ch, _ := s.CreateChannel("c", "/r", "", "", "", false)
	th, err := s.CreateThread(ch, "Schema migration")
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := s.AppendMessage(th, "alice", "human", "user", "first")
	s2, _ := s.AppendMessage(th, "pi-agent", "agent", "assistant", "second")
	if s1 != 1 || s2 != 2 {
		t.Fatalf("seqs %d %d", s1, s2)
	}
	msgs, _ := s.ListMessages(th, 1)
	if len(msgs) != 1 || msgs[0].Content != "second" {
		t.Fatalf("%+v", msgs)
	}
}

func TestReactionUniquePerAuthorEmoji(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	ch, _ := s.CreateChannel("c", "/r", "", "", "", false)
	th, _ := s.CreateThread(ch, "t")
	s.AppendMessage(th, "a", "human", "user", "hi")
	msgs, _ := s.ListMessages(th, 10)
	if err := s.AddReaction(msgs[0].ID, "👀", "bob", "human"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddReaction(msgs[0].ID, "👀", "bob", "human"); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestAppendMessageAtPreservesTimestamp(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	ch, _ := s.CreateChannel("c", "/r", "", "", "", false)
	th, _ := s.CreateThread(ch, "t")
	if _, err := s.AppendMessageAt(th, "a", "human", "user", "hi", "2020-01-02T03:04:05Z"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.ListMessages(th, 0)
	if len(msgs) != 1 {
		t.Fatalf("%+v", msgs)
	}
	if len(msgs[0].CreatedAt) < 10 || msgs[0].CreatedAt[:10] != "2020-01-02" {
		t.Fatalf("created_at %q", msgs[0].CreatedAt)
	}
}

func TestListInbox(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ch1, _ := s.CreateChannel("general", "", "", "", "", true)
	ch2, _ := s.CreateChannel("random", "", "", "", "", true)
	th1, _ := s.CreateThread(ch1, "hello")
	th2, _ := s.CreateThread(ch2, "world")
	_, _ = s.AppendMessageAt(th1, "alice", "human", "user", "first", "2026-09-23T10:00:00Z")
	_, _ = s.AppendMessageAt(th2, "bob", "human", "user", "second", "2026-09-23T11:00:00Z")
	_, _ = s.AppendMessageAt(th1, "alice", "human", "user", "third", "2026-09-23T12:00:00Z")
	msgs, err := s.ListInbox(10)
	if err != nil {
		t.Fatalf("ListInbox: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 got %d", len(msgs))
	}
	if msgs[0].Content != "first" || msgs[1].Content != "second" || msgs[2].Content != "third" {
		t.Fatalf("order wrong: %+v", msgs)
	}
	if msgs[0].ChannelName != "general" || msgs[0].ThreadTitle != "hello" {
		t.Fatalf("enrichment wrong: %+v", msgs[0])
	}
	msgs2, _ := s.ListInbox(1)
	if len(msgs2) != 1 {
		t.Fatalf("limit 1: got %d", len(msgs2))
	}
	if msgs2[0].Content != "third" {
		t.Fatalf("limit should return newest last when reversed to ASC, got %q", msgs2[0].Content)
	}
	chArchived, _ := s.CreateChannel("archived", "", "", "", "", true)
	thArchived, _ := s.CreateThread(chArchived, "archived-th")
	_, _ = s.AppendMessageAt(thArchived, "alice", "human", "user", "archived-msg", "2026-09-23T13:00:00Z")
	if _, err := s.db.Exec(`UPDATE channels SET archived_at = ? WHERE id = ?`, "2026-09-23T13:01:00Z", chArchived); err != nil {
		t.Fatal(err)
	}
	msgs3, _ := s.ListInbox(10)
	for _, m := range msgs3 {
		if m.Content == "archived-msg" {
			t.Fatalf("archived channel message should be excluded: %+v", msgs3)
		}
	}
	if len(msgs3) != 3 {
		t.Fatalf("archived channel: want 3 got %d %+v", len(msgs3), msgs3)
	}
	chOk, _ := s.CreateChannel("ok-arch-test", "", "", "", "", true)
	thKeep, _ := s.CreateThread(chOk, "keep")
	_, _ = s.AppendMessageAt(thKeep, "alice", "human", "user", "keep-msg", "2026-09-23T13:02:00Z")
	thGone, _ := s.CreateThread(chOk, "gone")
	_, _ = s.AppendMessageAt(thGone, "alice", "human", "user", "gone-msg", "2026-09-23T13:03:00Z")
	if _, err := s.db.Exec(`UPDATE threads SET archived_at = ? WHERE id = ?`, "2026-09-23T13:04:00Z", thGone); err != nil {
		t.Fatal(err)
	}
	msgs4, _ := s.ListInbox(10)
	for _, m := range msgs4 {
		if m.Content == "gone-msg" {
			t.Fatalf("archived thread message should be excluded: %+v", msgs4)
		}
	}
	found := false
	for _, m := range msgs4 {
		if m.Content == "keep-msg" {
			found = true
		}
	}
	if !found {
		t.Fatalf("keep-msg should remain: %+v", msgs4)
	}
}
