package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

func TestGCyclesGranularity(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.threadID = 3
	want := []string{store.GranularityThread, store.GranularityChannel, store.GranularityMessage}
	for _, w := range want {
		next, cmd := m.handleKey(key("g"))
		mm := toModel(next)
		if mm.granularity != w {
			t.Fatalf("granularity = %q, want %q", mm.granularity, w)
		}
		if cmd == nil {
			t.Fatal("changing granularity must refetch")
		}
		if mm.threadID != 0 {
			t.Fatalf("changing granularity must drop the loaded thread, got %d", mm.threadID)
		}
	}
}

func TestVReversesSort(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	next, _ := m.handleKey(key("v"))
	mm := toModel(next)
	if !mm.reversed {
		t.Fatal("v did not reverse")
	}
	next, _ = mm.handleKey(key("v"))
	if mm = toModel(next); mm.reversed {
		t.Fatal("v did not toggle back")
	}
}

func TestRefreshAlwaysRefetchesRows(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityThread
	m.rows = []store.Row{{ID: 1, ThreadID: 4}}
	m.threadID = 4
	if m.refresh() == nil {
		t.Fatal("a tick must refetch the rows")
	}
}

func TestSyncThreadSkipsAChannelRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityChannel
	m.rows = []store.Row{{ID: 1, ThreadID: 0}}
	m.cursor = 0
	if m.syncThread() != nil {
		t.Fatal("a channel row must not fetch a thread")
	}
}

func TestStaleRowsResponseIsDropped(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityThread
	m.rows = []store.Row{{ID: 42}}
	next, _ := m.Update(rowsFetchedMsg{
		granularity: store.GranularityMessage,
		rows:        []store.Row{{ID: 1}},
	})
	mm := toModel(next)
	if len(mm.rows) != 1 || mm.rows[0].ID != 42 {
		t.Fatal("a response for another granularity must be dropped")
	}
}

func TestStaleThreadResponseIsDropped(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.threadID = 5
	m.thread = []store.Message{{Seq: 1}}
	next, _ := m.Update(threadFetchedMsg{threadID: 6, messages: []store.Message{{Seq: 9}}})
	if mm := toModel(next); mm.thread[0].Seq != 1 {
		t.Fatal("a response for another thread must be dropped")
	}
}

func TestTickIsArmedByTheFirstRowsResponse(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityMessage
	_, cmd := m.Update(rowsFetchedMsg{granularity: store.GranularityMessage})
	if cmd == nil {
		t.Fatal("the first rows response must arm a tick")
	}
}

// A tick is what keeps the clock running, so it has to arm its successor. If
// it did not, the second tick would be the last one and the TUI would go quiet
// two seconds after it opened.
func TestATickReArmsItself(t *testing.T) {
	if tickInterval != 2*time.Second {
		t.Errorf("tickInterval = %s, want the spec's two seconds", tickInterval)
	}
	m := toModel(New("http://127.0.0.1:1"))
	m.rows = []store.Row{{ID: 1, ThreadID: 5}}
	m.threadID = 5
	if _, cmd := m.Update(tickMsg{}); cmd == nil {
		t.Fatal("a tick must arm the next refresh")
	}
}

// The clock is the whole mechanism, so it is worth the two seconds to watch it
// run. Two things are asserted together because one wait covers both: the clock
// delivers tickMsg, so Update has something to re-arm on, and it does not
// multiply. Arming a tick anywhere but on a response gives two per round, then
// four, then eight, and the TUI ends up hammering the daemon.
func TestTheClockRunsOnce(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.rows = []store.Row{{ID: 1, ThreadID: 5}}
	m.threadID = 5

	const rounds = 4
	fired := make(chan tea.Msg, 64)
	for range rounds {
		// A round is one fire of the clock: the refresh it returns must arm
		// nothing, and the rows response that refresh produces is the only
		// thing allowed to re-arm.
		next, cmd := m.Update(tickMsg{})
		m = toModel(next)
		watch(cmd, fired)
		next, cmd = m.Update(rowsFetchedMsg{granularity: store.GranularityMessage})
		m = toModel(next)
		watch(cmd, fired)
	}

	ticks := 0
	deadline := time.After(tickInterval + time.Second)
	for {
		select {
		case msg := <-fired:
			if _, ok := msg.(tickMsg); !ok {
				continue
			}
			ticks++
		case <-deadline:
			if ticks != rounds {
				t.Fatalf("%d ticks fired over %d rounds, want %d: the clock is arming itself", ticks, rounds, rounds)
			}
			return
		}
	}
}

// watch runs a command tree in the background and reports every message it
// produces, so a test can count what a model armed without waiting for it
// twice. Ticks are the point: they block for a whole tickInterval.
func watch(cmd tea.Cmd, into chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	go func() {
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, leaf := range msg {
				watch(leaf, into)
			}
		default:
			into <- msg
		}
	}()
}

// g cycles between three different lists, so a message row has no counterpart
// at channel granularity and the cursor cannot be carried across. Resetting is
// the honest answer; matching by id would either keep the wrong row or land on
// an arbitrary one.
func TestGResetsTheCursorAndDropsTheThread(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5}, {ID: 2, ThreadID: 6}, {ID: 3, ThreadID: 7}}
	m.cursor, m.scroll = 2, 2
	m.threadID, m.thread = 6, []store.Message{{Seq: 1, Content: "posted"}}
	next, _ := m.handleKey(key("g"))
	mm := toModel(next)
	if mm.cursor != 0 || mm.scroll != 0 {
		t.Errorf("cursor %d scroll %d, want both at the top: another granularity is another list", mm.cursor, mm.scroll)
	}
	if mm.threadID != 0 || mm.thread != nil {
		t.Errorf("threadID %d thread %+v, want both dropped: the old row is gone", mm.threadID, mm.thread)
	}
}

// v re-sorts the same rows, so unlike g it can and must keep the cursor on the
// row it was on. A reverse that moves the highlight is a reverse the user has
// to re-find their place in.
func TestVKeepsTheCursorOnTheSameRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5}, {ID: 2, ThreadID: 6}, {ID: 3, ThreadID: 7}}
	m.cursor = 0
	next, _ := m.handleKey(key("v"))
	mm := toModel(next)
	if len(mm.rows) != 3 || mm.rows[0].ID != 3 {
		t.Fatalf("v did not re-sort: %+v", mm.rows)
	}
	if mm.cursor != 2 || mm.rows[mm.cursor].ID != 1 {
		t.Errorf("cursor = %d on row %d, want row 1 at the end of a reversed list", mm.cursor, mm.rows[mm.cursor].ID)
	}
}

// The spec's promise: a tick refetches and keeps the cursor. New messages
// prepend at message granularity, so a cursor held as a fixed index would slide
// onto whatever arrived.
func TestTheTickKeepsTheCursorOnTheSameRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, MinHeight
	rows := make([]store.Row, 30)
	for i := range rows {
		rows[i] = store.Row{ID: int64(i + 1), ThreadID: 5}
	}
	m.rows = rows
	m.cursor = 20
	top := m.rows[m.scroll].ID
	next, _ := m.Update(rowsFetchedMsg{
		granularity: store.GranularityMessage,
		rows:        append([]store.Row{{ID: 99, ThreadID: 5}}, rows...),
	})
	mm := toModel(next)
	if got := mm.rows[mm.cursor].ID; got != 21 {
		t.Errorf("cursor = %d on row %d, want the row the user was on", mm.cursor, got)
	}
	if got := mm.rows[mm.scroll].ID; got != top {
		t.Errorf("the window now starts at row %d, want %d: a refetch must not lose the user's place", got, top)
	}
}

// syncThread exists so j and k do not refetch a thread that is already on
// screen, which is why it cannot be what a tick calls: an agent's reply is a
// message appended to the thread that is already loaded, and nothing about the
// selection has changed.
func TestARefreshReReadsTheThreadAlreadyOnScreen(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.rows = []store.Row{{ID: 1, ThreadID: 5}}
	m.threadID = 5
	if cmd := m.syncThread(); cmd != nil {
		t.Fatal("syncThread must not refetch a loaded thread; that is why the tick needs its own call")
	}
	cmd := m.refetchThread()
	if cmd == nil {
		t.Fatal("the tick must re-read the thread on screen or an appended reply never appears")
	}
	msg, ok := cmd().(threadFetchedMsg)
	if !ok || msg.threadID != 5 {
		t.Fatalf("refetchThread produced %+v, want threadFetchedMsg for thread 5", msg)
	}
	m.rows = []store.Row{{ID: 1, ThreadID: 0}}
	if m.refetchThread() != nil {
		t.Error("a channel row has no thread and must be re-read as nothing")
	}
	m.rows, m.threadID = []store.Row{{ID: 1, ThreadID: 5}}, 4
	if msg := m.refetchThread()().(threadFetchedMsg); msg.threadID != 5 {
		t.Errorf("refetchThread re-read thread %d, want the selected row's thread 5: m.threadID is stale between a cursor move and its response", msg.threadID)
	}
}

// This is the property the six-field session poll existed to buy, and the
// reason the tick is unconditional: an agent's reply reaches the pane with no
// keypress at all.
func TestTheTickShowsAnAppendedReplyWithNoKeypress(t *testing.T) {
	var mu sync.Mutex
	replied := false
	before := []store.Message{{ID: 1, ThreadID: 5, Name: "you", Content: "please look"}}
	after := []store.Message{
		{ID: 1, ThreadID: 5, Name: "you", Content: "please look"},
		{ID: 2, ThreadID: 5, Name: "ci.bot", Content: "looked, all clear"},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/rows":
			content := "please look"
			if replied {
				content = "looked, all clear"
			}
			_, _ = w.Write([]byte(`[{"ID":1,"ThreadID":5,"Channel":"eng","Thread":"pr",` +
				`"Name":"you","Content":"` + content + `"}]`))
		case "/v1/threads/5/messages":
			if replied {
				_ = json.NewEncoder(w).Encode(after)
			} else {
				_ = json.NewEncoder(w).Encode(before)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	m := toModel(New(ts.URL))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5}}
	m.threadID, m.thread = 5, before

	mu.Lock()
	replied = true
	mu.Unlock()

	msgs := drain(t, m.refresh())
	for _, msg := range msgs {
		var next tea.Model
		next, _ = m.Update(msg)
		m = toModel(next)
	}
	if len(m.thread) != 2 || m.thread[1].Content != "looked, all clear" {
		t.Fatalf("the reply never reached the pane without a keypress, thread = %+v", m.thread)
	}
	// A tick re-reads the rows and the selected thread and nothing else. Any
	// other request would be a second clock; a missing one would leave the list
	// stale while the thread moved on.
	kinds := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		kinds = append(kinds, fmt.Sprintf("%T", msg))
	}
	sort.Strings(kinds)
	if got := strings.Join(kinds, ","); got != "tui.rowsFetchedMsg,tui.threadFetchedMsg" {
		t.Errorf("a tick produced %s, want one rowsFetchedMsg and one threadFetchedMsg", got)
	}
}

// A response for a thread the user has already navigated away from is the same
// stale response the success branch already drops. Blanking the pane on screen
// with an error about a thread that is no longer there helps nobody.
func TestALateThreadErrorDoesNotBlankTheThreadOnScreen(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.threadID = 5
	m.thread = []store.Message{{Seq: 1, Content: "posted"}}
	next, _ := m.Update(threadFetchedMsg{
		threadID: 4,
		err:      errors.New("DAEMON_DOWN: connection refused"),
	})
	mm := toModel(next)
	if len(mm.thread) != 1 {
		t.Fatalf("a late error for thread 4 blanked thread 5's pane: %+v", mm.thread)
	}
	if mm.threadID != 5 {
		t.Errorf("threadID = %d, want 5: the thread on screen is still loaded", mm.threadID)
	}
}

// Every key the TUI answers has to be on the line, or it is a key the user has
// to guess at. The line is one row of a 71-column floor, so completeness is
// bounded by the narrowest terminal the TUI admits to supporting.
func TestTheHintNamesEveryBoundKey(t *testing.T) {
	hint := plain(hintLine())
	for _, want := range []string{"↑↓", "g group", "v sort", "Enter read", "Esc back", "r reply", "q quit"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the hint does not name %q: %q", want, hint)
		}
	}
	if n := termtext.DisplayWidth(hintLine()); n > MinWidth {
		t.Errorf("the hint is %d cells and the narrowest terminal is %d: %q", n, MinWidth, hint)
	}
}

// The rewrite deleted six views and the keys that opened them. Rebinding one of
// those keys would bring a view back with no other trace.
func TestTheRemovedKeysAreNotBound(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5, Channel: "eng", Thread: "pr"}}
	for _, k := range []string{"l", "p", "s", "n", "C", "e", "f", "c"} {
		next, cmd := m.handleKey(key(k))
		mm := toModel(next)
		if cmd != nil {
			t.Errorf("%q is bound: it returned a command", k)
		}
		if mm.quitting || mm.detail || mm.compose.active || mm.threadID != 0 || mm.status != "" {
			t.Errorf("%q changed the model: %+v", k, mm)
		}
	}
}

// drain runs a command tree and returns every message it produced except a
// tick. The leaves run concurrently and the wait ends on an idle gap, because a
// tick blocks for the whole of tickInterval and no test should sit that out.
func drain(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	msgs := make(chan tea.Msg, 8)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, leaf := range batch {
				go run(leaf)
			}
			return
		}
		msgs <- msg
	}
	go run(cmd)
	var out []tea.Msg
	idle := time.NewTimer(50 * time.Millisecond)
	defer idle.Stop()
	for {
		select {
		case msg := <-msgs:
			if _, isTick := msg.(tickMsg); isTick {
				continue
			}
			out = append(out, msg)
			idle.Reset(50 * time.Millisecond)
		case <-idle.C:
			return out
		}
	}
}
