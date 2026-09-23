package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/NoRaincheck/fluffle/internal/store"
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
	// Down
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = toModel(nm)
	if m.cursor != 1 {
		t.Fatalf("want 1 got %d", m.cursor)
	}
	// Enter channel 2 -> should fetch threads
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	// Enter thread
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	// Esc back
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = toModel(nm)
	if m.view != viewThreads {
		t.Fatalf("want back to threads")
	}
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = toModel(nm)
	if m.view != viewChannels {
		t.Fatalf("want back to channels")
	}
	// n new thread
	m.cursor = 0
	nm, _ = m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("want compose active for new thread")
	}
	view := m.View()
	_ = view
}
