package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

func threadFixture() []store.Message {
	return []store.Message{
		{Seq: 1, Name: "alice", AuthorType: "human", Content: "the original post", CreatedAt: "2026-09-23T10:00:00Z"},
		{Seq: 2, Name: "bob", AuthorType: "human", Content: "a reply", CreatedAt: "2026-09-23T10:05:00Z"},
		{Seq: 3, Name: "ci.bot", AuthorType: "agent", Content: "build passed", CreatedAt: "2026-09-23T10:10:00Z"},
	}
}

func TestRenderThreadShowsEveryMessage(t *testing.T) {
	got := plain(renderThread(60, 24, "eng › pr-review", threadFixture()))
	for _, want := range []string{"alice", "the original post", "bob", "a reply", "ci.bot", "build passed"} {
		if !strings.Contains(got, want) {
			t.Errorf("thread view is missing %q:\n%s", want, got)
		}
	}
}

func TestRenderThreadIsExactlyHeight(t *testing.T) {
	for _, h := range []int{1, 2, 5, 24} {
		if n := strings.Count(renderThread(60, h, "eng › pr-review", threadFixture()), "\n") + 1; n != h {
			t.Errorf("height %d produced %d lines", h, n)
		}
	}
}

func TestRenderThreadHeaderNamesTheThread(t *testing.T) {
	got := plain(renderThread(60, 24, "eng › pr-review", threadFixture()))
	if !strings.Contains(got, "eng › pr-review") {
		t.Errorf("header does not name the thread: %q", got)
	}
}

// squeeze drops every space, so a body that was re-wrapped and re-indented
// can be compared against the content it came from.
func squeeze(s string) string { return strings.Join(strings.Fields(s), "") }

// threadBodyText is what one message renders below its header line, with the
// indent removed, so a test can compare it to the content that produced it.
func threadBodyText(w int, content string) string {
	lines := threadLines(w, []store.Message{{
		Name: "alice", AuthorType: "human", Content: content, CreatedAt: "2026-09-23T10:00:00Z",
	}})
	indent := strings.Repeat(" ", min(threadBodyAt, w))
	cells := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		cells = append(cells, strings.TrimPrefix(plain(line), indent))
	}
	return strings.Join(cells, " ")
}

// The brief's width test uses 20, 40, 74 and 200, where the body column is
// wider than the indent and the truncation is provably a no-op, so it proves
// nothing. These are the widths where a body-column floor would wrap at one
// width and then truncate at a narrower one, silently dropping words.
func TestRenderThreadKeepsEveryCharacterInANarrowPane(t *testing.T) {
	for _, w := range []int{9, 12, 15} {
		for _, content := range []string{"a b c d e f g h", "hello world"} {
			msgs := []store.Message{{
				Name: "alice", AuthorType: "human", Content: content, CreatedAt: "2026-09-23T10:00:00Z",
			}}
			for i, line := range strings.Split(renderThread(w, 24, "eng", msgs), "\n") {
				if got := termtext.DisplayWidth(line); got > w {
					t.Errorf("width %d row %d is %d cells, and %q was lost", w, i, got, content)
				}
			}
			if got := threadBodyText(w, content); squeeze(got) != squeeze(content) {
				t.Errorf("width %d silently dropped text: %q rendered as %q", w, content, got)
			}
		}
	}
	// A 2-cell grapheme needs 2 body cells. From 10 columns up there are, and
	// the text survives; below that the width invariant wins and the character
	// is dropped rather than split or allowed to overrun the pane.
	for _, w := range []int{12, 15} {
		const wide = "多角形 字 テスト"
		if got := threadBodyText(w, wide); squeeze(got) != squeeze(wide) {
			t.Errorf("width %d silently dropped wide text: %q rendered as %q", w, wide, got)
		}
	}
}

func TestRenderThreadEmptyState(t *testing.T) {
	got := plain(renderThread(60, 24, "", nil))
	if !strings.Contains(got, "no thread") {
		t.Errorf("empty state = %q", got)
	}
}

// The empty state is the one line renderThread does not pad itself, so it has
// to be padded on the way out: a short line beside the list leaves a hole in
// the row it shares.
func TestRenderThreadEmptyStateFillsThePaneExactly(t *testing.T) {
	for _, w := range []int{1, 8, 19, 20, 36, 74, 126, 200} {
		for _, h := range []int{1, 2, 5, 24, 40} {
			rows := strings.Split(renderThread(w, h, "", nil), "\n")
			if len(rows) != h {
				t.Errorf("an empty %dx%d pane is %d rows", w, h, len(rows))
			}
			for i, line := range rows {
				if got := termtext.DisplayWidth(line); got != w {
					t.Errorf("an empty %dx%d pane row %d is %d cells, want %d", w, h, i, got, w)
				}
			}
		}
	}
}

func TestRenderThreadNeverExceedsWidth(t *testing.T) {
	long := strings.Repeat("word ", 40)
	msgs := []store.Message{{Name: "alice", Content: long, CreatedAt: "2026-09-23T10:00:00Z"}}
	for _, w := range []int{20, 40, 74, 200} {
		for _, line := range strings.Split(renderThread(w, 24, "eng › pr-review", msgs), "\n") {
			if got := termtext.DisplayWidth(line); got > w {
				t.Fatalf("width %d produced a %d-cell line: %q", w, got, line)
			}
		}
	}
}

// The four widths above are the ones the plan names; this one is every width
// the TUI can be asked for, so a pane narrower than the body indent cannot
// slip past on a width nobody happened to try.
func TestRenderThreadFitsEveryWidthItIsGiven(t *testing.T) {
	msgs := []store.Message{
		{Name: "a-name-far-longer-than-the-column", AuthorType: "human", Content: strings.Repeat("word ", 40), CreatedAt: "2026-09-23T10:00:00Z"},
		{Name: "wide-너무-넓은-이름", AuthorType: "agent", Content: "日本語のテキストと絵文字🙂が混ざった行", CreatedAt: "2026-09-23T10:05:00Z"},
		{Name: "bob", AuthorType: "human", Content: strings.Repeat("多角形", 30), CreatedAt: "2026-09-23T10:10:00Z"},
	}
	for w := 1; w <= 300; w++ {
		for _, h := range []int{1, 2, 3, 5, 24, 40} {
			for _, thread := range [][]store.Message{msgs, nil} {
				for i, line := range strings.Split(renderThread(w, h, "eng › pr-review", thread), "\n") {
					if got := termtext.DisplayWidth(line); got > w {
						t.Fatalf("width %d height %d line %d is %d cells, with %d messages", w, h, i, got, len(thread))
					}
				}
			}
		}
	}
}

// A thread pane sits beside the list with nothing between them, so a short
// line would leave a hole in the row. Every line must be exactly w cells, not
// merely at most w.
func TestRenderThreadLinesFillThePaneExactly(t *testing.T) {
	for _, w := range []int{1, 8, 12, 36, 74, 126, 200} {
		for _, h := range []int{1, 2, 3, 5, 24, 40} {
			for i, line := range strings.Split(renderThread(w, h, "eng › pr-review", threadFixture()), "\n") {
				if got := termtext.DisplayWidth(line); got != w {
					t.Fatalf("width %d height %d line %d is %d cells, want %d", w, h, i, got, w)
				}
			}
		}
	}
}

func TestRenderThreadAtNoSizeIsNothing(t *testing.T) {
	if got := renderThread(0, 24, "eng › pr-review", threadFixture()); got != "" {
		t.Errorf("a zero-width pane drew %q", got)
	}
	if got := renderThread(60, 0, "eng › pr-review", threadFixture()); got != "" {
		t.Errorf("a zero-height pane drew %q", got)
	}
}

func TestRenderThreadInOneRowKeepsTheRule(t *testing.T) {
	got := plain(renderThread(60, 1, "eng › pr-review", threadFixture()))
	if want := strings.Repeat("─", 60); got != want {
		t.Errorf("a one-row pane is %q, want the rule %q", got, want)
	}
}

// Two rows is the title and the rule, and nothing else, so a header cannot
// push a message out of a pane that has room for exactly two rows of chrome.
func TestRenderThreadInTwoRowsIsTheHeaderAndTheRule(t *testing.T) {
	got := plain(renderThread(60, 2, "eng › pr-review", threadFixture()))
	want := []string{"eng › pr-review", strings.Repeat("─", 60)}
	for i, line := range strings.Split(got, "\n") {
		if line = strings.TrimRight(line, " "); line != want[i] {
			t.Errorf("row %d is %q, want %q", i, line, want[i])
		}
	}
}

// A thread that exactly fills its pane must show no blank row: that would mean
// the last message is being dropped in favour of padding.
func TestRenderThreadDoesNotPadWhenTheThreadFillsThePane(t *testing.T) {
	for i, line := range strings.Split(plain(renderThread(60, 8, "eng › pr-review", threadFixture())), "\n") {
		if strings.TrimSpace(line) == "" {
			t.Errorf("row %d is blank: the thread is being dropped in favour of padding:\n%s", i, line)
		}
	}
}

func TestRenderThreadTruncatesALongHeader(t *testing.T) {
	got := plain(renderThread(20, 24, "engineering › a-very-long-thread-slug", threadFixture()))
	head := strings.Split(got, "\n")[0]
	if termtext.DisplayWidth(head) != 20 {
		t.Fatalf("header is %d cells, want 20: %q", termtext.DisplayWidth(head), head)
	}
	if !strings.HasSuffix(strings.TrimRight(head, " "), "…") {
		t.Errorf("a truncated header must end in an ellipsis: %q", head)
	}
	if !strings.HasPrefix(head, "engineering › ") {
		t.Errorf("a truncated header must keep what fits: %q", head)
	}
}

func TestRenderThreadKeepsTheLineBreaksInAPost(t *testing.T) {
	msgs := []store.Message{{Name: "alice", Content: "first line\nsecond line", CreatedAt: "2026-09-23T10:00:00Z"}}
	got := plain(renderThread(60, 24, "eng › pr-review", msgs))
	var found int
	for _, line := range strings.Split(got, "\n") {
		for _, want := range []string{"first line", "second line"} {
			if strings.Contains(line, want) {
				found++
				if strings.Contains(line, "first line") && strings.Contains(line, "second line") {
					t.Errorf("a post's line break was flattened into one line: %q", line)
				}
			}
		}
	}
	if found != 2 {
		t.Errorf("found %d of the post's two lines:\n%s", found, got)
	}
}

// The pane here is wider than the header ever needs, so the name column is the
// only thing that can cut the name. A pane-width truncation would hide that,
// which is what a narrower pane does.
func TestRenderThreadTruncatesALongAuthorName(t *testing.T) {
	msgs := []store.Message{{Name: "a-name-far-longer-than-one-column", Content: "short", CreatedAt: "2026-09-23T10:00:00Z"}}
	head := strings.TrimRight(plain(strings.Split(renderThread(60, 24, "eng", msgs), "\n")[2]), " ")
	want := strings.Repeat(" ", threadIndent) + "10:00" + strings.Repeat(" ", threadHeadGap) + "a-name-far-l"
	if head != want {
		t.Errorf("author header is %q, want the name cut at the %d-cell column to %q", head, ColW, want)
	}
}

// A name is untrusted text like any other, and the row view sanitizes its name
// column, so the thread view must not be the one place a name slips through.
func TestRenderThreadSanitizesTheAuthorName(t *testing.T) {
	msgs := []store.Message{{
		Name: "a\x1b]0;pwned\x07lice", AuthorType: "human",
		Content: "hi", CreatedAt: "2026-09-23T10:00:00Z",
	}}
	got := renderThread(60, 24, "eng", msgs)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("an escape sequence in an author name reached the thread view: %q", got)
	}
	if !strings.Contains(plain(got), "alice") {
		t.Fatalf("sanitization must keep the surrounding text: %q", plain(got))
	}
}

func TestRenderThreadSanitizesContent(t *testing.T) {
	msgs := []store.Message{{Name: "alice", Content: "safe\x1b]0;pwned\x07 text", CreatedAt: "2026-09-23T10:00:00Z"}}
	got := renderThread(60, 24, "eng › pr-review", msgs)
	if strings.Contains(got, "\x1b") {
		t.Fatal("an escape sequence reached the thread view")
	}
	if !strings.Contains(plain(got), "safe text") {
		t.Fatalf("sanitization must keep the surrounding text: %q", plain(got))
	}
}

func TestRenderThreadSanitizesTheHeader(t *testing.T) {
	got := renderThread(60, 24, "eng\x1b]0;pwned\x07 › pr-review", threadFixture())
	if strings.Contains(got, "\x1b") {
		t.Fatalf("an escape sequence reached the header: %q", got)
	}
	if !strings.Contains(plain(got), "eng › pr-review") {
		t.Fatalf("sanitization must keep the surrounding text: %q", plain(got))
	}
}

func TestRenderThreadShowsTheTailWhenItOverflows(t *testing.T) {
	got := plain(renderThread(60, 6, "eng › pr-review", threadFixture()))
	if !strings.Contains(got, "build passed") {
		t.Errorf("a short pane must show the newest messages: %q", got)
	}
	if strings.Contains(got, "the original post") {
		t.Errorf("a short pane must drop the oldest messages: %q", got)
	}
}

func TestEnterOpensTheThreadAndEscReturns(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5, Channel: "eng", Thread: "pr-review"}}
	m.cursor = 0
	next, cmd := m.handleKey(key("enter"))
	mm := toModel(next)
	if !mm.detail {
		t.Fatal("Enter did not open the thread")
	}
	if mm.threadID != 5 {
		t.Fatalf("threadID = %d, want 5", mm.threadID)
	}
	if cmd == nil {
		t.Fatal("opening a thread must fetch it")
	}
	next, _ = mm.handleKey(key("esc"))
	mm = toModel(next)
	if mm.detail {
		t.Fatal("Esc did not leave the thread")
	}
}

func TestEnterOnAChannelRowHasNoThread(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 0, Channel: "eng"}}
	m.cursor = 0
	next, _ := m.handleKey(key("enter"))
	mm := toModel(next)
	if mm.threadID != 0 {
		t.Fatalf("a channel row must not load a thread, got %d", mm.threadID)
	}
	if mm.detail {
		t.Fatal("a channel row has no thread to open")
	}
	if mm.status == "" {
		t.Fatal("a channel row must say so")
	}
}

// testRow's content is "build passed", which is also the last line of
// threadFixture, so a test that looks for that string cannot tell the list
// apart from the thread. This fixture has content the list cannot produce.
func loneThread() []store.Message {
	return []store.Message{{
		Seq: 1, Name: "dana", AuthorType: "human",
		Content: "only in the thread", CreatedAt: "2026-09-23T10:00:00Z",
	}}
}

func TestSplitPutsTheThreadBesideTheList(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 200, 40
	m.rows = []store.Row{testRow()}
	m.thread = loneThread()
	if got, want := m.paneWidth(), m.width-ListW; got != want {
		t.Fatalf("a split thread pane is %d cells, want %d", got, want)
	}
	rows := strings.Split(m.View(), "\n")
	body := rows[1 : 1+m.height-chromeH]
	if len(body) != m.height-chromeH {
		t.Fatalf("a split body is %d rows, want %d", len(body), m.height-chromeH)
	}
	for i, line := range body {
		if got := termtext.DisplayWidth(line); got != m.width {
			t.Fatalf("body row %d is %d cells, want the %d-cell terminal", i, got, m.width)
		}
	}
	// The first body row shares the list with the thread's title and its rule,
	// so it must carry both panes. The message itself is further down.
	first := plain(body[0])
	for _, want := range []string{"build passed", "eng › pr-review"} {
		if !strings.Contains(first, want) {
			t.Errorf("a split body row must carry both panes, missing %q: %q", want, first)
		}
	}
	if got := plain(strings.Join(body, "\n")); !strings.Contains(got, "only in the thread") {
		t.Errorf("the thread pane lost its message:\n%s", got)
	}
}

func TestViewNeverWiderThanTheTerminalInEitherGeometry(t *testing.T) {
	for _, w := range []int{MinWidth, MinSplitWidth - 1, MinSplitWidth, 200} {
		for _, detail := range []bool{false, true} {
			m := toModel(New("http://127.0.0.1:1"))
			m.width, m.height = w, 40
			m.rows = []store.Row{testRow()}
			m.thread, m.detail = threadFixture(), detail
			view := m.View()
			if n := strings.Count(view, "\n") + 1; n != m.height {
				t.Errorf("%d columns, detail %v: View is %d lines, want %d", w, detail, n, m.height)
			}
			for i, line := range strings.Split(view, "\n") {
				if got := termtext.DisplayWidth(line); got > w {
					t.Errorf("%d columns, detail %v: line %d is %d cells", w, detail, i, got)
				}
			}
		}
	}
}

func TestTheThreadTakesTheWholeWidthWhenTheListCannotShareIt(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = MinSplitWidth-1, 40
	m.rows = []store.Row{testRow()}
	m.thread = loneThread()
	if m.split() {
		t.Fatalf("%d columns must not split", m.width)
	}
	if got := m.paneWidth(); got != m.width {
		t.Fatalf("a stacked thread is %d cells wide, want the %d-cell terminal", got, m.width)
	}
	if strings.Contains(plain(m.bodyView()), "eng › pr-review") {
		t.Fatal("a stacked terminal must show the list until Enter")
	}
	next, _ := m.handleKey(key("enter"))
	mm := toModel(next)
	if got := mm.paneWidth(); got != mm.width {
		t.Fatalf("Enter must not narrow the thread: %d cells at %d columns", got, mm.width)
	}
	if n := strings.Count(mm.View(), "\n") + 1; n != mm.height {
		t.Fatalf("the fullscreen thread is %d lines, want %d", n, mm.height)
	}
	body := plain(mm.bodyView())
	if !strings.Contains(body, "only in the thread") {
		t.Fatalf("Enter must show the thread where the list was:\n%s", body)
	}
	if strings.Contains(body, "build passed") {
		t.Fatalf("Enter must replace the list, not sit beside it:\n%s", body)
	}
}

func TestEnterAndEscMeanTheSameThingWhenSplit(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{testRow()}
	m.thread = loneThread()
	if !m.split() {
		t.Fatalf("%d columns must split", m.width)
	}
	splitView := m.bodyView()
	next, _ := m.handleKey(key("enter"))
	mm := toModel(next)
	if !mm.detail {
		t.Fatal("Enter did not open the thread")
	}
	if got := mm.paneWidth(); got != mm.width {
		t.Errorf("Enter in a split terminal is %d cells, want the %d-cell terminal", got, mm.width)
	}
	// A split terminal must go fullscreen too, or Enter means two different
	// things in the two geometries, which is the whole point of one renderer.
	full := plain(mm.bodyView())
	if !strings.Contains(full, "only in the thread") {
		t.Errorf("Enter must show the thread:\n%s", full)
	}
	if strings.Contains(full, "build passed") {
		t.Errorf("Enter in a split terminal must replace the list, not narrow it:\n%s", full)
	}
	back, _ := mm.handleKey(key("esc"))
	if toModel(back).detail {
		t.Fatal("Esc did not leave the thread")
	}
	if got := toModel(back).bodyView(); got != splitView {
		t.Error("Esc must restore the split view exactly")
	}
}

// author_type decides the colour of an author, and in a terminal the test
// runner shares there is no colour to see, so the profile is set to one that
// emits SGR. 1 is termenv.ANSI, the first profile that colours anything.
func TestThreadColoursAnAuthorByItsAuthorType(t *testing.T) {
	profile := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	lipgloss.SetColorProfile(1)
	one := func(authorType string) string {
		return renderThread(60, 24, "eng", []store.Message{{
			Name: "someone", AuthorType: authorType, Content: "hi", CreatedAt: "2026-09-23T10:00:00Z",
		}})
	}
	human, agent := one("human"), one("agent")
	if !strings.Contains(human, "\x1b") || !strings.Contains(agent, "\x1b") {
		t.Fatal("the profile must colour the thread, or this test proves nothing")
	}
	if human == agent {
		t.Errorf("a human and an agent render identically: %q", human)
	}
	if plain(human) != plain(agent) {
		t.Errorf("colour must not change the text: %q vs %q", plain(human), plain(agent))
	}
}
