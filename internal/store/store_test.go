package store

import "testing"

func TestCreateAndListAnchoredChannel(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.CreateChannel("auth-refactor", "/tmp/proj", "git@x:y.git", "abc123", false)
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
	if _, err := s.CreateChannel("scratch", "", "", "", true); err != nil {
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
    ch, _ := s.CreateChannel("c", "/r", "", "", false)
    th, err := s.CreateThread(ch, "Schema migration")
    if err != nil { t.Fatal(err) }
    s1, _ := s.AppendMessage(th, "alice", "human", "user", "first")
    s2, _ := s.AppendMessage(th, "pi-agent", "agent", "assistant", "second")
    if s1 != 1 || s2 != 2 { t.Fatalf("seqs %d %d", s1, s2) }
    msgs, _ := s.ListMessages(th, 1)
    if len(msgs) != 1 || msgs[0].Content != "second" { t.Fatalf("%+v", msgs) }
}

func TestReactionUniquePerAuthorEmoji(t *testing.T) {
    s, _ := Open(":memory:")
    defer s.Close()
    ch, _ := s.CreateChannel("c", "/r", "", "", false)
    th, _ := s.CreateThread(ch, "t")
    s.AppendMessage(th, "a", "human", "user", "hi")
    msgs, _ := s.ListMessages(th, 10)
    if err := s.AddReaction(msgs[0].ID, "👀", "bob", "human"); err != nil { t.Fatal(err) }
    if err := s.AddReaction(msgs[0].ID, "👀", "bob", "human"); err == nil { t.Fatal("expected duplicate error") }
}
