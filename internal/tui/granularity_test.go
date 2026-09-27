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

// The clock is one chain, whatever else is in flight. It starts at Init, and
// refresh() arms the next tick and is the only thing that arms one, so a round —
// the tick, then the rows response it produced — leaves exactly one tick
// outstanding and never two. A second arming site doubles the count every round,
// two become four, then eight, and the TUI hammers the daemon instead of
// reading it.
//
// The steps are counted after they are all produced, so the whole round costs
// one tickInterval rather than one per step.
func TestTheClockHoldsExactlyOneTick(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.rows = []store.Row{{ID: 1, ThreadID: 5}}
	m.threadID = 5

	const rounds = 4
	steps := []tea.Cmd{m.Init()}
	for range rounds {
		next, cmd := m.Update(tickMsg{})
		m = toModel(next)
		steps = append(steps, cmd)
		next, cmd = m.Update(rowsFetchedMsg{granularity: store.GranularityMessage})
		m = toModel(next)
		steps = append(steps, cmd)
	}

	counts := countTicksAll(steps)
	if counts[0] != 1 {
		t.Errorf("Init left %d ticks in flight, want 1: the clock has to start somewhere, and refresh() is the only place it starts", counts[0])
	}
	for r := range rounds {
		refreshed, responded := counts[1+2*r], counts[2+2*r]
		if responded != 0 {
			t.Errorf("round %d's rows response armed %d ticks, want 0: the chain does not run through a response", r, responded)
		}
		if inFlight := refreshed + responded; inFlight != 1 {
			t.Errorf("round %d left %d ticks in flight, want 1: refresh() arms the clock and nothing else does", r, inFlight)
		}
	}
}

// g is the user's fetch, not the clock's, so neither the key nor the response
// it waits for may arm a tick. The clock is a self-sustaining chain and a tick
// nothing consumes is a second chain: each tick produces a rows response, each
// response armed another, and 2 became 4, then 8. The TUI was hammering the
// daemon instead of reading it, which is the opposite of what the clock is for.
//
// The baseline is taken the same way whichever site arms the tick, by adding up
// a whole clock round, so the number it reports is ticks-in-flight rather than
// ticks-somewhere.
func TestAUserFetchAddsNoTick(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5}}
	m.threadID = 5

	// One clock round: the clock fires and its refetch comes back. Whatever
	// that leaves in flight is the clock.
	next, refresh := m.Update(tickMsg{})
	m = toModel(next)
	next, clocked := m.Update(rowsFetchedMsg{granularity: store.GranularityMessage})
	m = toModel(next)

	// g, and the response it is waiting for.
	next, pressed := m.handleKey(key("g"))
	m = toModel(next)
	_, responded := m.Update(rowsFetchedMsg{granularity: m.granularity})

	counts := countTicksAll([]tea.Cmd{refresh, clocked, pressed, responded})
	before := counts[0] + counts[1]
	if before != 1 {
		t.Fatalf("a clock round left %d ticks in flight, want 1", before)
	}
	added := counts[2] + counts[3]
	if added != 0 {
		t.Errorf("g added %d ticks, want 0: a keypress is not part of the clock, and a tick nothing consumes is a second chain", added)
	}
	if total := before + added; total != 1 {
		t.Errorf("%d ticks in flight after g, want 1: each extra chain arms its own, so two become four, then eight", total)
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

// A transient DAEMON_DOWN must not stop the clock. The chain does not run
// through the rows response at all — refresh() arms the next tick before the
// request is even sent — so a response that arms nothing leaves the tick that
// was already in flight, and the round after the failure is armed like any
// other. The error reaches the status line and nothing else; the TUI recovers
// on its own, with no keypress, which is what the previous wave's fix was for.
//
// Before that fix the error path armed nothing either, but the clock was armed
// by the rows response and by nothing else, so one DAEMON_DOWN ended the chain
// for the rest of the session. Inverting the arming removes the dependency: the
// response is no longer on the chain, so it cannot break it.
func TestARowsErrorDoesNotStarveTheClock(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityMessage
	m.rows = []store.Row{{ID: 1, ThreadID: 5}}
	m.threadID = 5

	// The clock fires and its refetch fails.
	next, refresh := m.Update(tickMsg{})
	m = toModel(next)
	next, failed := m.Update(rowsFetchedMsg{
		granularity: store.GranularityMessage,
		err:         errors.New("DAEMON_DOWN: connection refused"),
	})
	if toModel(next).status == "" {
		t.Error("the error must still reach the status line")
	}

	// The next fire of the clock, which is the round after the failure.
	next, after := m.Update(tickMsg{})

	counts := countTicksAll([]tea.Cmd{refresh, failed, after})
	if counts[1] != 0 {
		t.Errorf("a rows error armed %d ticks, want 0: the clock is armed by refresh() and the chain does not run through the response", counts[1])
	}
	if counts[0] != 1 {
		t.Errorf("the round that failed left %d ticks in flight, want 1: a clock with nothing in flight never recovers", counts[0])
	}
	if counts[2] != 1 {
		t.Errorf("the round after a rows error armed %d ticks, want 1: the TUI has to recover on its own", counts[2])
	}
}

// countTicksAll counts the ticks each command tree arms and returns one count
// per tree, in order. A tick blocks for a whole tickInterval before it fires,
// so a tree cannot be counted by running it; every tree handed to this is armed
// at once and nothing waits between them, so one interval covers all of them
// and counting them one at a time would cost a wait per tree.
func countTicksAll(cmds []tea.Cmd) []int {
	fired := make([]chan tea.Msg, len(cmds))
	for i, cmd := range cmds {
		fired[i] = make(chan tea.Msg, 16)
		watch(cmd, fired[i])
	}
	time.Sleep(tickInterval + 100*time.Millisecond)
	counts := make([]int, len(cmds))
	for i, ch := range fired {
	drain:
		for {
			select {
			case msg := <-ch:
				if _, isTick := msg.(tickMsg); isTick {
					counts[i]++
				}
			default:
				break drain
			}
		}
	}
	return counts
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

// The key handler and the rows response are two halves of one press, and
// resetting the cursor in the first half is not enough: applyRows carries the
// cursor by the id of the row under it, so the rows themselves have to go or the
// response that follows matches the old id against the new list. The id
// namespaces differ per granularity, so that match succeeds on a row that has
// nothing to do with the one the user was on — and Enter and r then act on the
// wrong thread. This drives the whole path because the handler-only test above
// cannot see it.
func TestGCyclesOntoTheTopRowOfTheNextGranularity(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.granularity = store.GranularityChannel
	m.rows = []store.Row{{ID: 3, Channel: "eng"}}
	m.cursor = 0

	next, _ := m.handleKey(key("g"))
	if toModel(next).granularity != store.GranularityMessage {
		t.Fatal("g from channel granularity must reach message granularity")
	}
	// Id 3 is the channel the user was on. At message granularity the same
	// number is the second message, and the row the cursor lands on is what
	// Enter and r will open.
	next, _ = m.Update(rowsFetchedMsg{
		granularity: store.GranularityMessage,
		rows: []store.Row{
			{ID: 9, ThreadID: 1, Channel: "eng"},
			{ID: 3, ThreadID: 2, Channel: "eng"},
			{ID: 4, ThreadID: 3, Channel: "eng"},
		},
	})
	mm := toModel(next)
	if mm.cursor != 0 {
		t.Fatalf("cursor = %d on message %d, want the top row: g is another list, not another page",
			mm.cursor, mm.rows[mm.cursor].ID)
	}
}

// g changes what a row is, so the pane showing the old row's thread has nothing
// left to show. Leaving detail set puts an empty pane in front of the list,
// saying "press g" about a granularity the user has already pressed.
func TestGLeavesTheThreadPane(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, Channel: "eng", Thread: "pr"}}
	m.cursor = 0
	next, _ := m.handleKey(key("g"))
	m = toModel(next)
	next, _ = m.handleKey(key("g"))
	m = toModel(next)
	m.detail = true
	next, _ = m.handleKey(key("g"))
	if mm := toModel(next); mm.detail {
		t.Error("g must leave the thread pane: the rows it showed belong to a granularity that is gone")
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

// The spec's promise: a tick refetches and keeps the cursor. The feed is
// newest-first, so a new message prepends and a cursor held as a fixed index
// would slide onto whatever arrived; carrying it by id is what stops that.
//
// It also carries the window, and that is deliberate rather than incidental. The
// cursor was on the last visible row when the tick fired, so after the prepend
// it is one row further down and the window has to move to keep it visible —
// one row, and the message that arrived at the top goes with it. The old
// assertion checked the window's top id, which reads the same either way
// because the prepended row takes index 0 and every other row keeps its id, so
// it passed over a scroll shift of 0 to 1 and never saw the follow.
func TestTheTickKeepsTheCursorOnTheSameRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, MinHeight
	rows := make([]store.Row, 30)
	for i := range rows {
		rows[i] = store.Row{ID: int64(i + 1), ThreadID: 5}
	}
	m.rows = rows
	m.cursor = 20
	if m.scroll != 0 {
		t.Fatalf("the window starts at %d, want 0", m.scroll)
	}
	next, _ := m.Update(rowsFetchedMsg{
		granularity: store.GranularityMessage,
		rows:        append([]store.Row{{ID: 99, ThreadID: 5}}, rows...),
	})
	mm := toModel(next)
	if got := mm.rows[mm.cursor].ID; got != 21 {
		t.Errorf("cursor = %d on row %d, want the row the user was on", mm.cursor, got)
	}
	if mm.scroll != 1 {
		t.Errorf("scroll = %d, want 1: the prepended row pushed the cursor to the last visible line", mm.scroll)
	}
	visible := max(mm.height-chromeH, 1)
	if mm.cursor < mm.scroll || mm.cursor >= mm.scroll+visible {
		t.Errorf("cursor %d is outside the window [%d,%d): a refetch must not lose the user's place",
			mm.cursor, mm.scroll, mm.scroll+visible)
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
