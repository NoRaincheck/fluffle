package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

func toModel(v tea.Model) model {
	switch x := v.(type) {
	case model:
		return x
	case *model:
		return *x
	default:
		panic("unknown model type")
	}
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func keyType(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: t}
}

func TestSimpleFlow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = toModel(nm)
	if m.width != 80 || m.height != 24 {
		t.Fatalf("size not set")
	}
	channels := []store.Channel{{ID: 1, Name: "demo", IsOrphaned: true}, {ID: 2, Name: "second", IsOrphaned: true}}
	nm, _ = m.Update(channelsFetchedMsg{channels: channels})
	m = toModel(nm)
	if len(m.channels) != 2 {
		t.Fatalf("want 2 channels")
	}
	if m.cursor != 0 {
		t.Fatalf("cursor 0")
	}
	nm, _ = m.Update(keyType(tea.KeyDown))
	m = toModel(nm)
	if m.cursor != 1 {
		t.Fatalf("want 1 got %d", m.cursor)
	}
	nm, _ = m.Update(keyType(tea.KeyEnter))
	m = toModel(nm)
	threads := []store.Thread{{ID: 10, ChannelID: 2, Title: "hello"}}
	nm, _ = m.Update(threadsFetchedMsg{channelID: 2, threads: threads})
	m = toModel(nm)
	if m.view != viewThreads {
		t.Fatalf("want threads view")
	}
	if len(m.threads) != 1 {
		t.Fatalf("want 1 thread")
	}
	nm, _ = m.Update(keyType(tea.KeyEnter))
	m = toModel(nm)
	msgs := []store.Message{{ID: 1, ThreadID: 10, Seq: 1, Author: "alice", AuthorType: "human", Role: "user", Content: "hi", CreatedAt: "2026-09-23T00:00:00Z"}}
	nm, _ = m.Update(messagesFetchedMsg{threadID: 10, messages: msgs})
	m = toModel(nm)
	if m.view != viewMessages {
		t.Fatalf("want messages view")
	}
	if len(m.messages) != 1 {
		t.Fatalf("want 1 msg")
	}
	nm, _ = m.Update(keyType(tea.KeyEsc))
	m = toModel(nm)
	if m.view != viewThreads {
		t.Fatalf("want back to threads")
	}
	nm, _ = m.Update(keyType(tea.KeyEsc))
	m = toModel(nm)
	if m.view != viewChannels {
		t.Fatalf("want back to channels")
	}
	m.cursor = 0
	nm, _ = m.Update(keyRunes("n"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("want compose active for new thread")
	}
	view := m.View()
	_ = view
}

func TestInboxFlow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	inbox := []store.InboxMessage{
		{Message: store.Message{ID: 1, ThreadID: 10, Seq: 1, Author: "alice", AuthorType: "human", Content: "first line\nsecond", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello", ChannelID: 1},
		{Message: store.Message{ID: 2, ThreadID: 11, Seq: 1, Author: "bob", AuthorType: "human", Content: "second", CreatedAt: "2026-09-23T11:00:00Z"}, ChannelName: "random", ThreadTitle: "world", ChannelID: 2},
	}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	if len(m.inbox) != 2 {
		t.Fatalf("inbox len")
	}
	if m.cursor != 0 {
		t.Fatalf("cursor")
	}
	nm, _ = m.Update(keyRunes("j"))
	m = toModel(nm)
	if m.cursor != 1 {
		t.Fatalf("want 1 got %d", m.cursor)
	}
	nm, _ = m.Update(keyRunes("r"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("compose")
	}
	if m.compose.state.threadID != 11 {
		t.Fatalf("threadID %d", m.compose.state.threadID)
	}
}

func TestAdaptivePreviewTruncation(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	long := strings.Repeat("line\n", 20)
	inbox := []store.InboxMessage{{Message: store.Message{ID: 1, ThreadID: 10, Content: long, Author: "alice", AuthorType: "human", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello"}}
	previewMsgs := make([]store.Message, 10)
	for i := 0; i < 10; i++ {
		previewMsgs[i] = store.Message{ID: int64(2 + i), ThreadID: 10, Seq: int64(2 + i), Author: "bob", Content: fmt.Sprintf("reply %d", i), CreatedAt: "2026-09-23T11:00:00Z"}
	}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	nm, _ = m.Update(previewMessagesFetchedMsg{threadID: 10, messages: previewMsgs})
	m = toModel(nm)
	m.preview = true
	m.width = 120
	m.height = 24
	m.cursor = 0
	rendered := m.renderPreview(50, 20)
	if !strings.Contains(rendered, "(+") {
		t.Fatalf("expected truncation marker, got %q", rendered)
	}
	if strings.Count(rendered, "reply") > 5 {
		t.Fatalf("should show at most 5 replies")
	}
	tbl := m.renderInboxWithWidth(100, 20)
	if !strings.Contains(tbl, "CHANNEL") || !strings.Contains(tbl, "CONTENT") {
		t.Fatalf("header missing %q", tbl)
	}
}

func TestInboxKeybindings(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	inbox := []store.InboxMessage{{Message: store.Message{ID: 1, ThreadID: 10, Content: "hi"}, ChannelName: "general", ThreadTitle: "hello", ChannelID: 5}}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	nm, _ = m.Update(keyType(tea.KeyEnter))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("Enter should open reply")
	}
	m.compose.Close()
	nm, _ = m.Update(keyRunes("c"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("c should open compose")
	}
	m.compose.Close()
	nm, _ = m.Update(keyRunes("r"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("r should open compose")
	}
	m.compose.Close()
	had := m.preview
	nm, _ = m.Update(keyRunes("L"))
	m = toModel(nm)
	if m.preview == had {
		t.Fatalf("L toggle failed")
	}
	nm, _ = m.Update(keyType(tea.KeyEsc))
	m = toModel(nm)
	if m.view != viewInbox {
		t.Fatalf("Esc should be no-op in inbox")
	}
}
