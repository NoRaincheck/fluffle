package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

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

func TestListMessagesAfterUsesExclusiveCursor(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()
	for _, content := range []string{"first", "second", "third"} {
		if _, err := s.AppendMessage(threadID, "alice", "human", "user", content); err != nil {
			t.Fatal(err)
		}
	}

	messages, err := s.ListMessagesAfter(threadID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0].Seq != 2 || messages[0].Content != "second" {
		t.Fatalf("first cursor result = %+v", messages[0])
	}
	if messages[1].Seq != 3 || messages[1].Content != "third" {
		t.Fatalf("second cursor result = %+v", messages[1])
	}
}

func TestListMessagesAfterReturnsEmptyAtCurrentCursor(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()
	if _, err := s.AppendMessage(threadID, "alice", "human", "user", "only"); err != nil {
		t.Fatal(err)
	}

	messages, err := s.ListMessagesAfter(threadID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("got %d messages, want none", len(messages))
	}
}

func TestFileStorePersistsParentSequenceProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, _, threadID := newStoreWithThread(t, path)
	rootSeq, err := s.AppendMessage(threadID, "alice", "human", "user", "root")
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := s.MessageIDBySeq(threadID, rootSeq)
	if err != nil {
		t.Fatal(err)
	}
	childSeq, childID, err := s.AppendMessageByParentSeq(threadID, rootSeq, "pi-agent", "agent", "assistant", "reply", "2026-09-25T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if childSeq != 2 || childID == 0 || childID == rootID {
		t.Fatalf("child seq/id = %d/%d, root id = %d", childSeq, childID, rootID)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	messages, err := reopened.ListMessagesAfter(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0].ParentSeq.Valid {
		t.Fatalf("root parent seq = %+v, want null", messages[0].ParentSeq)
	}
	if !messages[1].ParentSeq.Valid || messages[1].ParentSeq.Int64 != rootSeq {
		t.Fatalf("child parent seq = %+v, want %d", messages[1].ParentSeq, rootSeq)
	}
	if messages[1].ParentIDValue() != rootID {
		t.Fatalf("child parent id = %d, want %d", messages[1].ParentIDValue(), rootID)
	}

	encoded, err := json.Marshal(messages[1])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["ParentSeq"]; ok {
		t.Fatalf("internal ParentSeq leaked into JSON: %s", encoded)
	}
	if _, ok := fields["ParentID"]; !ok {
		t.Fatalf("existing ParentID missing from JSON: %s", encoded)
	}
}

func TestMessageIDBySeqIsThreadScoped(t *testing.T) {
	s, channelID, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()
	otherThreadID, err := s.CreateThread(channelID, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(otherThreadID, "alice", "human", "user", "other"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.MessageIDBySeq(threadID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-thread sequence error = %v, want ErrNotFound", err)
	}
	otherID, err := s.MessageIDBySeq(otherThreadID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if otherID == 0 {
		t.Fatal("expected nonzero message id")
	}
}

func TestAppendMessageByParentSeqRejectsNegativeSequence(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()

	if _, _, err := s.AppendMessageByParentSeq(threadID, -1, "pi-agent", "agent", "assistant", "reply", ""); err == nil {
		t.Fatal("expected negative parent sequence error")
	}
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("invalid parent sequence created messages: %+v", messages)
	}
}

func TestAddReactionBySeqValidatesThreadTarget(t *testing.T) {
	s, channelID, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()
	otherThreadID, err := s.CreateThread(channelID, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(otherThreadID, "alice", "human", "user", "other"); err != nil {
		t.Fatal(err)
	}

	if err := s.AddReactionBySeq(threadID, 1, "+1", "bob", "human"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-thread reaction error = %v, want ErrNotFound", err)
	}
	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 0 {
		t.Fatalf("invalid target created reactions: %+v", reactions)
	}

	if _, err := s.AppendMessage(threadID, "alice", "human", "user", "target"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddReactionBySeq(threadID, 1, "+1", "bob", "human"); err != nil {
		t.Fatal(err)
	}
}

func TestListReactionsReturnsThreadFieldsInMessageOrder(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()
	firstSeq, err := s.AppendMessage(threadID, "alice", "human", "user", "first")
	if err != nil {
		t.Fatal(err)
	}
	secondSeq, err := s.AppendMessage(threadID, "pi-agent", "agent", "assistant", "second")
	if err != nil {
		t.Fatal(err)
	}
	for _, reaction := range []struct {
		seq        int64
		emoji      string
		name       string
		authorType string
	}{
		{secondSeq, "👀", "reviewer", "agent"},
		{secondSeq, "👍", "alice", "human"},
		{firstSeq, "❤️", "bob", "human"},
	} {
		if err := s.AddReactionBySeq(threadID, reaction.seq, reaction.emoji, reaction.name, reaction.authorType); err != nil {
			t.Fatal(err)
		}
	}

	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 3 {
		t.Fatalf("got %d reactions, want 3", len(reactions))
	}
	want := []struct {
		seq        int64
		emoji      string
		name       string
		authorType string
	}{
		{firstSeq, "❤️", "bob", "human"},
		{secondSeq, "👀", "reviewer", "agent"},
		{secondSeq, "👍", "alice", "human"},
	}
	for i, expected := range want {
		got := reactions[i]
		if got.MessageSeq != expected.seq || got.Emoji != expected.emoji || got.Name != expected.name || got.AuthorType != expected.authorType {
			t.Fatalf("reaction %d = %+v, want %+v", i, got, expected)
		}
		if got.ID == 0 || got.MessageID == 0 || got.CreatedAt == "" {
			t.Fatalf("reaction %d missing fields: %+v", i, got)
		}
	}
}

func TestAppendBatchRemapsSourceSequencesAndReferences(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()
	existingSeq, err := s.AppendMessage(threadID, "alice", "human", "user", "existing")
	if err != nil {
		t.Fatal(err)
	}
	existingID, err := s.MessageIDBySeq(threadID, existingSeq)
	if err != nil {
		t.Fatal(err)
	}
	events := []AppendEvent{
		{Type: "message", SourceSeq: 10, Name: "alice", AuthorType: "human", Role: "user", Content: "imported root", CreatedAt: "2026-09-25T10:00:00Z"},
		{Type: "message", SourceSeq: 20, ParentSeq: 10, Name: "pi-agent", AuthorType: "agent", Role: "assistant", Content: "imported reply", CreatedAt: "2026-09-25T10:01:00Z"},
		{Type: "message", SourceSeq: 30, ParentSeq: 1, Name: "alice", AuthorType: "human", Role: "user", Content: "existing parent", CreatedAt: "2026-09-25T10:02:00Z"},
		{Type: "reaction", SourceSeq: 40, MessageSeq: 20, Name: "bob", AuthorType: "human", Emoji: "+1", CreatedAt: "2026-09-25T10:03:00Z"},
		{Type: "reaction", SourceSeq: 50, MessageSeq: 1, Name: "carol", AuthorType: "human", Emoji: "👀", CreatedAt: "2026-09-25T10:04:00Z"},
	}

	results, err := s.AppendBatch(threadID, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(events) {
		t.Fatalf("got %d results, want %d", len(results), len(events))
	}
	wantMessageSeqs := []int64{2, 3, 4}
	for i, wantSeq := range wantMessageSeqs {
		if results[i].SourceSeq != []int64{10, 20, 30}[i] || results[i].Seq != wantSeq {
			t.Fatalf("message result %d = %+v, want source %d and seq %d", i, results[i], []int64{10, 20, 30}[i], wantSeq)
		}
		if results[i].MessageID == 0 || results[i].ReactionID != 0 {
			t.Fatalf("message result %d = %+v", i, results[i])
		}
	}
	if results[3].SourceSeq != 40 || results[3].Seq != 0 || results[3].MessageID != results[1].MessageID || results[3].ReactionID == 0 {
		t.Fatalf("mapped reaction result = %+v", results[3])
	}
	if results[4].SourceSeq != 50 || results[4].Seq != 0 || results[4].MessageID != existingID || results[4].ReactionID == 0 {
		t.Fatalf("existing reaction result = %+v", results[4])
	}

	messages, err := s.ListMessagesAfter(threadID, existingSeq)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("got %d imported messages, want 3", len(messages))
	}
	if messages[0].ParentSeq.Valid {
		t.Fatalf("imported root parent seq = %+v, want null", messages[0].ParentSeq)
	}
	if !messages[1].ParentSeq.Valid || messages[1].ParentSeq.Int64 != 2 || messages[1].ParentIDValue() != results[0].MessageID {
		t.Fatalf("mapped parent = %+v, parent id %d", messages[1].ParentSeq, messages[1].ParentIDValue())
	}
	if !messages[2].ParentSeq.Valid || messages[2].ParentSeq.Int64 != existingSeq || messages[2].ParentIDValue() != existingID {
		t.Fatalf("existing parent = %+v, parent id %d", messages[2].ParentSeq, messages[2].ParentIDValue())
	}

	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 2 {
		t.Fatalf("got %d reactions, want 2", len(reactions))
	}
	if reactions[0].ID != results[4].ReactionID || reactions[0].MessageSeq != existingSeq || reactions[0].CreatedAt != "2026-09-25T10:04:00Z" {
		t.Fatalf("existing reaction = %+v", reactions[0])
	}
	if reactions[1].ID != results[3].ReactionID || reactions[1].MessageSeq != 3 || reactions[1].CreatedAt != "2026-09-25T10:03:00Z" {
		t.Fatalf("mapped reaction = %+v", reactions[1])
	}
}

func TestAppendBatchRollsBackLateInvalidReference(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()
	events := []AppendEvent{
		{Type: "message", SourceSeq: 1, Name: "alice", AuthorType: "human", Role: "user", Content: "must roll back"},
		{Type: "reaction", SourceSeq: 2, MessageSeq: 999, Name: "bob", AuthorType: "human", Emoji: "+1"},
	}

	if _, err := s.AppendBatch(threadID, events); err == nil {
		t.Fatal("expected missing reaction target error")
	}
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("failed batch left messages: %+v", messages)
	}
	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 0 {
		t.Fatalf("failed batch left reactions: %+v", reactions)
	}
}

func TestAppendBatchRejectsInvalidEvents(t *testing.T) {
	tests := []struct {
		name  string
		event AppendEvent
	}{
		{name: "unknown type", event: AppendEvent{Type: "thread", SourceSeq: 1, Name: "alice", AuthorType: "human", Role: "user", Content: "content"}},
		{name: "negative source sequence", event: AppendEvent{Type: "message", SourceSeq: -1, Name: "alice", AuthorType: "human", Role: "user", Content: "content"}},
		{name: "negative parent sequence", event: AppendEvent{Type: "message", SourceSeq: 1, ParentSeq: -1, Name: "alice", AuthorType: "human", Role: "user", Content: "content"}},
		{name: "message name", event: AppendEvent{Type: "message", SourceSeq: 1, AuthorType: "human", Role: "user", Content: "content"}},
		{name: "message content", event: AppendEvent{Type: "message", SourceSeq: 1, Name: "alice", AuthorType: "human", Role: "user"}},
		{name: "reaction target", event: AppendEvent{Type: "reaction", SourceSeq: 1, Name: "bob", AuthorType: "human", Emoji: "+1"}},
		{name: "reaction emoji", event: AppendEvent{Type: "reaction", SourceSeq: 1, MessageSeq: 1, Name: "bob", AuthorType: "human"}},
		{name: "reaction name", event: AppendEvent{Type: "reaction", SourceSeq: 1, MessageSeq: 1, AuthorType: "human", Emoji: "+1"}},
		{name: "reaction timestamp", event: AppendEvent{Type: "reaction", SourceSeq: 1, MessageSeq: 1, Name: "bob", AuthorType: "human", Emoji: "+1", CreatedAt: "not-a-timestamp"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, threadID := newStoreWithThread(t, ":memory:")
			defer s.Close()
			if _, err := s.AppendBatch(threadID, []AppendEvent{tt.event}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAppendBatchRejectsDuplicateSourceSequences(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	defer s.Close()
	events := []AppendEvent{
		{Type: "message", SourceSeq: 7, Name: "alice", AuthorType: "human", Role: "user", Content: "first"},
		{Type: "message", SourceSeq: 7, Name: "alice", AuthorType: "human", Role: "user", Content: "second"},
	}

	if _, err := s.AppendBatch(threadID, events); err == nil {
		t.Fatal("expected duplicate source sequence error")
	}
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("invalid batch left messages: %+v", messages)
	}
}

func newStoreWithThread(t *testing.T, path string) (*Store, int64, int64) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	channelID, err := s.CreateChannel("c", "/r", "", "", "", false)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	threadID, err := s.CreateThread(channelID, "t")
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, channelID, threadID
}
