package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func TestSortedMessagesLatestAtTop(t *testing.T) {
	msgs := []store.Message{
		{ID: 1, Seq: 1, CreatedAt: "2025-01-01T10:00:00Z", Name: "alice", Content: "first"},
		{ID: 2, Seq: 2, CreatedAt: "2025-01-01T11:00:00Z", Name: "bob", Content: "second"},
		{ID: 3, Seq: 3, CreatedAt: "2025-01-01T09:00:00Z", Name: "carol", Content: "third"},
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
			{ID: 2, Seq: 2, CreatedAt: "2025-01-15T14:30:00Z", Name: "bob", AuthorType: "human", Content: "this is a very long message that should wrap onto multiple lines because it exceeds the available content width for the message column and must be displayed correctly"},
			{ID: 1, Seq: 1, CreatedAt: "2025-01-15T14:00:00Z", Name: "alice", AuthorType: "human", Content: "first short"},
		},
	}
	m.detailCursor = 0
	out := m.renderInboxDetail(m.width, m.height-4)
	if !strings.Contains(out, "TIME") || !strings.Contains(out, "NAME") || !strings.Contains(out, "MESSAGE") {
		t.Fatalf("renderInboxDetail should contain table header TIME/NAME/MESSAGE, got:\n%s", out)
	}
	if !strings.Contains(out, "Original Post") {
		t.Fatalf("renderInboxDetail should contain Original Post header, got:\n%s", out)
	}
	bobIdx := strings.Index(out, "bob")
	aliceIdx := strings.Index(out, "alice")
	if bobIdx == -1 || aliceIdx == -1 {
		t.Fatalf("both authors should be in output, got:\n%s", out)
	}
	if aliceIdx > bobIdx {
		t.Errorf("OP first: alice (original post) should appear before bob (reply), got aliceIdx %d bobIdx %d\noutput:\n%s", aliceIdx, bobIdx, out)
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
			{Message: store.Message{ID: 10, ThreadID: 5, Seq: 2, CreatedAt: "2025-01-15T14:30:00Z", Name: "bob", Content: "latest"}, ChannelName: "general", ChannelID: 1, ThreadTitle: "hello"},
			{Message: store.Message{ID: 9, ThreadID: 5, Seq: 1, CreatedAt: "2025-01-15T14:00:00Z", Name: "alice", Content: "first"}, ChannelName: "general", ChannelID: 1, ThreadTitle: "hello"},
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
			{Message: store.Message{ID: 10, ThreadID: 5, Seq: 2, CreatedAt: "2025-01-15T14:30:00Z", Name: "bob", Content: "latest"}, ChannelName: "general", ChannelID: 1, ThreadTitle: "hello"},
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

func TestRenderInboxDetailMessageColumnAligned(t *testing.T) {
	m := model{
		width:           80,
		height:          20,
		view:            viewInboxDetail,
		selectedChannel: &store.Channel{Name: "general"},
		selectedThread:  &store.Thread{Title: "hello"},
		messages: []store.Message{
			{ID: 1, Seq: 1, CreatedAt: "2025-01-15T14:00:00Z", Name: "alice", AuthorType: "human", Content: "short"},
			{ID: 2, Seq: 2, CreatedAt: "2025-01-15T14:01:00Z", Name: "bot-agent", AuthorType: "agent", Content: "also short"},
			{ID: 3, Seq: 3, CreatedAt: "2025-01-15T14:02:00Z", Name: "system", AuthorType: "system", Content: "same"},
		},
	}
	out := m.renderInboxDetail(m.width, m.height-4)

	// Strip ANSI codes for position checking
	stripANSI := func(s string) string {
		var b strings.Builder
		inEscape := false
		for _, r := range s {
			if r == '\x1b' {
				inEscape = true
				continue
			}
			if inEscape {
				if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
					inEscape = false
				}
				continue
			}
			b.WriteRune(r)
		}
		return b.String()
	}

	// Extract MESSAGE column start positions for each data row
	// Detail view has row number column: "  #  TIME        NAME        MESSAGE"
	// Expected MESSAGE column start: 1 + 4 + 2 + 12 + 2 + 10 + 2 = 33
	const expectedMsgCol = 33
	lines := strings.Split(out, "\n")
	var msgPositions []int
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "bot-agent") || strings.Contains(plain, "alice") || strings.Contains(plain, "system") {
			// Find the content position: look for the content word
			var content string
			switch {
			case strings.Contains(plain, "system"):
				content = "same"
			case strings.Contains(plain, "bot-agent"):
				content = "also"
			case strings.Contains(plain, "alice"):
				content = "short"
			}
			if content != "" {
				msgPos := strings.Index(plain, content)
				if msgPos >= 0 {
					msgPositions = append(msgPositions, msgPos)
				}
			}
		}
	}
	if len(msgPositions) < 2 {
		t.Fatalf("expected at least 2 message positions, got %d\noutput:\n%s", len(msgPositions), out)
	}
	// All MESSAGE column positions should match the expected column
	for i, pos := range msgPositions {
		if pos != expectedMsgCol {
			t.Errorf("row %d MESSAGE column at col %d, expected %d (not aligned)\noutput:\n%s", i+1, pos, expectedMsgCol, out)
		}
	}
}

func TestRenderInboxDetailShowsAllMessagesScrollable(t *testing.T) {
	msgs := make([]store.Message, 20)
	for i := 0; i < 20; i++ {
		msgs[i] = store.Message{ID: int64(i + 1), Seq: int64(i + 1), CreatedAt: "2025-01-15T14:30:00Z", Name: "alice", Content: "msg"}
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
