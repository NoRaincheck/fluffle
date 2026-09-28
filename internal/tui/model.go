package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

type model struct {
	width, height int
	quitting      bool
	granularity   string
	reversed      bool
	detail        bool
	rows          []store.Row
	cursor        int
	scroll        int
	threadID      int64
	thread        []store.Message
	status        string
	api           *apiClient
	compose       composeModel
}

type rowsFetchedMsg struct {
	granularity string
	rows        []store.Row
	err         error
}

type threadFetchedMsg struct {
	threadID int64
	messages []store.Message
	err      error
}

type composeSendMsg struct{ text string }

type sentMsg struct{ err error }

func New(base string) tea.Model {
	return model{granularity: store.GranularityMessage, api: NewAPIClient(base)}
}

func (m model) Init() tea.Cmd { return m.refresh() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.compose.resize(m.width)
		// The scroll window's height is derived from the terminal, so a resize
		// moves the last full page and can leave an offset that was legal a
		// moment ago past the end of the list. clamp is the only thing that
		// knows the bound, and nothing else re-derives it: the tick's
		// applyRows clamps too, but only once every couple of seconds, which is
		// a list of blank rows and an undrawn cursor until it lands.
		m.clamp()
		return m, nil
	case rowsFetchedMsg:
		// No tick is armed here, on either outcome, and that is the whole
		// arrangement. The clock is armed by refresh() before the request is
		// sent, so a response is never on the chain: it cannot starve it by
		// returning no command, and it cannot fork it by returning one. What
		// this path owes the clock is nothing.
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		if msg.granularity != m.granularity {
			return m, nil
		}
		m.applyRows(msg.rows)
		return m, m.syncThread()
	case threadFetchedMsg:
		// A response is stale or it is not, and the thread id that selects it
		// is the whole test. That is true of the failure as much as the
		// success: a late error for a thread the user has already navigated
		// away from must not blank the pane they are now looking at.
		if msg.threadID != m.threadID {
			return m, nil
		}
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			// A failed load must not leave the previous thread's messages
			// under the newly selected thread's title, and clearing
			// threadID lets the next syncThread retry instead of treating
			// the failure as loaded.
			m.thread, m.threadID = nil, 0
			return m, nil
		}
		m.thread = msg.messages
		return m, nil
	case composeSendMsg:
		return m.handleComposeSend(msg)
	case sentMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		m.status = "sent"
		return m, tea.Batch(m.fetchRows(), m.fetchThread(m.threadID))
	case tickMsg:
		return m, m.refresh()
	case tea.KeyMsg:
		// ctrl+c quits everywhere, and it is the one key Update takes before
		// the reply box. Routing it into the box would swallow it, because
		// the box has no case for it — and then a user who cannot send has
		// Esc as the only way out of a process they are trying to abandon.
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.compose.active {
			return m, m.compose.update(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) View() string {
	if m.tooNarrow() {
		return narrowNotice(m.width, m.height)
	}
	if m.compose.active {
		return overlayCentered(m.bodyView(), m.compose.view(), m.width, m.height)
	}
	return m.bodyView()
}

// slack is the margin the view leaves on its right and bottom edges, and it is
// the last line of defence against a viewport the app cannot see.
//
// Everything here is drawn to *exactly* the size the terminal reported, which
// leaves no room for disagreement. If the terminal is one cell narrower than it
// said, or measures one cell differently — a font, an ambiguous-width setting,
// anything — then the row the app believes is the terminal's width is one cell
// over, the terminal wraps it, and the emulator consumes a row the renderer
// does not know about. Every row after it is then written one row lower while
// the renderer still believes it skipped them, so the frame is drawn correctly
// and lands in the wrong place: the title band scrolls off the top, the footer
// and the status band off the bottom, and a row of the list is left behind at
// the top. Nothing in the app can detect that, and nothing in the app can undo
// it, because the only evidence is a screen the app never reads.
//
// A row the terminal can measure one cell differently from the app is the
// ordinary case, not the exotic one, so the view is drawn a cell narrower and a
// row shorter than the terminal and never touches an edge. One column of
// content and one blank row are the whole cost; the bands become unreachable
// for a one-cell disagreement, which is the only size that occurs.
const slack = 1

// viewW is the width the view is drawn at, one cell inside the terminal.
func (m model) viewW() int { return max(m.width-slack, 1) }

// bodyH is the height the body is drawn at: the terminal less the chrome and
// one spare row. chromeH, listWindowH and bodyView all read it, so the bands and
// the scroll window cannot disagree about how many rows the body has.
func (m model) bodyH() int { return max(m.height-m.chromeH()-slack, 1) }

// bodyView is title, body, footer, and status: the chrome around exactly the
// rest. In a split terminal the thread sits beside the list; in a stacked one it
// replaces the list; either way it is the same renderThread.
func (m model) bodyView() string {
	h := m.bodyH()
	var body string
	switch {
	case m.detail:
		body = renderThread(m.paneWidth(), h, m.threadTitle(), m.thread)
	case m.split():
		body = sideBySide(
			renderRows(ListW, h, m.rows, m.cursor, m.scroll),
			renderThread(m.paneWidth(), h, m.threadTitle(), m.thread))
	default:
		body = renderRows(m.viewW(), h, m.rows, m.cursor, m.scroll)
	}
	return strings.Join([]string{
		m.titleLine(),
		body,
		renderHelp(helpItems(), m.viewW()),
		m.statusBand(),
	}, "\n")
}

// statusBlockPad is the width the status band leaves unpainted at the right of
// the terminal, so the band is a band and not the whole row.
const statusBlockPad = 2

// statusBand is the last row of the view, and it is one row whatever the note
// in it. The note is budgeted to the band's **content** width, which is narrower
// than its block width because the style pads a cell each side and lipgloss
// wraps at the content width.
//
// Budgeted to the block width instead, any note longer than the content width
// wraps onto a second row, and that is not a cosmetic fault: the view is now a
// row taller than the terminal, and a renderer cannot reach into a terminal's
// scrollback, so it drops the top line to fit. The title band disappears, the
// column header moves up into its row, and the footer lands in the status
// band's row.
//
// The block width carries the excess too, because lipgloss measures narrow:
// without it the padded band is a cell or two wider than the terminal on a
// wide-ambiguous one. See cells.go.
func (m model) statusBand() string {
	block := m.viewW() - statusBlockPad
	note := termtext.SanitizeLine(m.statusLine())
	content := block - statusStyle.GetPaddingLeft() - statusStyle.GetPaddingRight()
	band := max(block-excessCells(note), 0)
	return statusStyle.Width(band).Render(truncateCells(note, content, truncTail))
}

// chromeH is the title band, the footer, and the status band. The footer is
// reflowed to the terminal width, so its height is whatever that reflow chose
// and this is the only place the arithmetic lives: the body, and the scroll
// window that has to fit inside it, are both derived from it.
func (m model) chromeH() int {
	return titleH + m.footerH() + statusH
}

func (m model) footerH() int {
	return max(len(strings.Split(renderHelp(helpItems(), m.viewW()), "\n")), 1)
}

// paneWidth is the width the thread is drawn at, whether it sits beside the
// list or fills the terminal. It is the only place that arithmetic lives, so
// both placements cannot drift apart.
func (m model) paneWidth() int {
	if m.split() && !m.detail {
		return m.viewW() - ListW - PaneDividerW
	}
	return m.viewW()
}

// sideBySide puts the thread pane to the right of the list, one list row and
// one thread row per output row, with the divider column between them. Joining
// the two blocks instead would stack them, which costs a row and is not a
// split.
func sideBySide(list, thread string) string {
	l, r := strings.Split(list, "\n"), strings.Split(thread, "\n")
	divider := sepStyle.Render(string(paneDividerRune))
	rows := make([]string, 0, max(len(l), len(r)))
	for i := range max(len(l), len(r)) {
		rows = append(rows, rowAt(l, i)+divider+rowAt(r, i))
	}
	return strings.Join(rows, "\n")
}

func rowAt(rows []string, i int) string {
	if i < len(rows) {
		return rows[i]
	}
	return ""
}

func (m model) tooNarrow() bool { return m.width < MinWidth || m.height < MinHeight }

// split reports whether the terminal is wide enough for the list and the
// thread side by side.
func (m model) split() bool { return m.width >= MinSplitWidth }

func (m model) titleLine() string {
	order := "newest first"
	if m.reversed {
		order = "oldest first"
	}
	return titleStyle.Render(fmt.Sprintf("flf - %s - %d rows - %s", m.granularity, len(m.rows), order))
}

func (m model) statusLine() string {
	if m.status != "" {
		return m.status
	}
	return ""
}

// appendStatus adds a note to the status band without losing what is already
// there: a later note explains an action, and overwriting the reason with it
// loses why the action was refused.
func appendStatus(status, note string) string {
	if status == "" {
		return note
	}
	return status + " - " + note
}

// threadTitle names the thread the pane shows, or is empty for a channel row,
// which has none. This is the only place a row and a thread disagree.
func (m model) threadTitle() string {
	row, ok := m.selectedRow()
	if !ok || row.ThreadID == 0 {
		return ""
	}
	return row.Channel + " › " + row.Thread
}

func narrowNotice(w, h int) string {
	return fmt.Sprintf("flf needs %d columns and %d rows (got %dx%d) - resize the terminal",
		MinWidth, MinHeight, w, h)
}

func (m *model) applyRows(rows []store.Row) {
	previous := int64(0)
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		previous = m.rows[m.cursor].ID
	}
	if m.reversed {
		rows = reverseRows(rows)
	}
	m.rows = rows
	m.cursor = 0
	for i, r := range rows {
		if r.ID == previous {
			m.cursor = i
			break
		}
	}
	m.clamp()
}

func reverseRows(rows []store.Row) []store.Row {
	out := make([]store.Row, len(rows))
	for i, r := range rows {
		out[len(rows)-1-i] = r
	}
	return out
}

func (m *model) clamp() {
	if len(m.rows) == 0 {
		m.cursor, m.scroll = 0, 0
		return
	}
	m.cursor = min(max(m.cursor, 0), len(m.rows)-1)
	visible := m.listWindowH()
	// The window is bounded by the list at both ends, and stating the bottom
	// of it is the whole point. `visible` comes from the terminal, so an
	// offset that was the last full page at one height is past the end at
	// another: the rows above it become unreachable by scrolling and the rows
	// below it are blank. The bound used to hold only as a consequence of the
	// offset always being derived from the cursor with the same height, which a
	// resize breaks and nothing else repairs until the next tick.
	m.scroll = min(max(m.scroll, 0), max(0, len(m.rows)-visible))
	// The cursor is inside the window, and the window moves as little as the
	// cursor needs it to.
	if top := m.cursor - visible + 1; m.scroll > top {
		m.scroll = top
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.cursor >= m.scroll+visible {
		m.scroll = m.cursor - visible + 1
	}
}

// listWindowH is how many rows of the list are the scrolling window rather than
// the header pinned above it. The window is what clamp scrolls, so it has to be
// the height clamp believes in: sizing it by the whole body let the cursor be
// pushed one row past the last drawn row, which is a cursor you cannot see.
func (m model) listWindowH() int {
	return max(m.bodyH()-listHeaderH, 1)
}

func (m model) selectedRow() (store.Row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return store.Row{}, false
	}
	return m.rows[m.cursor], true
}

func (m model) fetchRows() tea.Cmd {
	g := m.granularity
	return func() tea.Msg {
		rows, err := m.api.ListRows(nil, g, 200)
		return rowsFetchedMsg{granularity: g, rows: rows, err: err}
	}
}

func (m model) fetchThread(threadID int64) tea.Cmd {
	if threadID == 0 {
		return nil
	}
	return func() tea.Msg {
		msgs, err := m.api.ListMessages(nil, threadID)
		return threadFetchedMsg{threadID: threadID, messages: msgs, err: err}
	}
}

// syncThread loads the selected row's thread when it is not the one already
// loaded, and records which thread that is. A channel row has no thread, so it
// loads nothing and clears the selection.
func (m *model) syncThread() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok || row.ThreadID == 0 {
		m.threadID = 0
		return nil
	}
	if row.ThreadID == m.threadID {
		return nil
	}
	m.threadID = row.ThreadID
	return m.fetchThread(row.ThreadID)
}

// refetchThread re-reads the selected row's thread whatever is already loaded.
// It is the tick's counterpart to syncThread, and it has to be a different call:
// syncThread exists so j and k do not refetch a thread that is already on
// screen, but an agent's reply is exactly that — a message appended to the
// thread already loaded — and nothing about the selection has changed for
// syncThread to notice.
func (m model) refetchThread() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok || row.ThreadID == 0 {
		return nil
	}
	return m.fetchThread(row.ThreadID)
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Update takes ctrl+c before it gets here so the reply box cannot swallow
	// it. This is the other half of that: handleKey also answers a terminal
	// too narrow to draw anything.
	if msg.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}
	if m.tooNarrow() {
		return m, nil
	}
	switch msg.String() {
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		m.clamp()
		return m, m.syncThread()
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
		m.clamp()
		return m, m.syncThread()
	case "enter":
		return m.openThread()
	case "r":
		return m.openReply()
	case "esc":
		m.detail = false
		return m, nil
	case "g":
		// Another granularity is another list, so the row the cursor was on has
		// no counterpart in it. Reset rather than match by id — and drop the
		// rows, because applyRows carries the cursor by the id under it and the
		// id namespaces differ per granularity: a channel id can equal a
		// message id, so a stale row left here is matched against the next
		// list's rows and lands on an unrelated one.
		m.granularity = m.nextGranularity()
		m.rows = nil
		m.cursor, m.scroll = 0, 0
		m.detail = false
		m.threadID = 0
		m.thread = nil
		return m, m.fetchRows()
	case "v":
		// The same rows in the other order, so the cursor can be carried across
		// by id and does not have to be.
		m.reversed = !m.reversed
		m.applyRows(m.rows)
		return m, nil
	}
	return m, nil
}

// openThread opens the selected row's thread. In a split terminal it fills
// the terminal; in a stacked one it replaces the list. Same rule either way,
// so Enter and Esc mean the same thing in both geometries. A channel row has
// no thread and says so rather than opening an empty pane.
func (m *model) openThread() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		m.status = "no row selected"
		return m, nil
	}
	if row.ThreadID == 0 {
		m.status = "no thread on this row - press g"
		return m, nil
	}
	m.detail = true
	return m, m.syncThread()
}

func (m *model) handleComposeSend(msg composeSendMsg) (tea.Model, tea.Cmd) {
	m.compose.close()
	if m.threadID == 0 {
		// A failed thread load clears the id, so there is nowhere to send. The
		// typed text is gone either way; say so rather than dropping it in
		// silence, and keep the error that explains why the thread is not there.
		m.status = appendStatus(m.status, "cannot reply - the thread is not loaded")
		return m, nil
	}
	threadID, text := m.threadID, msg.text
	return m, func() tea.Msg {
		return sentMsg{err: m.api.SendReply(nil, threadID, text)}
	}
}

// openReply starts a reply to the selected row's thread. A channel row has no
// thread, so there is nothing to reply to.
func (m *model) openReply() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		m.status = "no row selected"
		return m, nil
	}
	if row.ThreadID == 0 {
		m.status = "no thread on this row - press g"
		return m, nil
	}
	m.detail = true
	m.compose.open("reply in " + row.Channel + " › " + row.Thread)
	return m, m.syncThread()
}

// tickInterval is how often the TUI re-reads its rows and the selected
// thread. One unconditional tick replaces a six-field poll whose release
// rules were a documented dead end: a session started in the thread you were
// already looking at was not discovered until you moved away and back.
const tickInterval = 2 * time.Second

type tickMsg struct{}

// tick arms the next refresh. refresh() is its only caller, so the clock is a
// chain of refreshes rather than a timer loop: the clock's rate is the request
// rate and nothing runs behind it.
func (m model) tick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// refresh re-reads the rows and the selected thread, and arms the next tick.
// It is the only place a tick is armed, and refresh is reached from exactly two
// places — Init, which starts the chain, and tickMsg, which is the chain — so
// there is one tick in flight, always, and no response or keypress can fork it.
// A key that triggers its own refetch, g or the rows a sentMsg refreshes, arms
// no tick of its own precisely because the tick is armed here, before the
// request, rather than when a response comes back.
//
// Arming on the response instead is what this used to do, and it made every
// rows response a second clock. A tick already in flight when a user-initiated
// response arrived was joined by another; each of those produced a response that
// armed one more; two became four, then eight, and the TUI hammered the daemon
// instead of reading it. That is also why the rows response arms nothing on its
// error path: the chain does not run through the response, so a DAEMON_DOWN
// cannot stop it.
//
// New messages prepend — the feed is newest-first — so the row the cursor is on
// is still in the list but has moved down by however many arrived, and
// applyRows carries the cursor across by id rather than by index. The window
// follows the carried cursor, which is why a new message at the top does not
// push the row you are reading off the screen.
func (m model) refresh() tea.Cmd {
	return tea.Batch(m.fetchRows(), m.refetchThread(), m.tick())
}

func (m model) nextGranularity() string {
	switch m.granularity {
	case store.GranularityMessage:
		return store.GranularityThread
	case store.GranularityThread:
		return store.GranularityChannel
	default:
		return store.GranularityMessage
	}
}
