package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	layoutOPContent     = "original post text"
	layoutReplyOne      = "first reply"
	layoutReplyTwo      = "second reply"
	layoutOtherThreadOP = "other thread op"
)

func layoutInbox() []store.InboxMessage {
	return []store.InboxMessage{
		{Message: store.Message{ID: 1, ThreadID: 10, Seq: 1, Name: "alice", AuthorType: "human", Content: layoutOPContent, CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "eng", ThreadTitle: "layout-work", ChannelID: 1},
		{Message: store.Message{ID: 2, ThreadID: 10, Seq: 2, Name: "bob", AuthorType: "agent", Content: layoutReplyOne, CreatedAt: "2026-09-23T10:05:00Z"}, ChannelName: "eng", ThreadTitle: "layout-work", ChannelID: 1},
		{Message: store.Message{ID: 3, ThreadID: 10, Seq: 3, Name: "carol", AuthorType: "human", Content: layoutReplyTwo, CreatedAt: "2026-09-23T10:09:00Z"}, ChannelName: "eng", ThreadTitle: "layout-work", ChannelID: 1},
		{Message: store.Message{ID: 4, ThreadID: 11, Seq: 1, Name: "dave", AuthorType: "human", Content: layoutOtherThreadOP, CreatedAt: "2026-09-23T11:00:00Z"}, ChannelName: "eng", ThreadTitle: "other-thread", ChannelID: 1},
	}
}

func layoutFullRows() map[int64][]store.Message {
	return map[int64][]store.Message{
		10: {
			{ID: 1, ThreadID: 10, Seq: 1, Name: "alice", AuthorType: "human", Content: layoutOPContent, CreatedAt: "2026-09-23T10:00:00Z"},
			{ID: 2, ThreadID: 10, Seq: 2, Name: "bob", AuthorType: "agent", Content: layoutReplyOne, CreatedAt: "2026-09-23T10:05:00Z"},
			{ID: 3, ThreadID: 10, Seq: 3, Name: "carol", AuthorType: "human", Content: layoutReplyTwo, CreatedAt: "2026-09-23T10:09:00Z"},
		},
		11: {
			{ID: 4, ThreadID: 11, Seq: 1, Name: "dave", AuthorType: "human", Content: layoutOtherThreadOP, CreatedAt: "2026-09-23T11:00:00Z"},
		},
	}
}

func layoutModel(t *testing.T, base string, w, h int) model {
	t.Helper()
	m := toModel(New(base))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = toModel(nm)
	nm, _ = m.Update(inboxFetchedMsg{inbox: layoutInbox()})
	m = toModel(nm)
	m.preview = false
	return m
}

func layoutFullModel(t *testing.T, w, h int) model {
	t.Helper()
	m := layoutModel(t, "http://127.0.0.1:0", w, h)
	nm, _ := m.Update(keyRunes("l"))
	m = toModel(nm)
	nm, _ = m.Update(fullRowsFetchedMsg{rows: layoutFullRows()})
	return toModel(nm)
}

func lineWith(lines []string, sub string) string {
	for _, l := range lines {
		plain := stripAnsi(l)
		if strings.Contains(plain, sub) {
			return plain
		}
	}
	return ""
}

func inboxRenderLines(m model) []string {
	raw := strings.Split(stripAnsi(m.View()), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		runes := []rune(l)
		if len(runes) >= 2 && runes[0] == '│' && runes[len(runes)-1] == '│' {
			runes = runes[1 : len(runes)-1]
		}
		out = append(out, string(runes))
	}
	return out
}

func TestInboxLayoutDefaultsToCompact(t *testing.T) {
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	if m.inboxLayout != inboxLayoutCompact {
		t.Fatalf("default layout should be compact, got %v", m.inboxLayout)
	}
	view := m.View()
	if !strings.Contains(view, "layout:compact") {
		t.Fatalf("compact layout should be labelled in title, got %q", view)
	}
	if !strings.Contains(view, layoutReplyTwo) {
		t.Fatalf("compact should show the latest message per group, got %q", view)
	}
}

func TestInboxLayoutToggleWithL(t *testing.T) {
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	nm, _ := m.Update(keyRunes("l"))
	m = toModel(nm)
	if m.inboxLayout != inboxLayoutFull {
		t.Fatalf("l should switch to full, got %v", m.inboxLayout)
	}
	if !strings.Contains(m.View(), "layout:full") {
		t.Fatalf("full layout should be labelled in title, got %q", m.View())
	}
	nm, _ = m.Update(keyRunes("l"))
	m = toModel(nm)
	if m.inboxLayout != inboxLayoutCompact {
		t.Fatalf("l should switch back to compact, got %v", m.inboxLayout)
	}
}

func TestInboxLayoutToggleWithUppercaseL(t *testing.T) {
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	m.preview = false
	nm, _ := m.Update(keyRunes("L"))
	m = toModel(nm)
	if m.inboxLayout != inboxLayoutFull {
		t.Fatalf("L should switch to full, got %v", m.inboxLayout)
	}
	if m.preview {
		t.Fatalf("L should not enable preview")
	}
}

func TestPreviewToggleMovedToP(t *testing.T) {
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	m.preview = false
	nm, _ := m.Update(keyRunes("p"))
	m = toModel(nm)
	if !m.preview {
		t.Fatalf("p should enable preview")
	}
	nm, _ = m.Update(keyRunes("p"))
	m = toModel(nm)
	if m.preview {
		t.Fatalf("p should disable preview")
	}
}

func TestInboxFullLayoutHeadLineShowsOP(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	lines := inboxRenderLines(m)
	head := lineWith(lines, layoutOPContent)
	if head == "" {
		t.Fatalf("full layout should show the OP content, got %q", m.View())
	}
	if strings.Contains(head, layoutReplyTwo) {
		t.Fatalf("head line should not show the latest message, got %q", head)
	}
	if !strings.Contains(head, "alice") {
		t.Fatalf("head line should carry the OP author, got %q", head)
	}
}

func TestInboxFullLayoutRendersRepliesInOrder(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	lines := inboxRenderLines(m)
	replyOne := lineWith(lines, layoutReplyOne)
	replyTwo := lineWith(lines, layoutReplyTwo)
	if replyOne == "" || replyTwo == "" {
		t.Fatalf("both replies should render, got %q", m.View())
	}
	if !strings.Contains(replyOne, "> "+layoutReplyOne) {
		t.Fatalf("reply should be prefixed with '> ', got %q", replyOne)
	}
	if !strings.Contains(replyTwo, "> "+layoutReplyTwo) {
		t.Fatalf("reply should be prefixed with '> ', got %q", replyTwo)
	}
	var idxOne, idxTwo int
	for i, l := range lines {
		plain := stripAnsi(l)
		if strings.Contains(plain, layoutReplyOne) {
			idxOne = i
		}
		if strings.Contains(plain, layoutReplyTwo) {
			idxTwo = i
		}
	}
	if idxOne >= idxTwo {
		t.Fatalf("replies should be sequential, got %d and %d", idxOne, idxTwo)
	}
}

func TestInboxFullLayoutRepliesAlignToContentColumn(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	lines := inboxRenderLines(m)
	head := lineWith(lines, layoutOPContent)
	reply := lineWith(lines, layoutReplyOne)
	if head == "" || reply == "" {
		t.Fatalf("expected head and reply lines, got %q", m.View())
	}
	headCol := strings.Index(head, layoutOPContent)
	replyCol := strings.Index(reply, "> ")
	if headCol < 0 || replyCol < 0 {
		t.Fatalf("could not locate content columns: head=%q reply=%q", head, reply)
	}
	if headCol != replyCol {
		t.Fatalf("reply '> ' should start at content column %d, got %d", headCol, replyCol)
	}
}

func TestInboxFullLayoutDropsCountMarker(t *testing.T) {
	compact := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	compactView := compact.View()
	if !strings.Contains(compactView, "(2+)") {
		t.Fatalf("compact should keep the (2+) reply marker, got %q", compactView)
	}
	full := layoutFullModel(t, 140, 24)
	if strings.Contains(full.View(), "(2+)") {
		t.Fatalf("full layout should drop the reply-count marker, got %q", full.View())
	}
}

func TestInboxFullLayoutTruncatesEveryLine(t *testing.T) {
	long := strings.Repeat("x", 400)
	rows := layoutFullRows()
	rows[10][1].Content = long
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	nm, _ := m.Update(keyRunes("l"))
	m = toModel(nm)
	nm, _ = m.Update(fullRowsFetchedMsg{rows: rows})
	m = toModel(nm)
	lines := inboxRenderLines(m)
	reply := lineWith(lines, "> xxx")
	if reply == "" {
		t.Fatalf("expected a truncated reply line, got %q", m.View())
	}
	if !strings.HasSuffix(reply, "...") {
		t.Fatalf("truncated reply should end with ..., got %q", reply)
	}
	if len(reply) > m.width-2 {
		t.Fatalf("reply line %d exceeds panel width %d: %q", len(reply), m.width-2, reply)
	}
	head := lineWith(lines, layoutOPContent)
	if len(head) > m.width-2 {
		t.Fatalf("head line %d exceeds panel width %d: %q", len(head), m.width-2, head)
	}
}

func TestInboxFullLayoutThreadWithNoRepliesHasNoReplyLines(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	lines := inboxRenderLines(m)
	idx := -1
	for i, l := range lines {
		if strings.Contains(stripAnsi(l), layoutOtherThreadOP) {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("expected the second thread head line, got %q", m.View())
	}
	if idx+1 < len(lines) && strings.Contains(stripAnsi(lines[idx+1]), "> ") {
		t.Fatalf("thread without replies should not render reply lines: %q", lines[idx+1])
	}
}

func TestInboxFullLayoutCursorMovesPerGroup(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	if m.cursor != 0 {
		t.Fatalf("cursor should start on the first group, got %d", m.cursor)
	}
	nm, _ := m.Update(keyType(tea.KeyDown))
	m = toModel(nm)
	if m.cursor != 1 {
		t.Fatalf("cursor should advance one group, got %d", m.cursor)
	}
	nm, _ = m.Update(keyType(tea.KeyDown))
	m = toModel(nm)
	if m.cursor != 1 {
		t.Fatalf("cursor should stop at the last group, got %d", m.cursor)
	}
	nm, _ = m.Update(keyType(tea.KeyUp))
	m = toModel(nm)
	if m.cursor != 0 {
		t.Fatalf("cursor should move back one group, got %d", m.cursor)
	}
}

func TestInboxFullLayoutKeepsCursorBlockVisible(t *testing.T) {
	m := layoutFullModel(t, 140, 12)
	nm, _ := m.Update(keyType(tea.KeyDown))
	m = toModel(nm)
	if m.cursor != 1 {
		t.Fatalf("cursor should be on the second group, got %d", m.cursor)
	}
	view := m.View()
	if !strings.Contains(view, layoutOPContent) {
		t.Fatalf("cursor group must be scrolled into view, got %q", view)
	}
	if !strings.Contains(view, "> "+layoutReplyOne) || !strings.Contains(view, "> "+layoutReplyTwo) {
		t.Fatalf("the whole cursor block must stay visible, got %q", view)
	}
	if strings.Contains(view, layoutOtherThreadOP) {
		t.Fatalf("earlier group should be scrolled out, got %q", view)
	}
}

func TestInboxFullLayoutFetchesVisibleThreads(t *testing.T) {
	var mu sync.Mutex
	fetched := map[int64]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 4 || parts[0] != "v1" || parts[1] != "threads" || parts[3] != "messages" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			t.Errorf("bad thread id %q", parts[2])
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		fetched[id]++
		mu.Unlock()
		msgs, ok := layoutFullRows()[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(msgs)
	}))
	defer ts.Close()

	m := layoutModel(t, ts.URL, 140, 24)
	nm, cmd := m.Update(keyRunes("l"))
	m = toModel(nm)
	if cmd == nil {
		t.Fatalf("switching to full layout should fetch visible threads")
	}
	fetchedMsg, ok := cmd().(fullRowsFetchedMsg)
	if !ok {
		t.Fatalf("expected fullRowsFetchedMsg")
	}
	nm, _ = m.Update(fetchedMsg)
	m = toModel(nm)
	view := m.View()
	if !strings.Contains(view, "> "+layoutReplyOne) {
		t.Fatalf("replies should render once rows arrive, got %q", view)
	}
	if !strings.Contains(view, "> "+layoutReplyTwo) {
		t.Fatalf("replies should render once rows arrive, got %q", view)
	}

	mu.Lock()
	defer mu.Unlock()
	if fetched[10] != 1 || fetched[11] != 1 {
		t.Fatalf("expected one fetch per visible thread, got %v", fetched)
	}
}

func TestInboxFullLayoutDoesNotRefetchCachedThreads(t *testing.T) {
	var mu sync.Mutex
	fetched := map[int64]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		mu.Lock()
		fetched[id]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(layoutFullRows()[id])
	}))
	defer ts.Close()

	m := layoutModel(t, ts.URL, 140, 24)
	nm, cmd := m.Update(keyRunes("l"))
	m = toModel(nm)
	nm, _ = m.Update(cmd())
	m = toModel(nm)
	nm, cmd = m.Update(keyType(tea.KeyDown))
	m = toModel(nm)
	if cmd != nil {
		t.Fatalf("cached threads should not be refetched, got %T", cmd())
	}
	mu.Lock()
	defer mu.Unlock()
	if fetched[10] != 1 || fetched[11] != 1 {
		t.Fatalf("expected one fetch per thread, got %v", fetched)
	}
}

func TestInboxFullLayoutSortAndFilterStillWork(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	nm, _ := m.Update(keyRunes("v"))
	m = toModel(nm)
	if m.inboxSort != inboxSortChannelThreadDesc {
		t.Fatalf("v should still cycle sort in full layout, got %v", m.inboxSort)
	}
	nm, _ = m.Update(filterAppliedMsg{text: "eng/other-thread"})
	m = toModel(nm)
	if m.inboxFilterChan != "eng" || m.inboxFilterThread != "other-thread" {
		t.Fatalf("filter should apply in full layout, got %q/%q", m.inboxFilterChan, m.inboxFilterThread)
	}
	view := m.View()
	if !strings.Contains(view, layoutOtherThreadOP) {
		t.Fatalf("filtered group should render, got %q", view)
	}
	if strings.Contains(view, layoutOPContent) {
		t.Fatalf("filtered-out group should be hidden, got %q", view)
	}
}

func TestInboxFullLayoutSuppressesPreviewSplit(t *testing.T) {
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	m.preview = true
	if !m.previewVisible() {
		t.Fatalf("preview should be visible in compact layout")
	}
	nm, _ := m.Update(keyRunes("l"))
	m = toModel(nm)
	nm, _ = m.Update(fullRowsFetchedMsg{rows: layoutFullRows()})
	m = toModel(nm)
	if m.previewVisible() {
		t.Fatalf("full layout should suppress the preview split")
	}
	if m.inboxListWidth() != m.width-2 {
		t.Fatalf("full layout list should take the full width, got %d want %d", m.inboxListWidth(), m.width-2)
	}
	head := lineWith(inboxRenderLines(m), layoutOPContent)
	if head == "" {
		t.Fatalf("expected the OP head line, got %q", m.View())
	}
	if !strings.Contains(head, "alice") || !strings.Contains(head, "eng") {
		t.Fatalf("full layout should keep all columns, got %q", head)
	}
	nm, _ = m.Update(keyRunes("l"))
	m = toModel(nm)
	if m.previewVisible() {
		t.Fatalf("returning to compact should not silently re-enable preview")
	}
	nm, _ = m.Update(keyRunes("p"))
	m = toModel(nm)
	if !m.previewVisible() {
		t.Fatalf("p in compact should restore the preview split")
	}
}

func TestInboxFullLayoutDoesNotFetchPreview(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(layoutFullRows()[id])
	}))
	defer ts.Close()

	m := layoutModel(t, ts.URL, 140, 24)
	m.preview = true
	nm, cmd := m.Update(keyRunes("l"))
	m = toModel(nm)
	if cmd == nil {
		t.Fatalf("switching to full layout should still fetch thread rows")
	}
	msg, ok := cmd().(fullRowsFetchedMsg)
	if !ok {
		t.Fatalf("full layout should not also fetch preview data, got %T", cmd())
	}
	nm, _ = m.Update(msg)
	m = toModel(nm)
	nm, cmd = m.Update(keyType(tea.KeyDown))
	m = toModel(nm)
	if cmd != nil {
		t.Fatalf("no fetch expected while the split is suppressed, got %T", cmd())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/v1/threads/10/messages"] != 1 || hits["/v1/threads/11/messages"] != 1 {
		t.Fatalf("expected exactly one fetch per thread, got %v", hits)
	}
}

func TestLayoutFullTurnsPreviewOff(t *testing.T) {
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	m.preview = true
	if !m.previewVisible() {
		t.Fatalf("precondition: preview should be visible in compact")
	}
	nm, _ := m.Update(keyRunes("l"))
	m = toModel(nm)
	if m.inboxLayout != inboxLayoutFull {
		t.Fatalf("l should switch to full, got %v", m.inboxLayout)
	}
	if m.preview {
		t.Fatalf("switching to full should treat preview as off")
	}
	if m.previewVisible() {
		t.Fatalf("preview should not be visible in full layout")
	}
}

func TestLayoutToggleRoundTripWithPreview(t *testing.T) {
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	m.preview = false
	press := func(k string) model {
		nm, _ := m.Update(keyRunes(k))
		return toModel(nm)
	}
	m = press("p")
	if !m.preview || m.inboxLayout != inboxLayoutCompact {
		t.Fatalf("p on: preview=%v layout=%v", m.preview, m.inboxLayout)
	}
	m = press("l")
	if m.preview || m.inboxLayout != inboxLayoutFull {
		t.Fatalf("l to full: preview=%v layout=%v", m.preview, m.inboxLayout)
	}
	m = press("p")
	if !m.preview || m.inboxLayout != inboxLayoutCompact {
		t.Fatalf("p on: preview=%v layout=%v", m.preview, m.inboxLayout)
	}
	m = press("L")
	if m.preview || m.inboxLayout != inboxLayoutFull {
		t.Fatalf("L to full: preview=%v layout=%v", m.preview, m.inboxLayout)
	}
	m = press("l")
	if m.preview || m.inboxLayout != inboxLayoutCompact {
		t.Fatalf("l to compact: preview=%v layout=%v", m.preview, m.inboxLayout)
	}
}

func TestPreviewOnInvariantHoldsAcrossKeySequence(t *testing.T) {
	keys := []string{"l", "p", "L", "p", "l", "l", "p", "L", "p", "p"}
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	m.preview = false
	for i, k := range keys {
		nm, _ := m.Update(keyRunes(k))
		m = toModel(nm)
		if m.preview && m.inboxLayout != inboxLayoutCompact {
			t.Fatalf("after %d presses (%q): preview on in %v layout", i+1, k, inboxLayoutName(m.inboxLayout))
		}
	}
}

func TestResizeDoesNotReEnablePreviewInFullLayout(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	if m.preview {
		t.Fatalf("precondition: preview should be off in full layout")
	}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	m = toModel(nm)
	if m.preview {
		t.Fatalf("resize must not re-enable preview while the full layout is active")
	}
	nm, _ = m.Update(keyRunes("l"))
	m = toModel(nm)
	nm, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	m = toModel(nm)
	if !m.preview {
		t.Fatalf("resize should still auto-enable preview in compact layout")
	}
}

func TestPreviewOnSwitchesToCompactLayout(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	if m.inboxLayout != inboxLayoutFull {
		t.Fatalf("precondition: expected full layout, got %v", m.inboxLayout)
	}
	m.preview = false
	nm, _ := m.Update(keyRunes("p"))
	m = toModel(nm)
	if !m.preview {
		t.Fatalf("p should enable preview")
	}
	if m.inboxLayout != inboxLayoutCompact {
		t.Fatalf("p should switch to compact layout, got %v", m.inboxLayout)
	}
	if !m.previewVisible() {
		t.Fatalf("preview should be visible after p")
	}
	if !strings.Contains(m.View(), "layout:compact") {
		t.Fatalf("title should report compact, got %q", m.View())
	}
}

func TestPreviewOffKeepsFullLayout(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	m.preview = true
	nm, _ := m.Update(keyRunes("p"))
	m = toModel(nm)
	if m.preview {
		t.Fatalf("p should disable preview")
	}
	if m.inboxLayout != inboxLayoutFull {
		t.Fatalf("p turning preview off should not change layout, got %v", m.inboxLayout)
	}
}

func TestPreviewOnFromCompactKeepsCompact(t *testing.T) {
	m := layoutModel(t, "http://127.0.0.1:0", 140, 24)
	m.preview = false
	nm, _ := m.Update(keyRunes("p"))
	m = toModel(nm)
	if !m.preview || m.inboxLayout != inboxLayoutCompact {
		t.Fatalf("p from compact should enable preview and stay compact, got preview=%v layout=%v", m.preview, m.inboxLayout)
	}
}

func TestPreviewOnOutsideInboxStillForcesCompact(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	m.preview = false
	nm, _ := m.Update(keyRunes("p"))
	m = toModel(nm)
	m.view = viewChannels
	nm, _ = m.Update(keyRunes("p"))
	m = toModel(nm)
	if m.preview {
		t.Fatalf("p should turn preview off")
	}
	nm, _ = m.Update(keyRunes("p"))
	m = toModel(nm)
	if !m.preview {
		t.Fatalf("p should turn preview back on")
	}
	if m.inboxLayout != inboxLayoutCompact {
		t.Fatalf("preview on should always imply compact layout, got %v", m.inboxLayout)
	}
}

func TestInboxFullLayoutReplyOpensComposeForGroup(t *testing.T) {
	m := layoutFullModel(t, 140, 24)
	nm, _ := m.Update(keyRunes("r"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatalf("r should open reply compose in full layout")
	}
	if m.compose.state.threadID != 11 {
		t.Fatalf("reply should target the cursor group thread, got %d", m.compose.state.threadID)
	}
	nm, _ = m.Update(keyType(tea.KeyEsc))
	m = toModel(nm)
	nm, _ = m.Update(keyType(tea.KeyEsc))
	m = toModel(nm)
	if m.view != viewInbox {
		t.Fatalf("Esc should return to the inbox, got %v", m.view)
	}
	nm, _ = m.Update(keyType(tea.KeyDown))
	m = toModel(nm)
	nm, _ = m.Update(keyRunes("r"))
	m = toModel(nm)
	if m.compose.state.threadID != 10 {
		t.Fatalf("reply should follow the cursor group, got %d", m.compose.state.threadID)
	}
}
