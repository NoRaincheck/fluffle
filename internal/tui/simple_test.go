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
	if m.compose.IsActive() {
		t.Fatalf("n should not open compose — reply-only")
	}
	nm, _ = m.Update(keyRunes("c"))
	m = toModel(nm)
	if m.compose.IsActive() {
		t.Fatalf("c should not open compose — reply-only")
	}
	nm, _ = m.Update(keyType(tea.KeyEnter))
	m = toModel(nm)
	nm, _ = m.Update(threadsFetchedMsg{channelID: 2, threads: threads})
	m = toModel(nm)
	nm, _ = m.Update(keyRunes("r"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("r should open compose for thread reply")
	}
	if m.compose.state.threadID != 10 {
		t.Fatalf("threadID %d", m.compose.state.threadID)
	}
	if m.view != viewMessages {
		t.Fatalf("r from threads should show full thread (viewMessages), got %v", m.view)
	}
	if m.compose.height != 4 {
		t.Fatalf("compose height should be minimal 4, got %d", m.compose.height)
	}
	view := m.View()
	if !strings.Contains(view, "hello") && !strings.Contains(view, "Reply in") {
		t.Fatalf("view should show thread and reply box, got %q", view)
	}
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
	// latest-desc default: cursor 0=threadID 11 (newer), cursor 1=threadID 10 (older)
	if m.compose.state.threadID != 10 {
		t.Fatalf("threadID %d", m.compose.state.threadID)
	}
	if m.compose.state.parentID != 0 {
		t.Fatalf("should reply to thread, not parent, got parentID %d", m.compose.state.parentID)
	}
	if m.view != viewMessages {
		t.Fatalf("r should show full thread (viewMessages), got %v", m.view)
	}
	if m.compose.height != 4 {
		t.Fatalf("compose minimal height 4, got %d", m.compose.height)
	}
	view := m.View()
	// cursor 1 = threadID 10 = "hello" (older, now second in latest-desc)
	if !strings.Contains(view, "hello") {
		t.Fatalf("compose view should show full thread layout, got %q", view)
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
	rendered := m.renderPreview(50, 40)
	if strings.Contains(rendered, "(+") {
		t.Fatalf("should not have truncation marker, got %q", rendered)
	}
	if strings.Count(rendered, "reply") < 10 {
		t.Fatalf("should show all 10 replies, got %d", strings.Count(rendered, "reply"))
	}
	if !strings.Contains(rendered, "PREVIEW THREAD") {
		t.Fatalf("preview header missing, got %q", rendered)
	}
	small := m.renderPreview(50, 10)
	if !strings.Contains(small, "PREVIEW THREAD") {
		t.Fatalf("preview header should remain visible when clipped, got %q", small)
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
	if m.view != viewInboxDetail {
		t.Fatalf("Enter should open detail view, got %v", m.view)
	}
	if m.compose.IsActive() {
		t.Fatalf("Enter should not open compose — only r")
	}
	nm, _ = m.Update(keyRunes("c"))
	m = toModel(nm)
	if m.compose.IsActive() {
		t.Fatalf("c should not open compose — only r")
	}
	nm, _ = m.Update(keyRunes("n"))
	m = toModel(nm)
	if m.compose.IsActive() {
		t.Fatalf("n should not open compose — only r")
	}
	nm, _ = m.Update(keyRunes("r"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("r should open compose")
	}
	if m.view != viewInboxDetail {
		t.Fatalf("r should stay in detail view, got %v", m.view)
	}
	m.compose.Close()
	m.view = viewInbox
	m.cursor = 0
	had := m.preview
	nm, _ = m.Update(keyRunes("L"))
	m = toModel(nm)
	if m.preview == had {
		t.Fatalf("L toggle failed")
	}
	nm, _ = m.Update(keyType(tea.KeyEsc))
	m = toModel(nm)
	if m.view != viewInbox {
		t.Fatalf("Esc should be no-op in inbox, got %v", m.view)
	}
}

func TestWrapText(t *testing.T) {
	long := "hello world this is a very long line that should wrap"
	lines := wrapText(long, 20)
	for i, l := range lines {
		if len(l) > 20 {
			t.Fatalf("line %d too long %q len %d", i, l, len(l))
		}
	}
	if len(lines) < 3 {
		t.Fatalf("expected wrap to 3+ lines, got %d: %v", len(lines), lines)
	}
	w2 := wrapText("supercalifragilisticexpialidocious", 10)
	for i, l := range w2 {
		if len(l) > 10 {
			t.Fatalf("break failed line %d %q", i, l)
		}
	}
}

func TestWrapTextPreservesParagraphs(t *testing.T) {
	multi := "first line\nsecond line\n\nthird line"
	lines := wrapText(multi, 20)
	found := false
	for _, l := range lines {
		if l == "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("should preserve blank paragraph, got %v", lines)
	}
}

func TestPreviewWrapNoTruncate(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	m.preview = true
	m.cursor = 0
	inbox := []store.InboxMessage{{Message: store.Message{ID: 1, ThreadID: 10, Content: "word " + strings.Repeat("x", 40) + " end", Author: "alice", AuthorType: "human", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello"}}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	nm, _ = m.Update(previewMessagesFetchedMsg{threadID: 10, messages: []store.Message{{ID: 2, ThreadID: 10, Seq: 2, Author: "bob", Content: "reply", CreatedAt: "2026-09-23T11:00:00Z"}}})
	m = toModel(nm)
	rendered := m.renderPreview(50, 30)
	if !strings.Contains(rendered, "end") {
		t.Fatalf("root post truncated, missing 'end' in %q", rendered)
	}
}

func TestInboxPreviewFillToHeight(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	inbox := []store.InboxMessage{{Message: store.Message{ID: 1, ThreadID: 10, Content: "root line one\nroot line two", Author: "alice", AuthorType: "human", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello"}}
	var msgs []store.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs, store.Message{ID: int64(2 + i), ThreadID: 10, Seq: int64(2 + i), Author: "bob", Content: fmt.Sprintf("reply %d", i), CreatedAt: "2026-09-23T11:00:00Z"})
	}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	nm, _ = m.Update(previewMessagesFetchedMsg{threadID: 10, messages: msgs})
	m = toModel(nm)
	m.cursor = 0
	tall := m.renderPreview(50, 40)
	if strings.Count(tall, "reply") != 10 {
		t.Fatalf("tall should show 10 replies, got %d", strings.Count(tall, "reply"))
	}
	if !strings.Contains(tall, "root line one") || !strings.Contains(tall, "root line two") {
		t.Fatalf("root not fully shown %q", tall)
	}
	short := m.renderPreview(50, 10)
	if !strings.Contains(short, "PREVIEW THREAD") {
		t.Fatalf("header clipped %q", short)
	}
	lines := strings.Split(strings.TrimSuffix(short, "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("height 10 expected 10 lines, got %d", len(lines))
	}
	if !strings.Contains(short, "root line one") {
		t.Fatalf("short preview should keep root post, got %q", short)
	}
}

func TestInboxEnterDetailView(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = toModel(nm)
	inbox := []store.InboxMessage{
		{Message: store.Message{ID: 1, ThreadID: 10, Seq: 1, Author: "alice", AuthorType: "human", Content: "root", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello", ChannelID: 1},
	}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	m.cursor = 0
	nm, _ = m.Update(keyType(tea.KeyEnter))
	m = toModel(nm)
	if m.view != viewInboxDetail {
		t.Fatalf("Enter should open detail view, got %v", m.view)
	}
	if m.detailThreadID != 10 {
		t.Fatalf("detailThreadID not set")
	}
}

func TestInboxEnterNoOpWhenEmpty(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = toModel(nm)
	nm, _ = m.Update(inboxFetchedMsg{inbox: nil})
	m = toModel(nm)
	nm, _ = m.Update(keyType(tea.KeyEnter))
	m = toModel(nm)
	if m.view != viewInbox {
		t.Fatalf("Enter on empty inbox should stay inbox")
	}
}

func TestInboxDetailScrolling(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m = toModel(nm)
	msgs := make([]store.Message, 20)
	for i := 0; i < 20; i++ {
		msgs[i] = store.Message{ID: int64(i + 1), ThreadID: 10, Seq: int64(i + 1), Author: "alice", AuthorType: "human", Content: fmt.Sprintf("message %d with a fairly long content that should wrap", i), CreatedAt: "2026-09-23T10:00:00Z"}
	}
	m.view = viewInboxDetail
	m.detailThreadID = 10
	m.messages = msgs
	m.selectedChannel = &store.Channel{Name: "general"}
	m.selectedThread = &store.Thread{Title: "hello"}
	m.width = 80
	m.height = 10
	rendered := m.renderInboxDetail(80, 6)
	lines := strings.Split(rendered, "\n")
	if len(lines) != 6 {
		t.Fatalf("detail height 6 expected 6 lines, got %d %q", len(lines), rendered)
	}
	if !strings.Contains(rendered, "message 0") {
		t.Fatalf("should show top, got %q", rendered)
	}
	m.detailScroll = 10
	rendered2 := m.renderInboxDetail(80, 6)
	if strings.Contains(rendered2, "message 0") {
		t.Fatalf("after scroll top should be hidden %q", rendered2)
	}
}

func TestPreviewRepliesNotTruncated(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = toModel(nm)
	longContent := strings.Repeat("x ", 50)
	inbox := []store.InboxMessage{{Message: store.Message{ID: 1, ThreadID: 10, Seq: 1, Author: "alice", AuthorType: "human", Content: "root", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello", ChannelID: 1}}
	msgs := []store.Message{{ID: 2, ThreadID: 10, Seq: 2, Author: "bob", Content: longContent, CreatedAt: "2026-09-23T11:00:00Z"}}
	nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
	m = toModel(nm)
	nm, _ = m.Update(previewMessagesFetchedMsg{threadID: 10, messages: msgs})
	m = toModel(nm)
	m.cursor = 0
	rendered := m.renderPreview(50, 20)
	if strings.Contains(rendered, "...") {
		t.Fatalf("replies should not have ellipsis (truncation), got %q", rendered)
	}
	if !strings.Contains(rendered, "x x x") {
		t.Fatalf("full reply content should be visible, got %q", rendered)
	}
}
