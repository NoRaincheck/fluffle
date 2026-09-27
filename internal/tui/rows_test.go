package tui

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// toModel unwraps a tea.Model for assertions, whether it holds a model or a
// pointer to one.
func toModel(t tea.Model) model {
	switch v := t.(type) {
	case model:
		return v
	case *model:
		return *v
	}
	panic("not a tui model")
}

// key builds a KeyMsg for a named key or a literal rune run.
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func plain(s string) string { return termtext.StripANSI(s) }

func testRow() store.Row {
	return store.Row{
		ID: 7, ThreadID: 3, Time: "2026-09-23T15:04:12Z",
		Channel: "eng", Thread: "pr-review", Name: "ci.bot",
		Content: "build passed", Count: 3,
	}
}

func TestRowLineFillsItsWidth(t *testing.T) {
	for _, w := range []int{MinWidth, 80, 200} {
		line := plain(rowLine(w, testRow(), false))
		if got := termtext.DisplayWidth(line); got != w {
			t.Errorf("rowLine(%d) is %d cells: %q", w, got, line)
		}
	}
}

func TestRowLineColumns(t *testing.T) {
	line := plain(rowLine(80, testRow(), false))
	// The 61-cell prefix is CursorW, then four ColW cells separated by ColGap,
	// then the CountW cell. It ends in the "  3" that TestRowLineCountColumn
	// pins at [rowPrefixW-CountW, rowPrefixW).
	const want = "  Sep 23 15:04  eng           pr-review     ci.bot          3"
	if !strings.HasPrefix(line, want) {
		t.Fatalf("row layout changed:\ngot  %q\nwant %q", line, want)
	}
	if !strings.Contains(line, "build passed") {
		t.Errorf("row is missing its content: %q", line)
	}
}

func TestRowLineSelectedCarriesTheCursor(t *testing.T) {
	if !strings.HasPrefix(plain(rowLine(80, testRow(), true)), "▸ ") {
		t.Error("a selected row must carry the cursor marker")
	}
}

func TestRowLineTruncatesOverlongColumns(t *testing.T) {
	r := testRow()
	r.Channel = strings.Repeat("x", 40)
	r.Name = strings.Repeat("y", 40)
	r.Content = "aaa\nbbb"
	line := plain(rowLine(80, r, false))
	if got := termtext.DisplayWidth(line); got != 80 {
		t.Fatalf("an overlong column broke the row width: %d", got)
	}
	if !strings.Contains(line, "aaa") {
		t.Errorf("the first line of the content must be rendered: %q", line)
	}
	if strings.Contains(line, "bbb") {
		t.Errorf("only the first line of the content may be rendered: %q", line)
	}
}

func TestRowLineSanitizesTheNameColumn(t *testing.T) {
	r := testRow()
	r.Name = "x\x1b]0;pwned\x07y"
	r.Channel = "\x1b]0;chan\x07eng"
	raw := rowLine(80, r, false)
	if strings.Contains(raw, "\x1b") {
		t.Errorf("an escape sequence reached the row: %q", raw)
	}
	if strings.Contains(raw, "pwned") || strings.Contains(raw, "chan\x07") {
		t.Errorf("an OSC payload must be dropped entirely: %q", raw)
	}
	if got := termtext.DisplayWidth(plain(raw)); got != 80 {
		t.Errorf("a sanitized column changed the row width to %d", got)
	}
}

func TestRowLineCountColumn(t *testing.T) {
	prefix := func(r store.Row) string {
		cells := []rune(plain(rowLine(80, r, false)))
		return string(cells[rowPrefixW-CountW : rowPrefixW])
	}
	if got := prefix(testRow()); got != "  3" {
		t.Errorf("count cell = %q, want %q", got, "  3")
	}
	one := testRow()
	one.Count = 1
	if got := prefix(one); strings.TrimSpace(got) != "" {
		t.Errorf("a count of 1 must leave the count column blank, got %q", got)
	}
	huge := testRow()
	huge.Count = 5000
	if got := prefix(huge); got != "99+" {
		t.Errorf("an overflowing count = %q, want %q", got, "99+")
	}
}

func TestRowLineSanitizesContent(t *testing.T) {
	r := testRow()
	r.Content = "clean\x1b]0;pwned\x07 text"
	// Assert on the raw line: plain() would strip the very sequences this
	// test exists to catch, so checking the stripped line proves nothing.
	raw := rowLine(80, r, false)
	if strings.Contains(raw, "\x1b") {
		t.Errorf("an escape sequence reached the row: %q", raw)
	}
	if strings.Contains(raw, "pwned") {
		t.Errorf("the OSC payload must be dropped entirely: %q", raw)
	}
	if !strings.Contains(plain(raw), "clean text") {
		t.Errorf("sanitization must keep the surrounding text: %q", plain(raw))
	}
}

func TestRowLineTruncatedContentKeepsTheTail(t *testing.T) {
	r := testRow()
	r.Content = strings.Repeat("z", 100)
	line := plain(rowLine(80, r, false))
	if !strings.HasSuffix(line, "…") {
		t.Errorf("truncated content must end in the tail, so the reader knows it was cut: %q", line)
	}
	if got := termtext.DisplayWidth(line); got != 80 {
		t.Errorf("line is %d cells, want 80", got)
	}
}

// renderRows owes the caller exactly h lines, each exactly w cells. The line
// count is the pad-to-height loop; the width is rowLine's own pad, plus render
// Rows' pad on the placeholder.
func TestRenderRowsFillsTheWindowWithFullWidthLines(t *testing.T) {
	rows := []store.Row{{ID: 1, Channel: "eng", Name: "alice", Content: "short"}}
	for _, c := range []struct {
		name string
		rows []store.Row
	}{
		{"rows", rows},
		{"empty state", nil},
	} {
		lines := strings.Split(renderRows(100, 4, c.rows, 0, 0), "\n")
		if len(lines) != 4 {
			t.Errorf("%s: renderRows returned %d lines, want 4", c.name, len(lines))
		}
		for i, line := range lines {
			if got := termtext.DisplayWidth(plain(line)); got != 100 {
				t.Errorf("%s line %d is %d cells, want 100: %q", c.name, i, got, line)
			}
		}
	}
}

func TestRenderRowsEmptyState(t *testing.T) {
	got := plain(renderRows(80, 10, nil, 0, 0))
	if !strings.Contains(got, "no rows") {
		t.Errorf("empty state = %q", got)
	}
	if n := strings.Count(got, "\n") + 1; n != 10 {
		t.Errorf("renderRows returned %d lines, want 10", n)
	}
}

func TestRenderRowsWindowFollowsScroll(t *testing.T) {
	rows := make([]store.Row, 20)
	for i := range rows {
		rows[i] = store.Row{ID: int64(i), Channel: "eng", Name: "alice", Content: fmt.Sprintf("body-%d", i)}
	}
	got := plain(renderRows(80, 5, rows, 10, 10))
	if !strings.Contains(got, "body-10") {
		t.Fatalf("the window does not start at the scrolled row: %q", got)
	}
	if strings.Contains(got, "body-9") || strings.Contains(got, "body-15") {
		t.Errorf("the window must hold exactly rows 10..14: %q", got)
	}
	if n := strings.Count(got, "\n") + 1; n != 5 {
		t.Errorf("renderRows returned %d lines, want 5", n)
	}
}

func TestRenderRowsMarksOnlyTheCursorRow(t *testing.T) {
	rows := []store.Row{{ID: 1, Content: "a"}, {ID: 2, Content: "b"}}
	got := plain(renderRows(80, 2, rows, 1, 0))
	if n := strings.Count(got, "▸"); n != 1 {
		t.Errorf("%d cursor markers rendered, want 1: %q", n, got)
	}
	if !strings.HasPrefix(got, "  ") {
		t.Errorf("the unselected row must not carry the cursor: %q", got)
	}
}

func TestNewStartsOnMessageGranularity(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	if m.granularity != store.GranularityMessage {
		t.Fatalf("granularity = %q, want message", m.granularity)
	}
}

func TestApplyRowsPreservesCursorByID(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.height = 40
	m.rows = []store.Row{{ID: 1}, {ID: 2}, {ID: 3}}
	m.cursor = 1
	m.applyRows([]store.Row{{ID: 9}, {ID: 2}, {ID: 7}})
	if m.rows[m.cursor].ID != 2 {
		t.Fatalf("cursor landed on id %d, want 2", m.rows[m.cursor].ID)
	}
}

func TestApplyRowsFallsBackWhenTheSelectedRowIsGone(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.height = 40
	m.rows = []store.Row{{ID: 1}, {ID: 2}, {ID: 3}}
	m.cursor = 2
	m.applyRows([]store.Row{{ID: 9}, {ID: 4}})
	if got := m.rows[m.cursor].ID; got != 9 {
		t.Fatalf("cursor = %d, want 9 once id 3 is gone", got)
	}
}

func TestApplyRowsReversesOnDemand(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.reversed = true
	m.applyRows([]store.Row{{ID: 1}, {ID: 2}, {ID: 3}})
	if m.rows[0].ID != 3 {
		t.Fatalf("reversed first row = %d, want 3", m.rows[0].ID)
	}
}

func TestApplyRowsClampsAnEmptyResult(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.rows = []store.Row{{ID: 1}}
	m.cursor = 5
	m.applyRows(nil)
	if m.cursor != 0 || m.scroll != 0 {
		t.Fatalf("cursor=%d scroll=%d, want 0 0", m.cursor, m.scroll)
	}
}

func TestTooNarrow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 40, 40
	if !m.tooNarrow() {
		t.Error("40 columns must be too narrow")
	}
	m.width, m.height = 120, 10
	if !m.tooNarrow() {
		t.Error("10 rows must be too narrow")
	}
	m.width, m.height = 120, 40
	if m.tooNarrow() || !m.split() {
		t.Error("120x40 must be a split")
	}
	m.width, m.height = 80, 40
	if m.tooNarrow() || m.split() {
		t.Error("80 columns must be usable but not split")
	}
	m.width, m.height = MinWidth, MinHeight
	if m.tooNarrow() {
		t.Errorf("%dx%d is exactly the minimum and must be usable", MinWidth, MinHeight)
	}
	m.width = MinWidth - 1
	if !m.tooNarrow() {
		t.Errorf("%d columns is one under the minimum and must be too narrow", MinWidth-1)
	}
	m.width, m.height = MinWidth, MinHeight-1
	if !m.tooNarrow() {
		t.Errorf("%d rows is one under the minimum and must be too narrow", MinHeight-1)
	}
	m.width = MinSplitWidth
	if !m.split() {
		t.Errorf("%d columns is the split threshold and must split", MinSplitWidth)
	}
	m.width = MinSplitWidth - 1
	if m.split() {
		t.Errorf("%d columns is one under the split threshold and must not split", MinSplitWidth-1)
	}
}

func TestKeysAreIgnoredWhenTheTerminalIsTooNarrow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 40, 40
	m.rows = []store.Row{{ID: 1}, {ID: 2}}
	m.cursor = 1
	if _, _ = m.handleKey(key("k")); m.cursor != 1 {
		t.Errorf("cursor moved to %d in a 40-column terminal", m.cursor)
	}
	if _, cmd := m.handleKey(key("q")); cmd != nil {
		t.Error("q must not quit a terminal too small to draw the list")
	}
	if _, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Error("ctrl+c must quit even when the terminal is too narrow")
	}
	if !m.quitting {
		t.Error("quitting must be set so View stops drawing")
	}
}

func TestDownKeyKeepsTheCursorVisible(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 80, MinHeight
	m.rows = make([]store.Row, 100)
	for i := range m.rows {
		m.rows[i] = store.Row{ID: int64(i), Content: fmt.Sprintf("row-%d", i)}
	}
	for range 40 {
		// A row with no thread legitimately yields a nil command, so the
		// command is not the signal to stop on.
		if _, _ = m.handleKey(key("j")); m.cursor > 40 {
			t.Fatalf("cursor ran past 40: %d", m.cursor)
		}
	}
	if m.cursor != 40 {
		t.Fatalf("cursor = %d, want 40", m.cursor)
	}
	visible := max(m.height-chromeH, 1)
	if m.cursor < m.scroll || m.cursor >= m.scroll+visible {
		t.Fatalf("cursor %d is outside the window [%d,%d)", m.cursor, m.scroll, m.scroll+visible)
	}
	// The row the cursor names must actually be drawn.
	window := plain(renderRows(m.width, visible, m.rows, m.cursor, m.scroll))
	if !strings.Contains(window, "row-40") {
		t.Errorf("the selected row is not in the rendered window: %q", window)
	}
}

func TestUpKeyStopsAtTheTop(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 80, 40
	m.rows = []store.Row{{ID: 1}, {ID: 2}}
	m.cursor, m.scroll = 1, 1
	if _, _ = m.handleKey(key("k")); m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	if m.scroll != 0 {
		t.Fatalf("scroll = %d, want 0 once the cursor is the first row", m.scroll)
	}
	if _, _ = m.handleKey(key("k")); m.cursor != 0 {
		t.Fatalf("cursor = %d, want to stay at 0", m.cursor)
	}
}

func TestClampKeepsTheCursorInsideTheVisibleWindow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.height = 13
	m.rows = make([]store.Row, 100)
	m.cursor = 99
	m.scroll = 0
	m.clamp()
	if m.cursor != 99 {
		t.Fatalf("cursor = %d, want 99", m.cursor)
	}
	if want := 99 - (13 - chromeH) + 1; m.scroll != want {
		t.Fatalf("scroll = %d, want %d so the cursor is the last visible row", m.scroll, want)
	}
}

func TestClampBoundsTheCursorToTheList(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.height = 40
	m.rows = []store.Row{{ID: 1}, {ID: 2}, {ID: 3}}
	m.cursor = 99
	m.scroll = 42
	m.clamp()
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want the last row", m.cursor)
	}
	if m.scroll > m.cursor {
		t.Errorf("scroll = %d must never pass the cursor %d", m.scroll, m.cursor)
	}
	m.cursor = -5
	m.clamp()
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.cursor)
	}
}

// selectedModel drives the production path to a loaded thread: it feeds a
// rowsFetchedMsg through Update rather than assigning fields, so it cannot mask
// a model that never records the selection.
func selectedModel(t *testing.T, api *apiClient, rows []store.Row) model {
	t.Helper()
	m := toModel(New(api.base))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	next, _ = next.Update(rowsFetchedMsg{granularity: store.GranularityMessage, rows: rows})
	return toModel(next)
}

func TestSyncThreadRecordsTheSelectedThread(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5}, {ID: 2, ThreadID: 6}}
	if cmd := m.syncThread(); cmd == nil {
		t.Error("the selected thread must be fetched")
	}
	if m.threadID != 5 {
		t.Fatalf("threadID = %d, want 5: nothing else records the selection", m.threadID)
	}
	if cmd := m.syncThread(); cmd != nil {
		t.Error("the already-loaded thread must not be fetched again")
	}
	if _, _ = m.handleKey(key("j")); m.threadID != 6 {
		t.Errorf("threadID = %d, want 6 after moving down", m.threadID)
	}
}

func TestSyncThreadClearsTheThreadForAChannelRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5}, {ID: 2, ThreadID: 0}}
	if _, _ = m.handleKey(key("j")); m.threadID != 0 {
		t.Fatalf("threadID = %d, want 0 on a channel row", m.threadID)
	}
	if cmd := m.syncThread(); cmd != nil {
		t.Error("a channel row has no thread and must fetch nothing")
	}
	if _, _ = m.handleKey(key("k")); m.threadID != 5 {
		t.Errorf("threadID = %d, want 5 after moving back onto a thread row", m.threadID)
	}
	m.rows = nil
	if _, _ = m.handleKey(key("j")); m.threadID != 0 {
		t.Errorf("threadID = %d, want 0 with no selection at all", m.threadID)
	}
}

func TestRowsFetchedRecordsTheThreadThroughUpdate(t *testing.T) {
	m := selectedModel(t, NewAPIClient("http://127.0.0.1:1"),
		[]store.Row{{ID: 1, ThreadID: 5}})
	if m.threadID != 5 {
		t.Fatalf("threadID = %d, want 5: Update's value receiver must keep the write", m.threadID)
	}
}

func TestThreadResponseIsAppliedForTheSelectedThread(t *testing.T) {
	m := selectedModel(t, NewAPIClient("http://127.0.0.1:1"),
		[]store.Row{{ID: 1, ThreadID: 5}})
	if m.threadID != 5 {
		t.Fatalf("threadID = %d, want 5", m.threadID)
	}
	next, _ := m.Update(threadFetchedMsg{
		threadID: 5,
		messages: []store.Message{{ID: 1, Content: "hi"}},
	})
	if got := toModel(next).thread; len(got) != 1 || got[0].Content != "hi" {
		t.Fatalf("the response for the loaded thread must be applied, got %+v", got)
	}
	next, _ = m.Update(threadFetchedMsg{
		threadID: 4,
		messages: []store.Message{{ID: 2, Content: "stale"}},
	})
	if got := toModel(next).thread; len(got) != 0 {
		t.Fatalf("a response for another thread must be dropped, got %+v", got)
	}
}

func TestComposeSendRepliesToTheSelectedThread(t *testing.T) {
	var gotPath, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"seq":7}`))
	}))
	defer ts.Close()

	m := selectedModel(t, NewAPIClient(ts.URL), []store.Row{{ID: 1, ThreadID: 5}})
	if m.threadID != 5 {
		t.Fatalf("threadID = %d, want 5", m.threadID)
	}
	next, cmd := m.handleComposeSend(composeSendMsg{text: "thanks"})
	if cmd == nil {
		t.Fatal("a reply must produce a command; a nil one is a silent no-op")
	}
	if toModel(next).compose.active {
		t.Error("the reply box must close on send")
	}
	if _, ok := cmd().(sentMsg); !ok {
		t.Fatalf("cmd returned %T, want sentMsg", cmd())
	}
	if gotPath != "/v1/threads/5/messages" {
		t.Errorf("posted to %q, want /v1/threads/5/messages", gotPath)
	}
	if !strings.Contains(gotBody, "thanks") {
		t.Errorf("body = %q, want the reply text", gotBody)
	}
}

func TestComposeSendClosesTheBox(t *testing.T) {
	m := selectedModel(t, NewAPIClient("http://127.0.0.1:1"), []store.Row{{ID: 1, ThreadID: 5}})
	m.compose.open("eng › pr-review")
	m.compose.text = "thanks"
	if !m.compose.active {
		t.Fatal("the reply box must be open before sending")
	}
	if _, _ = m.handleComposeSend(composeSendMsg{text: "thanks"}); m.compose.active {
		t.Error("sending must close the reply box")
	}
	if m.compose.text != "" {
		t.Errorf("text = %q, want it cleared on close", m.compose.text)
	}
}

func TestComposeSendWithNoSelectedThreadDoesNothing(t *testing.T) {
	posted := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posted = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"seq":7}`))
	}))
	defer ts.Close()

	// A channel row has no thread, so there is nothing to reply to.
	m := selectedModel(t, NewAPIClient(ts.URL), []store.Row{{ID: 1, ThreadID: 0}})
	if m.threadID != 0 {
		t.Fatalf("threadID = %d, want 0", m.threadID)
	}
	_, cmd := m.handleComposeSend(composeSendMsg{text: "thanks"})
	if cmd != nil {
		t.Error("a reply with no selected thread must not produce a command")
	}
	if posted {
		t.Error("nothing may be posted when no thread is selected")
	}
}

func TestSentMsgReportsSuccessAndFailure(t *testing.T) {
	m := selectedModel(t, NewAPIClient("http://127.0.0.1:1"), []store.Row{{ID: 1, ThreadID: 5}})

	next, cmd := m.Update(sentMsg{err: errors.New("DELIVERY_UNKNOWN: the reply was not acknowledged")})
	got := toModel(next)
	if !strings.Contains(got.status, "error:") || !strings.Contains(got.status, "DELIVERY_UNKNOWN") {
		t.Errorf("status = %q, want the delivery failure", got.status)
	}
	if cmd != nil {
		t.Error("a failed send must not refetch")
	}

	next, cmd = m.Update(sentMsg{})
	got = toModel(next)
	if got.status != "sent" {
		t.Errorf("status = %q, want sent", got.status)
	}
	if cmd == nil {
		t.Error("a successful send must refresh the rows and the thread")
	}
}

func TestRowsFetchedErrorIsReported(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 80, 40
	next, _ := m.Update(rowsFetchedMsg{
		granularity: store.GranularityMessage,
		err:         errors.New("DAEMON_DOWN: connection refused"),
	})
	if got := toModel(next).status; !strings.Contains(got, "error:") || !strings.Contains(got, "DAEMON_DOWN") {
		t.Errorf("status = %q, want the failure", got)
	}
}

func TestRowsFetchedForAnotherGranularityIsIgnored(t *testing.T) {
	m := selectedModel(t, NewAPIClient("http://127.0.0.1:1"), []store.Row{{ID: 1, ThreadID: 5}})
	before := len(m.rows)
	m.granularity = store.GranularityChannel
	next, _ := m.Update(rowsFetchedMsg{
		granularity: store.GranularityMessage,
		rows:        []store.Row{{ID: 99}},
	})
	if got := toModel(next); len(got.rows) != before || got.rows[0].ID != 1 {
		t.Errorf("a response for another granularity must be ignored, got %+v", got.rows)
	}
}

// A failed fetch must leave the pane empty rather than showing the previous
// thread under the new thread's title, and it must stay retryable.
func TestFailedThreadFetchClearsThePaneAndRetries(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"ID":1,"ThreadID":1,"Content":"thread A"}]`))
	}))
	defer ts.Close()

	next, _ := toModel(New(ts.URL)).Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	next, _ = next.Update(rowsFetchedMsg{granularity: store.GranularityMessage, rows: []store.Row{
		{ID: 1, ThreadID: 1, Channel: "eng", Thread: "a"},
		{ID: 2, ThreadID: 2, Channel: "eng", Thread: "b"},
	}})
	m := toModel(next)
	if m.threadID != 1 {
		t.Fatalf("threadID = %d, want 1", m.threadID)
	}
	next, _ = m.Update(threadFetchedMsg{threadID: 1, messages: []store.Message{{ID: 1, Content: "A"}}})
	m = toModel(next)
	if len(m.thread) != 1 {
		t.Fatalf("thread A = %+v, want it loaded", m.thread)
	}

	next, _ = m.handleKey(key("j"))
	m = toModel(next)
	if m.threadID != 2 {
		t.Fatalf("threadID = %d, want 2 after moving to b", m.threadID)
	}
	next, _ = m.Update(threadFetchedMsg{threadID: 2, err: errors.New("DAEMON_DOWN: connection refused")})
	m = toModel(next)

	if len(m.thread) != 0 {
		t.Errorf("a failed fetch left thread A's messages in the pane: %+v", m.thread)
	}
	if m.threadID != 0 {
		t.Errorf("threadID = %d, want 0 so the next syncThread retries", m.threadID)
	}
	if got := m.threadTitle(); got != "eng › b" {
		t.Errorf("threadTitle = %q, want the selected thread's title", got)
	}

	paths = nil
	cmd := m.syncThread()
	if cmd == nil {
		t.Fatal("the failed thread must be retried, not cached as loaded")
	}
	if _, ok := cmd().(threadFetchedMsg); !ok {
		t.Fatal("the retry must be a threadFetchedMsg")
	}
	if len(paths) != 1 || paths[0] != "/v1/threads/2/messages" {
		t.Errorf("retried %v, want [/v1/threads/2/messages]", paths)
	}
}

func TestSyncThreadFetchesOnlyWhatChanged(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.height = 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5}}
	if cmd := m.syncThread(); cmd == nil {
		t.Error("the selected thread must be fetched")
	}
	if cmd := m.syncThread(); cmd != nil {
		t.Error("the already-loaded thread must not be fetched again")
	}
	m.rows = []store.Row{{ID: 1, ThreadID: 9}}
	if cmd := m.syncThread(); cmd == nil {
		t.Error("a newly selected thread must be fetched")
	}
	m.rows = []store.Row{{ID: 1, ThreadID: 0}}
	if cmd := m.syncThread(); cmd != nil {
		t.Error("a channel row has no thread and must fetch nothing")
	}
	m.rows = nil
	if cmd := m.syncThread(); cmd != nil {
		t.Error("an empty list has no selection and must fetch nothing")
	}
}

func TestNarrowNoticeNamesTheMinimum(t *testing.T) {
	got := narrowNotice(40, 10)
	if !strings.Contains(got, "71") || !strings.Contains(got, "24") || !strings.Contains(got, "40x10") {
		t.Fatalf("notice = %q", got)
	}
}

func TestTitleLineNamesGranularity(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityChannel
	m.rows = []store.Row{{ID: 1}}
	if !strings.Contains(plain(m.titleLine()), "channel") {
		t.Fatalf("title = %q", m.titleLine())
	}
	if !strings.Contains(plain(m.titleLine()), "1 rows") {
		t.Errorf("title must count the rows: %q", m.titleLine())
	}
	if got := plain(m.titleLine()); !strings.Contains(got, "newest first") {
		t.Errorf("title = %q, want the newest-first order", got)
	}
	m.reversed = true
	if got := plain(m.titleLine()); !strings.Contains(got, "oldest first") {
		t.Errorf("title = %q, want the oldest-first order", got)
	}
}

func TestQQuits(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 80, 40
	_, cmd := m.handleKey(key("q"))
	if cmd == nil {
		t.Fatal("q must return a quit command")
	}
	if !m.quitting {
		t.Error("quitting must be set")
	}
}

func TestThreadTitleIsEmptyForAChannelRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.rows = []store.Row{{ID: 1, Channel: "eng", ThreadID: 0}}
	if m.threadTitle() != "" {
		t.Fatalf("threadTitle = %q, want empty", m.threadTitle())
	}
	m.rows = []store.Row{{ID: 1, Channel: "eng", Thread: "pr-review", ThreadID: 5}}
	if m.threadTitle() != "eng › pr-review" {
		t.Fatalf("threadTitle = %q", m.threadTitle())
	}
}

func TestViewIsNarrowNoticeWhenTooNarrow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 40, 10
	view := plain(m.View())
	if !strings.Contains(view, "needs 71 columns") {
		t.Fatalf("view = %q", view)
	}
	// The call site must pass the width first. TestNarrowNoticeNamesTheMinimum
	// calls narrowNotice with its own arguments, so only this can catch them
	// arriving the other way round, and a swapped pair prints a lie.
	if !strings.Contains(view, "got 40x10") {
		t.Fatalf("the notice must report the terminal it was given, not 10x40: %q", view)
	}
}

func TestViewIsExactlyTerminalHeight(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 200, 40
	m.rows = []store.Row{testRow()}
	if n := strings.Count(m.View(), "\n") + 1; n != 40 {
		t.Errorf("View is %d lines, want 40", n)
	}
}
