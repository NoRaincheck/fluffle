package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/screen"
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

func (m model) Init() tea.Cmd { return m.fetchRows() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.compose.resize(m.width)
		return m, nil
	case rowsFetchedMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		if msg.granularity != m.granularity {
			return m, nil
		}
		m.applyRows(msg.rows)
		return m, tea.Batch(m.syncThread(), m.tick())
	case threadFetchedMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			// A failed load must not leave the previous thread's messages
			// under the newly selected thread's title, and clearing
			// threadID lets the next syncThread retry instead of treating
			// the failure as loaded.
			m.thread, m.threadID = nil, 0
			return m, nil
		}
		if msg.threadID != m.threadID {
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
	case tea.KeyMsg:
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
		return screen.OverlayCentered(m.bodyView(), m.compose.view(), m.width, m.height)
	}
	return m.bodyView()
}

// bodyView is title, body, hint, and status: chromeH rows of chrome around
// exactly the rest. In a split terminal the thread sits beside the list; in a
// stacked one it replaces the list; either way it is the same renderThread.
func (m model) bodyView() string {
	h := m.height - chromeH
	var body string
	switch {
	case m.detail:
		body = renderThread(m.paneWidth(), h, m.threadTitle(), m.thread)
	case m.split():
		body = sideBySide(
			renderRows(ListW, h, m.rows, m.cursor, m.scroll),
			renderThread(m.paneWidth(), h, m.threadTitle(), m.thread))
	default:
		body = renderRows(m.width, h, m.rows, m.cursor, m.scroll)
	}
	return strings.Join([]string{
		m.titleLine(),
		body,
		dimStyle.Render("↑↓ nav · g group · v sort · Enter read · r reply · q quit"),
		statusStyle.Width(m.width - 2).Render(termtext.Truncate(m.statusLine(), m.width-2, "…")),
	}, "\n")
}

// paneWidth is the width the thread is drawn at, whether it sits beside the
// list or fills the terminal. It is the only place that arithmetic lives, so
// both placements cannot drift apart.
func (m model) paneWidth() int {
	if m.split() && !m.detail {
		return m.width - ListW
	}
	return m.width
}

// sideBySide puts the thread pane to the right of the list, one list row and
// one thread row per output row. Joining the two blocks instead would stack
// them, which costs a row and is not a split.
func sideBySide(list, thread string) string {
	l, r := strings.Split(list, "\n"), strings.Split(thread, "\n")
	rows := make([]string, 0, max(len(l), len(r)))
	for i := range max(len(l), len(r)) {
		rows = append(rows, rowAt(l, i)+rowAt(r, i))
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
// thread side by side. Task 8 uses it.
func (m model) split() bool { return m.width >= MinSplitWidth }

func (m model) titleLine() string {
	order := "newest first"
	if m.reversed {
		order = "oldest first"
	}
	return titleStyle.Render(fmt.Sprintf("flf · %s · %d rows · %s", m.granularity, len(m.rows), order))
}

func (m model) statusLine() string {
	if m.status != "" {
		return m.status
	}
	return ""
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
	return fmt.Sprintf("flf needs %d columns and %d rows (got %dx%d) — resize the terminal",
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
	visible := max(m.height-chromeH, 1)
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

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	case "esc":
		m.detail = false
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
		m.status = "no thread on this row — press g"
		return m, nil
	}
	m.detail = true
	return m, m.syncThread()
}

func (m *model) handleComposeSend(msg composeSendMsg) (tea.Model, tea.Cmd) {
	m.compose.close()
	if m.threadID == 0 {
		return m, nil
	}
	threadID, text := m.threadID, msg.text
	return m, func() tea.Msg {
		return sentMsg{err: m.api.SendReply(nil, threadID, text)}
	}
}

// tick is the poll clock. Task 10 replaces this stub with the real tick.
func (m model) tick() tea.Cmd { return nil }
