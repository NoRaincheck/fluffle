package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

func TestComposeTypesAndSends(t *testing.T) {
	var c composeModel
	c.resize(100)
	c.open("reply in eng › pr-review")
	for _, r := range "hi" {
		c.update(key(string(r)))
	}
	if c.text != "hi" {
		t.Fatalf("text = %q, want %q", c.text, "hi")
	}
	cmd := c.update(key("enter"))
	if cmd == nil {
		t.Fatal("Enter must produce a send command")
	}
	msg, ok := cmd().(composeSendMsg)
	if !ok {
		t.Fatal("the command must produce a composeSendMsg")
	}
	if msg.text != "hi" {
		t.Fatalf("sent text = %q", msg.text)
	}
}

func TestComposeEmptySendDoesNothing(t *testing.T) {
	var c composeModel
	c.open("reply")
	if cmd := c.update(key("enter")); cmd != nil {
		t.Fatal("an empty compose must not send")
	}
	if c.err == "" {
		t.Fatal("an empty compose must say cannot be empty")
	}
	if !strings.Contains(c.err, "empty") {
		t.Errorf("err = %q, want it to name the empty text", c.err)
	}
}

// A refused send leaves its complaint behind, so the next one must clear it
// rather than showing a stale error over a good draft.
func TestComposeSendClearsAStaleError(t *testing.T) {
	var c composeModel
	c.open("reply")
	c.update(key("enter"))
	if c.err == "" {
		t.Fatal("the empty send must have complained")
	}
	for _, r := range "hi" {
		c.update(key(string(r)))
	}
	if cmd := c.update(key("enter")); cmd == nil {
		t.Fatal("a non-empty compose must send")
	}
	if c.err != "" {
		t.Errorf("err = %q, want it cleared once the send succeeds", c.err)
	}
}

func TestComposeWhitespaceOnlySendDoesNothing(t *testing.T) {
	var c composeModel
	c.open("reply")
	for _, r := range "  " {
		c.update(key(string(r)))
	}
	if cmd := c.update(key("enter")); cmd != nil {
		t.Fatal("a whitespace-only compose must not send")
	}
}

func TestComposeEscCancels(t *testing.T) {
	var c composeModel
	c.open("reply")
	c.update(key("x"))
	c.update(key("esc"))
	if c.active || c.text != "" || c.context != "" || c.cursor != 0 {
		t.Fatalf("Esc left the compose active: %+v", c)
	}
}

// composeBoxLines is the context, the text, the error line, the hint, and the
// two border rows. The box is that tall whatever it contains.
const composeBoxLines = 6

// Reopening must start from an empty box, not the last draft or the last
// complaint, or a cancelled reply leaks into the next one.
func TestComposeOpenClearsThePreviousDraft(t *testing.T) {
	var c composeModel
	c.open("reply")
	for _, r := range "abc" {
		c.update(key(string(r)))
	}
	c.update(key("enter"))
	c.open("reply in eng › pr-review")
	if c.text != "" {
		t.Errorf("text = %q, want it cleared on open", c.text)
	}
	if c.err != "" {
		t.Errorf("err = %q, want it cleared on open", c.err)
	}
	if c.cursor != 0 {
		t.Errorf("cursor = %d, want 0 on open", c.cursor)
	}
}

// The cursor is a real insertion point, not a display-only caret: left and
// right move it and typing inserts where it is.
func TestComposeCursorMovesWithinTheText(t *testing.T) {
	var c composeModel
	c.open("reply")
	for _, r := range "abc" {
		c.update(key(string(r)))
	}
	c.update(tea.KeyMsg{Type: tea.KeyLeft})
	if c.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 after left", c.cursor)
	}
	c.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if c.text != "abXc" {
		t.Fatalf("text = %q, want %q", c.text, "abXc")
	}
	if c.cursor != 3 {
		t.Errorf("cursor = %d, want 3 after typing", c.cursor)
	}
	c.update(tea.KeyMsg{Type: tea.KeyRight})
	if c.cursor != 4 {
		t.Errorf("cursor = %d, want 4 after right", c.cursor)
	}
	// The cursor is at the end of the text now, and right must not carry it
	// past the end: a cursor past the end slices out of range on the next
	// keystroke.
	for range 2 {
		c.update(tea.KeyMsg{Type: tea.KeyRight})
	}
	if c.cursor > len(c.text) {
		t.Errorf("cursor = %d, past the end of %q", c.cursor, c.text)
	}
}

// Backspace deletes the cell before the cursor and leaves the cursor on the
// cell that moved into its place.
func TestComposeBackspaceDeletesBeforeTheCursor(t *testing.T) {
	var c composeModel
	c.open("reply")
	for _, r := range "abc" {
		c.update(key(string(r)))
	}
	c.update(tea.KeyMsg{Type: tea.KeyLeft})
	c.update(key("backspace"))
	if c.text != "ac" {
		t.Fatalf("text = %q, want %q", c.text, "ac")
	}
	if c.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after backspace", c.cursor)
	}
}

// A terminal reports the space bar as its own key type, not as a rune.
func TestComposeSpaceKeyInsertsASpace(t *testing.T) {
	var c composeModel
	c.open("reply")
	c.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	c.update(tea.KeyMsg{Type: tea.KeySpace})
	if c.text != "a " {
		t.Fatalf("text = %q, want %q", c.text, "a ")
	}
}

func TestComposeBackspaceEdits(t *testing.T) {
	var c composeModel
	c.open("reply")
	for _, r := range "abc" {
		c.update(key(string(r)))
	}
	c.update(key("backspace"))
	if c.text != "ab" {
		t.Fatalf("text = %q, want %q", c.text, "ab")
	}
}

func TestComposeViewNamesTheThread(t *testing.T) {
	var c composeModel
	c.resize(100)
	c.open("reply in eng › pr-review")
	got := plain(c.view())
	if !strings.Contains(got, "pr-review") {
		t.Errorf("the compose view does not name the thread: %q", got)
	}
	// A view is several lines, and DisplayWidth sums a multi-line string rather
	// than reporting the widest line, so the width is measured per line.
	for i, line := range strings.Split(got, "\n") {
		if w := termtext.DisplayWidth(line); w > 100 {
			t.Errorf("compose view line %d is %d cells wide", i, w)
		}
	}
}

// The compose box is an overlay on the whole view, not a pane, so lipgloss's
// Width sets its content width and the border adds two more cells. At the
// narrowest supported terminal the box plus that border still has to fit, or
// the overlay clips it. The width is pinned exactly, because a bound alone
// leaves every box that is too narrow passing.
func TestComposeViewFitsTheNarrowestTerminal(t *testing.T) {
	var c composeModel
	c.resize(MinWidth)
	c.open("reply in eng › pr-review")
	for _, r := range "hi" {
		c.update(key(string(r)))
	}
	if c.width > MinWidth {
		t.Fatalf("compose width = %d, wider than the %d-cell terminal", c.width, MinWidth)
	}
	for i, line := range strings.Split(plain(c.view()), "\n") {
		if w := termtext.DisplayWidth(line); w != c.width {
			t.Errorf("compose view line %d is %d cells, want the box width %d",
				i, w, c.width)
		}
	}
}

// The same bound through the real render path, so the overlay that centres the
// box is covered and not just the box.
func TestComposeOverlayFitsTheNarrowestTerminal(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	resized, _ := m.Update(tea.WindowSizeMsg{Width: MinWidth, Height: MinHeight})
	m = toModel(resized)
	m.rows = []store.Row{{ID: 1, ThreadID: 5, Channel: "eng", Thread: "pr-review"}}
	m.cursor = 0
	next, _ := m.openReply()
	mm := toModel(next)
	view := plain(mm.View())
	for i, line := range strings.Split(view, "\n") {
		if w := termtext.DisplayWidth(line); w > MinWidth {
			t.Errorf("view line %d is %d cells at a %d-cell terminal", i, w, MinWidth)
		}
	}
	if !strings.Contains(view, "pr-review") {
		t.Errorf("the overlaid view does not show the compose box: %q", view)
	}
}

// The box is 80% of the terminal so the thread behind it stays readable. A
// bound would pass for a box that is arbitrarily narrow, so the width is
// pinned to the fraction.
func TestComposeResizeIsEightyPercentOfTheTerminal(t *testing.T) {
	for _, w := range []int{MinWidth, 80, 100, 120, 200} {
		var c composeModel
		c.resize(w)
		if want := w * 80 / 100; c.width != want {
			t.Errorf("resize(%d) gave width %d, want %d", w, c.width, want)
		}
	}
}

// Below the floor the box is a fixed width rather than a fraction. That case is
// unreachable through View, which shows the narrow notice first, so the floor is
// pinned here instead.
func TestComposeResizeFloorsNarrowTerminals(t *testing.T) {
	for _, w := range []int{0, 1, 29} {
		var c composeModel
		c.resize(w)
		if c.width != 60 {
			t.Errorf("resize(%d) gave width %d, want the floor 60", w, c.width)
		}
	}
}

// A long thread name is cut to the box's text column, not wrapped across it,
// so the box keeps its height however long the name is. The cut is pinned to
// the cell, not just asserted to be shorter than the input, so a column that is
// two cells too wide is caught too.
func TestComposeViewTruncatesALongThreadName(t *testing.T) {
	var c composeModel
	c.resize(MinWidth)
	long := "reply in eng " + strings.Repeat("x", 200)
	c.open(long)
	got := plain(c.view())
	if !strings.Contains(got, long[:c.width-6]) {
		t.Errorf("the compose view cut the context short of its text column: %q", got)
	}
	if strings.Contains(got, long[:c.width-5]) {
		t.Errorf("the compose view showed %d context cells, want %d",
			c.width-5, c.width-6)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != composeBoxLines {
		t.Errorf("the compose box is %d lines, want %d: a cut context must not wrap",
			len(lines), composeBoxLines)
	}
	for i, line := range lines {
		if w := termtext.DisplayWidth(line); w != c.width {
			t.Errorf("compose view line %d is %d cells, want the box width %d",
				i, w, c.width)
		}
	}
}

// The typed text is truncated to the same column as the context, so the box is
// a fixed rectangle however much is typed into it.
func TestComposeViewFitsLongTypedText(t *testing.T) {
	var c composeModel
	c.resize(MinWidth)
	c.open("reply in eng › pr-review")
	c.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", 500))})
	lines := strings.Split(plain(c.view()), "\n")
	if len(lines) != composeBoxLines {
		t.Fatalf("the compose box is %d lines, want %d: the text must be truncated, not wrapped",
			len(lines), composeBoxLines)
	}
	// The border is drawn in cells that are not whitespace, so it is trimmed
	// explicitly to get at the text column.
	if got := strings.Trim(lines[2], "│ "); got != strings.Repeat("x", c.width-6) {
		t.Errorf("the text line is %d cells of content, want %d",
			termtext.DisplayWidth(got), c.width-6)
	}
	for i, line := range lines {
		if w := termtext.DisplayWidth(line); w != c.width {
			t.Errorf("compose view line %d is %d cells, want the box width %d",
				i, w, c.width)
		}
	}
}

// The context is built from the row's channel and thread names, which are
// server-supplied and are sanitized everywhere else they are drawn. A modal
// that skipped that would be the one place an escape sequence in a channel name
// still reached the terminal.
func TestComposeViewSanitizesTheContext(t *testing.T) {
	var c composeModel
	c.resize(120)
	c.open("reply in eng › \x1b[31mpr-review\x1b[0m")
	if got := c.view(); strings.Contains(got, "\x1b") {
		t.Errorf("the compose view passed an escape sequence through: %q", got)
	}
	if got := plain(c.view()); !strings.Contains(got, "pr-review") {
		t.Errorf("sanitizing dropped the text it should keep: %q", got)
	}
}

// The cursor glyph is part of the text line. It is drawn at the end of the
// line rather than at the cursor, so moving the cursor does not move the glyph;
// what the box owes the reader is that the glyph is there at all.
func TestComposeViewDrawsTheCursor(t *testing.T) {
	var c composeModel
	c.resize(MinWidth)
	c.open("reply in eng › pr-review")
	for _, r := range "hi" {
		c.update(key(string(r)))
	}
	if got := plain(c.view()); !strings.Contains(got, "hi▏") {
		t.Errorf("the cursor is not drawn after the typed text: %q", got)
	}
}

func TestOpenReplyOnAThreadRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5, Channel: "eng", Thread: "pr-review"}}
	m.cursor = 0
	next, cmd := m.openReply()
	if cmd == nil {
		t.Fatal("openReply must fetch the thread it is replying to")
	}
	mm := toModel(next)
	if !mm.compose.active {
		t.Fatal("r did not open compose")
	}
	if mm.compose.context != "reply in eng › pr-review" {
		t.Fatalf("compose context = %q", mm.compose.context)
	}
	if !mm.detail {
		t.Fatal("replying must show the thread being replied to")
	}
}

// The binding itself, not just openReply: a key that opens nothing is the
// whole feature from the user's side.
func TestRKeyOpensTheReplyBox(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5, Channel: "eng", Thread: "pr-review"}}
	m.cursor = 0
	if _, _ = m.handleKey(key("r")); !m.compose.active {
		t.Fatalf("r did not open the reply box: %+v", m.compose)
	}
}

func TestRKeyOnAChannelRowSaysThereIsNoThread(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 0, Channel: "eng"}}
	m.cursor = 0
	if _, _ = m.handleKey(key("r")); m.compose.active {
		t.Fatal("r on a channel row must not open a reply box")
	}
	if m.status == "" {
		t.Fatal("r on a channel row must say there is no thread")
	}
}

func TestRKeyWithNoRowsSaysSo(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	if _, _ = m.handleKey(key("r")); m.compose.active {
		t.Fatal("r with no rows must not open a reply box")
	}
	if m.status == "" {
		t.Fatal("r with no rows must say so")
	}
}

func TestOpenReplyOnAChannelRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 0, Channel: "eng"}}
	next, _ := m.openReply()
	mm := toModel(next)
	if mm.compose.active {
		t.Fatal("a channel row has no thread to reply to")
	}
	if mm.status == "" {
		t.Fatal("a channel row must say so")
	}
}

func TestReplySendsToTheSelectedThread(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.threadID = 9
	m.compose.open("reply in eng › pr-review")
	next, _ := m.handleComposeSend(composeSendMsg{text: "hi"})
	mm := toModel(next)
	if mm.compose.active {
		t.Fatal("compose must close on send")
	}
}

// A thread load that failed left threadID at 0, so a reply cannot be sent. The
// typed text is dropped either way, and the user must be told why.
func TestReplyWithNoLoadedThreadSaysSo(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.threadID = 0
	m.compose.open("reply in eng › pr-review")
	next, _ := m.handleComposeSend(composeSendMsg{text: "hi"})
	mm := toModel(next)
	if mm.status == "" {
		t.Fatal("dropping a reply with no loaded thread must say so")
	}
	if mm.compose.active {
		t.Fatal("compose must close on send")
	}
}
