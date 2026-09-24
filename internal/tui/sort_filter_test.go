package tui

import (
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

func TestInboxSortV(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	inbox := []store.InboxMessage{
		{Message: store.Message{ID: 1, ThreadID: 10, Content: "first", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "b-channel", ThreadTitle: "z-thread", ChannelID: 2},
		{Message: store.Message{ID: 2, ThreadID: 11, Content: "second", CreatedAt: "2026-09-23T09:00:00Z"}, ChannelName: "a-channel", ThreadTitle: "a-thread", ChannelID: 1},
		{Message: store.Message{ID: 3, ThreadID: 12, Content: "third", CreatedAt: "2026-09-23T11:00:00Z"}, ChannelName: "a-channel", ThreadTitle: "b-thread", ChannelID: 1},
	}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	// default sort is latest-desc: third (11), first (10), second (09)
	filtered := m.inboxFilteredSorted()
	if len(filtered) != 3 {
		t.Fatalf("len")
	}
	if filtered[0].Content != "third" || filtered[1].Content != "first" || filtered[2].Content != "second" {
		t.Fatalf("latest-desc sort failed: got %v %v %v", filtered[0].Content, filtered[1].Content, filtered[2].Content)
	}
	// press v to cycle to latest-asc
	nm, _ = m.Update(keyRunes("v"))
	m = toModel(nm)
	if m.inboxSort != inboxSortLatestAsc {
		t.Fatalf("sort not cycled to latest-asc")
	}
	filtered = m.inboxFilteredSorted()
	// latest-asc: second (09), first (10), third (11)
	if filtered[0].Content != "second" || filtered[1].Content != "first" || filtered[2].Content != "third" {
		t.Fatalf("latest-asc sort failed: got %v %v %v", filtered[0].Content, filtered[1].Content, filtered[2].Content)
	}
	// cycle to channel/thread asc
	nm, _ = m.Update(keyRunes("v"))
	m = toModel(nm)
	if m.inboxSort != inboxSortChannelThreadAsc {
		t.Fatalf("sort not cycled to channel-thread asc")
	}
	filtered = m.inboxFilteredSorted()
	// channel/thread asc: a-channel/a-thread (second), a-channel/b-thread (third), b-channel/z-thread (first)
	if filtered[0].ChannelName != "a-channel" || filtered[0].ThreadTitle != "a-thread" {
		t.Fatalf("channel/thread asc first wrong %v", filtered[0])
	}
	if filtered[1].ThreadTitle != "b-thread" {
		t.Fatalf("second wrong %v", filtered[1])
	}
	if filtered[2].ChannelName != "b-channel" {
		t.Fatalf("third wrong %v", filtered[2])
	}
	// view should contain sorted order? Check render
	view := m.View()
	if !strings.Contains(view, "a-channel") {
		t.Fatalf("view missing")
	}
	// cycle to channel/thread asc time desc
	nm, _ = m.Update(keyRunes("v"))
	m = toModel(nm)
	if m.inboxSort != inboxSortChannelThreadAscTimeDesc {
		t.Fatalf("sort not cycled to channel-thread asc time desc")
	}
	filtered = m.inboxFilteredSorted()
	// channel A→Z, thread A→Z, time newest first: a-channel/a-thread (third 11:00), a-channel/b-thread (third 11:00), b-channel/z-thread (first 10:00)
	if filtered[0].ChannelName != "a-channel" || filtered[0].ThreadTitle != "a-thread" {
		t.Fatalf("channel-thread asc time desc first wrong %v", filtered[0])
	}
	if filtered[1].ChannelName != "a-channel" || filtered[1].ThreadTitle != "b-thread" {
		t.Fatalf("channel-thread asc time desc second wrong %v", filtered[1])
	}
	if filtered[2].ChannelName != "b-channel" {
		t.Fatalf("channel-thread asc time desc third wrong %v", filtered[2])
	}
	// cycle to channel/thread asc time asc (oldest first)
	nm, _ = m.Update(keyRunes("v"))
	m = toModel(nm)
	if m.inboxSort != inboxSortChannelThreadAscTimeAsc {
		t.Fatalf("sort not cycled to channel-thread asc time asc")
	}
	filtered = m.inboxFilteredSorted()
	// channel A→Z, thread A→Z, time oldest first: same grouping, same order for this data
	if filtered[0].ChannelName != "a-channel" || filtered[0].ThreadTitle != "a-thread" {
		t.Fatalf("channel-thread asc time asc first wrong %v", filtered[0])
	}
	if filtered[1].ChannelName != "a-channel" || filtered[1].ThreadTitle != "b-thread" {
		t.Fatalf("channel-thread asc time asc second wrong %v", filtered[1])
	}
	if filtered[2].ChannelName != "b-channel" {
		t.Fatalf("channel-thread asc time asc third wrong %v", filtered[2])
	}
	// cycle back to latest-desc
	nm, _ = m.Update(keyRunes("v"))
	m = toModel(nm)
	if m.inboxSort != inboxSortLatestDesc {
		t.Fatalf("cycle back failed")
	}
}

func TestInboxSortChannelThreadTimeOrdering(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	// Multiple messages in same thread to test time ordering
	inbox := []store.InboxMessage{
		{Message: store.Message{ID: 1, ThreadID: 1, Content: "old msg", CreatedAt: "2026-09-23T08:00:00Z"}, ChannelName: "general", ThreadTitle: "thread-a", ChannelID: 1},
		{Message: store.Message{ID: 2, ThreadID: 1, Content: "new msg", CreatedAt: "2026-09-23T12:00:00Z"}, ChannelName: "general", ThreadTitle: "thread-a", ChannelID: 1},
		{Message: store.Message{ID: 3, ThreadID: 2, Content: "mid msg", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "thread-b", ChannelID: 1},
	}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)

	// time desc: newest first within each thread
	nm, _ = m.Update(keyRunes("v")) // latest-asc
	m = toModel(nm)
	nm, _ = m.Update(keyRunes("v")) // channel-thread-asc
	m = toModel(nm)
	nm, _ = m.Update(keyRunes("v")) // channel-thread-asc time desc
	m = toModel(nm)
	filtered := m.inboxFilteredSorted()
	// thread-a has 2 msgs, thread-b has 1; within thread-a newest first = new msg, old msg
	if filtered[0].Content != "new msg" || filtered[0].ThreadTitle != "thread-a" {
		t.Fatalf("time desc: expected new msg first in thread-a, got %v", filtered[0])
	}
	if filtered[1].Content != "old msg" || filtered[1].ThreadTitle != "thread-a" {
		t.Fatalf("time desc: expected old msg second in thread-a, got %v", filtered[1])
	}
	if filtered[2].Content != "mid msg" || filtered[2].ThreadTitle != "thread-b" {
		t.Fatalf("time desc: expected mid msg in thread-b, got %v", filtered[2])
	}

	// time asc: oldest first within each thread
	nm, _ = m.Update(keyRunes("v")) // channel-thread-asc time asc
	m = toModel(nm)
	filtered = m.inboxFilteredSorted()
	if filtered[0].Content != "old msg" || filtered[0].ThreadTitle != "thread-a" {
		t.Fatalf("time asc: expected old msg first in thread-a, got %v", filtered[0])
	}
	if filtered[1].Content != "new msg" || filtered[1].ThreadTitle != "thread-a" {
		t.Fatalf("time asc: expected new msg second in thread-a, got %v", filtered[1])
	}
	if filtered[2].Content != "mid msg" || filtered[2].ThreadTitle != "thread-b" {
		t.Fatalf("time asc: expected mid msg in thread-b, got %v", filtered[2])
	}
}

func TestInboxFilterF(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	inbox := []store.InboxMessage{
		{Message: store.Message{ID: 1, ThreadID: 10, Content: "first", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello", ChannelID: 1},
		{Message: store.Message{ID: 2, ThreadID: 11, Content: "second", CreatedAt: "2026-09-23T11:00:00Z"}, ChannelName: "random", ThreadTitle: "world", ChannelID: 2},
		{Message: store.Message{ID: 3, ThreadID: 12, Content: "third", CreatedAt: "2026-09-23T12:00:00Z"}, ChannelName: "general", ThreadTitle: "world", ChannelID: 1},
	}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	// press f should open filter modal
	nm, _ = m.Update(keyRunes("f"))
	m = toModel(nm)
	if !m.filter.IsActive() {
		t.Fatalf("filter modal not active")
	}
	view := m.View()
	if !strings.Contains(view, "Filter:") {
		t.Fatalf("filter view missing %q", view)
	}
	// type "general" and enter
	for _, ch := range "general" {
		nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = toModel(nm)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = toModel(nm)
	// should have applied filter
	// Update handles filterAppliedMsg in next cycle, need to process it
	nm, _ = m.Update(filterAppliedMsg{text: "general"})
	m = toModel(nm)
	if m.inboxFilterChan != "general" {
		t.Fatalf("filter chan not set %q", m.inboxFilterChan)
	}
	filtered := m.inboxFilteredSorted()
	if len(filtered) != 2 {
		t.Fatalf("filtered len 2 expected got %d", len(filtered))
	}
	// filter with channel/thread
	nm, _ = m.Update(keyRunes("f"))
	m = toModel(nm)
	for _, ch := range "general/hello" {
		nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = toModel(nm)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = toModel(nm)
	nm, _ = m.Update(filterAppliedMsg{text: "general/hello"})
	m = toModel(nm)
	if m.inboxFilterChan != "general" || m.inboxFilterThread != "hello" {
		t.Fatalf("channel/thread filter failed %q %q", m.inboxFilterChan, m.inboxFilterThread)
	}
	filtered = m.inboxFilteredSorted()
	if len(filtered) != 1 || filtered[0].Content != "first" {
		t.Fatalf("filtered channel/thread wrong %v", filtered)
	}
	// clear filter with empty
	nm, _ = m.Update(filterAppliedMsg{text: ""})
	m = toModel(nm)
	if m.inboxFilterChan != "" || m.inboxFilterThread != "" {
		t.Fatalf("clear failed")
	}
	filtered = m.inboxFilteredSorted()
	if len(filtered) != 3 {
		t.Fatalf("clear should show all")
	}
	// test f via actual modal enter flow (without manual filterAppliedMsg)
	m2 := toModel(New("http://127.0.0.1:0"))
	nm, _ = m2.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m2 = toModel(nm)
	nm, _ = m2.Update(inboxFetchedMsg{inbox: inbox})
	m2 = toModel(nm)
	nm, _ = m2.Update(keyRunes("f"))
	m2 = toModel(nm)
	for _, ch := range "random" {
		nm, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m2 = toModel(nm)
	}
	// Enter should apply - capture Cmd and execute
	var cmd tea.Cmd
	nm, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 = toModel(nm)
	if m2.filter.IsActive() {
		t.Fatalf("filter should be closed after Enter")
	}
	if cmd != nil {
		msg := cmd()
		nm, _ = m2.Update(msg)
		m2 = toModel(nm)
	}
	if m2.inboxFilterChan != "random" {
		t.Fatalf("filter via modal Enter should apply, got %q", m2.inboxFilterChan)
	}
	filtered = m2.inboxFilteredSorted()
	if len(filtered) != 1 || filtered[0].ChannelName != "random" {
		t.Fatalf("filtered via modal wrong %v", filtered)
	}
}

func TestInboxFilterAndSortInteraction(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	inbox := []store.InboxMessage{
		{Message: store.Message{ID: 1, CreatedAt: "2026-09-23T10:00:00Z", Content: "a"}, ChannelName: "general", ThreadTitle: "z", ChannelID: 1},
		{Message: store.Message{ID: 2, CreatedAt: "2026-09-23T09:00:00Z", Content: "b"}, ChannelName: "general", ThreadTitle: "a", ChannelID: 1},
	}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	// sort by channel/thread asc should order by thread title a before z, both same channel, so b before a in time but after sort, a thread first
	nm, _ = m.Update(keyRunes("v"))
	m = toModel(nm)
	filtered := m.inboxFilteredSorted()
	if filtered[0].Content != "b" {
		t.Fatalf("sort channel/thread asc failed")
	}
	// now filter by thread "z" should show only a
	nm, _ = m.Update(filterAppliedMsg{text: "general/z"})
	m = toModel(nm)
	filtered = m.inboxFilteredSorted()
	if len(filtered) != 1 || filtered[0].Content != "a" {
		t.Fatalf("filter after sort failed %v", filtered)
	}
}
