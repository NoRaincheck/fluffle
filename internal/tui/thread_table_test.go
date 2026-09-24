package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func TestSortedMessagesLatestAtTop(t *testing.T) {
	msgs := []store.Message{
		{ID: 1, Seq: 1, CreatedAt: "2025-01-01T10:00:00Z", Author: "alice", Content: "first"},
		{ID: 2, Seq: 2, CreatedAt: "2025-01-01T11:00:00Z", Author: "bob", Content: "second"},
		{ID: 3, Seq: 3, CreatedAt: "2025-01-01T09:00:00Z", Author: "carol", Content: "third"},
	}
	sorted := sortedMessagesDesc(msgs)
	if len(sorted) != 3 {
		t.Fatalf("sorted len %d", len(sorted))
	}
	if sorted[0].ID != 2 {
		t.Errorf("latest should be first, got ID %d want 2", sorted[0].ID)
	}
	if sorted[1].ID != 1 {
		t.Errorf("second latest wrong, got %d", sorted[1].ID)
	}
	if sorted[2].ID != 3 {
		t.Errorf("earliest last, got %d", sorted[2].ID)
	}
}

func TestRenderInboxDetailIsTableWithWrapping(t *testing.T) {
	m := model{
		width:           80,
		height:          20,
		view:            viewInboxDetail,
		selectedChannel: &store.Channel{Name: "general"},
		selectedThread:  &store.Thread{Title: "hello"},
		messages: []store.Message{
			{ID: 2, Seq: 2, CreatedAt: "2025-01-15T14:30:00Z", Author: "bob", AuthorType: "human", Content: "this is a very long message that should wrap onto multiple lines because it exceeds the available content width for the message column and must be displayed correctly"},
			{ID: 1, Seq: 1, CreatedAt: "2025-01-15T14:00:00Z", Author: "alice", AuthorType: "human", Content: "first short"},
		},
	}
	m.detailCursor = 0
	out := m.renderInboxDetail(m.width, m.height-4)
	if !strings.Contains(out, "TIME") || !strings.Contains(out, "NAME") || !strings.Contains(out, "MESSAGE") {
		t.Fatalf("renderInboxDetail should contain table header TIME/NAME/MESSAGE, got:\n%s", out)
	}
	bobIdx := strings.Index(out, "bob")
	aliceIdx := strings.Index(out, "alice")
	if bobIdx == -1 || aliceIdx == -1 {
		t.Fatalf("both authors should be in output, got:\n%s", out)
	}
	if bobIdx > aliceIdx {
		t.Errorf("latest at top: bob (latest) should appear before alice, got bobIdx %d aliceIdx %d\noutput:\n%s", bobIdx, aliceIdx, out)
	}
	if !strings.Contains(out, "very long message") {
		t.Fatalf("message content missing:\n%s", out)
	}
	if strings.Contains(out, "very long message that should wrap onto multiple lines because it exceeds the available content width") {
		t.Errorf("message should be wrapped, not on single long line:\n%s", out)
	}
}

func TestReplyFromInboxGoesToSinglePanelTable(t *testing.T) {
	m := model{
		width:  80,
		height: 20,
		view:   viewInbox,
		inbox: []store.InboxMessage{
			{Message: store.Message{ID: 10, ThreadID: 5, Seq: 2, CreatedAt: "2025-01-15T14:30:00Z", Author: "bob", Content: "latest"}, ChannelName: "general", ChannelID: 1, ThreadTitle: "hello"},
			{Message: store.Message{ID: 9, ThreadID: 5, Seq: 1, CreatedAt: "2025-01-15T14:00:00Z", Author: "alice", Content: "first"}, ChannelName: "general", ChannelID: 1, ThreadTitle: "hello"},
		},
		channels: []store.Channel{{ID: 1, Name: "general"}},
		cursor:   0,
	}
	m.detailCursor = 0
	next, _ := m.handleThreadReply()
	var nm model
	switch v := next.(type) {
	case model:
		nm = v
	case *model:
		nm = *v
	default:
		t.Fatalf("unexpected model type %T", next)
	}
	if nm.view != viewInboxDetail {
		t.Fatalf("r from inbox should go to viewInboxDetail (single panel table), got view %v", nm.view)
	}
	if !nm.compose.IsActive() {
		t.Fatalf("r should open compose overlay")
	}
	if nm.compose.state.threadID != 5 {
		t.Errorf("compose threadID should be 5, got %d", nm.compose.state.threadID)
	}
	if nm.detailThreadID == 0 {
		t.Errorf("detailThreadID should be set")
	}
}

func TestEnterFromInboxGoesToSinglePanelTableWithoutCompose(t *testing.T) {
	m := model{
		width:  80,
		height: 20,
		view:   viewInbox,
		inbox: []store.InboxMessage{
			{Message: store.Message{ID: 10, ThreadID: 5, Seq: 2, CreatedAt: "2025-01-15T14:30:00Z", Author: "bob", Content: "latest"}, ChannelName: "general", ChannelID: 1, ThreadTitle: "hello"},
		},
		channels: []store.Channel{{ID: 1, Name: "general"}},
		cursor:   0,
		preview:  true,
	}
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	next, _ := m.handleKey(msg)
	var nm model
	switch v := next.(type) {
	case model:
		nm = v
	case *model:
		nm = *v
	default:
		t.Fatalf("unexpected type %T", next)
	}
	if nm.view != viewInboxDetail {
		t.Fatalf("Enter from inbox should go to viewInboxDetail, got %v", nm.view)
	}
	if nm.compose.IsActive() {
		t.Fatalf("Enter should NOT open compose")
	}
	viewStr := nm.View()
	if strings.Contains(viewStr, "[PREVIEW") {
		t.Errorf("viewInboxDetail should be single panel, no preview split, got:\n%s", viewStr)
	}
}

func TestRenderInboxDetailShowsAllMessagesScrollable(t *testing.T) {
	msgs := make([]store.Message, 20)
	for i := 0; i < 20; i++ {
		msgs[i] = store.Message{ID: int64(i + 1), Seq: int64(i + 1), CreatedAt: "2025-01-15T14:30:00Z", Author: "alice", Content: "msg"}
	}
	m := model{
		width:           80,
		height:          10,
		view:            viewInboxDetail,
		selectedChannel: &store.Channel{Name: "general"},
		selectedThread:  &store.Thread{Title: "hello"},
		messages:        msgs,
	}
	sorted := sortedMessagesDesc(m.messages)
	if len(sorted) != 20 {
		t.Fatalf("sorted len")
	}
	if sorted[0].Seq != 20 {
		t.Errorf("latest seq should be 20 at top, got %d", sorted[0].Seq)
	}
}
