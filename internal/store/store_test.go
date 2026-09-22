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
