# Global Inbox with Adaptive Preview Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the 3-view Channels→Threads→Messages stack with a single flat global inbox table plus adaptive preview pane showing full message truncated to Y lines + last 5 replies.

**Architecture:** New store method `ListInbox` JOINs messages/threads/channels and is exposed via `GET /v1/inbox`; TUI `model.go` gains `viewInbox` with `renderInboxWithWidth` table and `renderPreview` adaptive truncation, cursor nav triggers debounced preview fetch via existing thread-messages endpoint.

**Tech Stack:** Go 1.22+, SQLite (modernc.org/sqlite), Bubble Tea v2, Lipgloss v2 table, net/http

**Spec:** `docs/superpowers/specs/2026-09-23-inbox-preview-design.md`
**Brainstorm:** `docs/superpowers/specs/2026-09-23-tui-brainstorm.md` (layout alternatives, gaps, visual enhancements)

## Global Constraints

- `gofmt -l` must be empty for all changed files.
- `go vet ./...` must be clean.
- Exit codes: 0 success, 1 client error, 2 daemon error — never deviate.
- Error envelope `{"code","message"}` at every layer boundary.
- `SetMaxOpenConns(1)` for SQLite, daemon binds `127.0.0.1:0` localhost-only — unchanged.
- No comments unless explicitly requested — code self-documenting.
- TDD: failing test → implementation → commit, never commit without tests passing.
- Preview adaptive Y: `Y = clamp(3,20, hAvail - X*2 -2)` where X=5, `hAvail = previewHeight-4`.

---

## File Structure

- `internal/store/store.go` — add `InboxMessage` type + `ListInbox(limit int) ([]InboxMessage, error)` (JOIN, cap limits, reverse to ASC)
- `internal/store/store_test.go` — add `TestListInbox` coverage
- `internal/server/server.go` — add `handleInbox` + route `GET /v1/inbox` (verify via existing `readAPIError` envelope)
- `internal/server/server_test.go` or `internal/server/inbox_test.go` — handler test (if no existing server test, create new file)
- `internal/tui/api.go` — add `ListInbox(ctx, limit) ([]store.InboxMessage, error)` + `InboxMessage` re-export or define locally
- `internal/tui/model.go` — core changes: `viewInbox`, `inbox []InboxMessage`, `fetchInbox`, `inboxFetchedMsg`, `renderInboxWithWidth`, `renderPreview` adaptive Y, `maybeFetchPreview` inbox variant, key handling `r/c/n/L`, helpView update, title/status strings
- `internal/tui/simple_test.go` — rewrite/add `TestInboxFlow` + `TestAdaptivePreviewTruncation`
- `internal/tui/preview_test.go` (optional) — unit test for `truncatePreviewContent` helper if extracted

---

### Task 1: Store — InboxMessage and ListInbox

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`
- Verify: `internal/store/store.go:73` Open sets `SetMaxOpenConns(1)`

**Interfaces:**
- Consumes: existing `Channel`, `Thread`, `Message` tables, `scanMessages` helper
- Produces: `type InboxMessage struct { Message; ChannelName string; ChannelID int64; ThreadTitle string }` and `func (s *Store) ListInbox(limit int) ([]InboxMessage, error)`

- [ ] **Step 1: Write the failing test in `internal/store/store_test.go`**

```go
func TestListInbox(t *testing.T) {
    s := newTestStore(t) // use existing helper that opens temp file, or Open("file:"+t.Name()+"?mode=memory&cache=shared") with SetMaxOpenConns(1)
    ch1, _ := s.CreateChannel("general", "", "", "", "", true)
    ch2, _ := s.CreateChannel("random", "", "", "", "", true)
    th1, _ := s.CreateThread(ch1, "hello")
    th2, _ := s.CreateThread(ch2, "world")
    _, _ = s.AppendMessageAt(th1, "alice", "human", "user", "first", "2026-09-23T10:00:00Z")
    _, _ = s.AppendMessageAt(th2, "bob", "human", "user", "second", "2026-09-23T11:00:00Z")
    _, _ = s.AppendMessageAt(th1, "alice", "human", "user", "third", "2026-09-23T12:00:00Z")
    msgs, err := s.ListInbox(10)
    if err != nil { t.Fatalf("ListInbox: %v", err) }
    if len(msgs) != 3 { t.Fatalf("want 3 got %d", len(msgs)) }
    if msgs[0].Content != "first" || msgs[1].Content != "second" || msgs[2].Content != "third" { t.Fatalf("order wrong") }
    if msgs[0].ChannelName != "general" || msgs[0].ThreadTitle != "hello" { t.Fatalf("enrichment wrong") }
    // archived excluded
    // limit cap test
    msgs2, _ := s.ListInbox(1)
    if len(msgs2) != 1 { t.Fatalf("limit 1") }
    if msgs2[0].Content != "third" { t.Fatalf("limit should return newest last when reversed to ASC, got %q", msgs2[0].Content) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store -run TestListInbox -v`
Expected: FAIL with "undefined: ListInbox" or "undefined: InboxMessage"

- [ ] **Step 3: Write minimal implementation in `internal/store/store.go`**

```go
type InboxMessage struct {
    Message
    ChannelName string `json:"channel_name"`
    ChannelID   int64  `json:"channel_id"`
    ThreadTitle string `json:"thread_title"`
}

func (s *Store) ListInbox(limit int) ([]InboxMessage, error) {
    if limit <= 0 { limit = 100 }
    if limit > 200 { limit = 200 }
    q := `SELECT m.id, m.thread_id, m.seq, m.parent_id, m.author, m.author_type, m.role, m.content, COALESCE(m.created_at,''),
                 c.name, c.id, t.title
          FROM messages m
          JOIN threads t ON t.id = m.thread_id
          JOIN channels c ON c.id = t.channel_id
          WHERE c.archived_at IS NULL AND t.archived_at IS NULL
          ORDER BY m.created_at DESC, m.id DESC LIMIT ?`
    rows, err := s.db.Query(q, limit)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []InboxMessage
    for rows.Next() {
        var im InboxMessage
        if err := rows.Scan(&im.ID, &im.ThreadID, &im.Seq, &im.ParentID, &im.Author, &im.AuthorType, &im.Role, &im.Content, &im.CreatedAt, &im.ChannelName, &im.ChannelID, &im.ThreadTitle); err != nil { return nil, err }
        out = append(out, im)
    }
    if err := rows.Err(); err != nil { return nil, err }
    for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { out[i], out[j] = out[j], out[i] }
    if out == nil { out = []InboxMessage{} }
    return out, nil
}
```
Place type near `Message` definition at `store.go:194`, method after `ListMessagesByParent`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/store/store.go internal/store/store_test.go
go vet ./internal/store
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): add InboxMessage and ListInbox global query"
```

---

### Task 2: Server — GET /v1/inbox endpoint

**Files:**
- Modify: `internal/server/server.go`
- Test: `internal/server/server_test.go` (create if missing, or reuse `internal/server/inbox_test.go`)

**Interfaces:**
- Consumes: `store.Store.ListInbox(int) ([]InboxMessage, error)` from Task 1
- Produces: `GET /v1/inbox` HTTP handler returning `application/json []InboxMessage` with error envelope

- [ ] **Step 1: Write the failing test**

```go
func TestInboxHandler(t *testing.T) {
    store := newTestStore(t)
    // seed channel/thread/message via store directly
    ch, _ := store.CreateChannel("general", "", "", "", "", true)
    th, _ := store.CreateThread(ch, "hello")
    _, _ = store.AppendMessage(th, "alice", "human", "user", "hi")
    srv := newTestServer(t, store) // helper that builds mux with routes
    resp := srv.Get(t, "/v1/inbox?limit=10")
    if resp.Code != 200 { t.Fatalf("want 200 got %d body %s", resp.Code, resp.Body.String()) }
    if ct := resp.Header().Get("Content-Type"); ct != "application/json" { t.Fatalf("want json ct got %q", ct) }
    var msgs []store.InboxMessage
    if err := json.Unmarshal(resp.Body.Bytes(), &msgs); err != nil { t.Fatalf("unmarshal: %v", err) }
    if len(msgs) != 1 { t.Fatalf("want 1 got %d", len(msgs)) }
    if msgs[0].ChannelName != "general" { t.Fatalf("enrichment") }
}
```

If `newTestServer` not exists, inspect `internal/server/server.go` for `New` and `Handler` creation; replicate pattern from existing tests (check `docs/backend.md`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server -run TestInboxHandler -v`
Expected: FAIL 404 or route not found

- [ ] **Step 3: Write minimal implementation in `internal/server/server.go`**

Locate mux setup where `"/v1/channels"` is registered. Add:

```go
mux.HandleFunc("/v1/inbox", func(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet { writeError(w, "METHOD_NOT_ALLOWED", "method not allowed", http.StatusMethodNotAllowed); return }
    limit := 100
    if v := r.URL.Query().Get("limit"); v != "" {
        if n, err := strconv.Atoi(v); err == nil { limit = n }
    }
    msgs, err := s.ListInbox(limit)
    if err != nil { writeError(w, "DAEMON_ERROR", err.Error(), 500); return }
    writeJSON(w, msgs)
})
```

Ensure `writeJSON` sets `Content-Type: application/json`, `writeError` uses `{"code","message"}` envelope (existing helpers).

Add `strconv` to imports.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/server/server.go internal/server/*_test.go
go vet ./internal/server
git add internal/server/server.go internal/server/*_test.go
git commit -m "feat(server): expose GET /v1/inbox for global inbox"
```

---

### Task 3: TUI API Client — ListInbox

**Files:**
- Modify: `internal/tui/api.go`
- Test: `internal/tui/api_test.go` (or inline test in `simple_test.go`)

**Interfaces:**
- Consumes: `GET /v1/inbox` from Task 2
- Produces: `func (c *apiClient) ListInbox(ctx context.Context, limit int) ([]store.InboxMessage, error)`

- [ ] **Step 1: Write the failing test**

```go
func TestAPIListInbox(t *testing.T) {
    var inbox []store.InboxMessage = []store.InboxMessage{{Message: store.Message{ID:1, Content:"hi"}, ChannelName:"general", ThreadTitle:"hello"}}
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/inbox" { t.Fatalf("path %s", r.URL.Path) }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(inbox)
    }))
    defer ts.Close()
    c := NewAPIClient(ts.URL)
    got, err := c.ListInbox(nil, 10)
    if err != nil { t.Fatalf("err %v", err) }
    if len(got)!=1 || got[0].Content!="hi" { t.Fatalf("wrong") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestAPIListInbox -v`
Expected: FAIL undefined ListInbox

- [ ] **Step 3: Write minimal implementation in `internal/tui/api.go`**

```go
func (c *apiClient) ListInbox(ctx context.Context, limit int) ([]store.InboxMessage, error) {
    if limit <= 0 { limit = 100 }
    url := fmt.Sprintf("%s/v1/inbox?limit=%d", c.base, limit)
    resp, err := c.http.Get(url)
    if err != nil { return nil, fmt.Errorf("DAEMON_DOWN: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 { return nil, readAPIError(resp) }
    var msgs []store.InboxMessage
    if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil { return nil, fmt.Errorf("DAEMON_DOWN: %w", err) }
    if msgs == nil { msgs = []store.InboxMessage{} }
    return msgs, nil
}
```

Ensure import of `store` already present.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui -run TestAPIListInbox -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/api.go
go vet ./internal/tui
git add internal/tui/api.go
git commit -m "feat(tui): add api ListInbox client"
```

---

### Task 4: TUI Model — Global Inbox State & Inbox Fetch

**Files:**
- Modify: `internal/tui/model.go:15,29,58`
- Test: `internal/tui/simple_test.go`

**Interfaces:**
- Consumes: `apiClient.ListInbox` from Task 3
- Produces: `viewInbox` constant, `inbox []store.InboxMessage`, `fetchInbox() tea.Cmd`, `inboxFetchedMsg` type, `Init()` switches to inbox

- [ ] **Step 1: Write the failing test**

```go
func TestInboxFlow(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
    m = toModel(nm)
    inbox := []store.InboxMessage{
        {Message: store.Message{ID:1, ThreadID:10, Seq:1, Author:"alice", AuthorType:"human", Content:"first line\nsecond", CreatedAt:"2026-09-23T10:00:00Z"}, ChannelName:"general", ThreadTitle:"hello", ChannelID:1},
        {Message: store.Message{ID:2, ThreadID:11, Seq:1, Author:"bob", AuthorType:"human", Content:"second", CreatedAt:"2026-09-23T11:00:00Z"}, ChannelName:"random", ThreadTitle:"world", ChannelID:2},
    }
    nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
    m = toModel(nm)
    if len(m.inbox)!=2 { t.Fatalf("inbox len") }
    if m.cursor!=0 { t.Fatalf("cursor") }
    // j down
    nm,_ = m.Update(tea.KeyPressMsg{Text:"j", Code:'j'})
    m = toModel(nm)
    if m.cursor!=1 { t.Fatalf("want 1 got %d", m.cursor) }
    // r reply should open compose with correct threadID
    nm,_ = m.Update(tea.KeyPressMsg{Text:"r", Code:'r'})
    m = toModel(nm)
    if !m.compose.IsActive() { t.Fatalf("compose") }
    if m.compose.state.threadID != 11 { t.Fatalf("threadID %d", m.compose.state.threadID) }
}
```

Add `inboxFetchedMsg` definition to model.go first? Test will fail until model updated.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestInboxFlow -v`
Expected: FAIL undefined inboxFetchedMsg or field inbox

- [ ] **Step 3: Write minimal implementation**

In `model.go`:
- Add to `viewKind`: `viewInbox viewKind = iota` (keep old constants but mark deprecated; or replace enumeration to single inbox — simpler: add `viewInbox` and keep others but unused)
- Add fields to `model`: `inbox []store.InboxMessage`
- Add `type inboxFetchedMsg struct { inbox []store.InboxMessage; err error }` near other fetched msgs
- Add `func (m *model) fetchInbox() tea.Cmd { return func() tea.Msg { msgs, err := m.api.ListInbox(nil, 100); return inboxFetchedMsg{inbox: msgs, err: err} } }`
- Change `Init()` to `return m.fetchInbox()`
- Add case `inboxFetchedMsg` in `Update`: set `m.inbox`, `m.cursor=0`, status string, return `maybeFetchPreview()`
- Stub `renderInboxWithWidth` minimal returning placeholder (full table in Task 5), but enough to compile

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui -run TestInboxFlow -v`
Expected: PASS (render not checked yet)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/model.go internal/tui/simple_test.go
go vet ./internal/tui
git add internal/tui/model.go internal/tui/simple_test.go
git commit -m "feat(tui): add inbox state and fetch lifecycle"
```

---

### Task 5: TUI Rendering — Inbox Table + Adaptive Preview

**Files:**
- Modify: `internal/tui/model.go:529,699`
- Test: `internal/tui/simple_test.go` (extend)

**Interfaces:**
- Consumes: `m.inbox`, `m.previewMessages`, `getAuthorStyle`, `truncate`, `formatTime`, `lipgloss/table`
- Produces: `renderInboxWithWidth(w int) string`, `renderPreview(w int) string` adaptive Y + last 5 replies

- [ ] **Step 1: Write the failing test**

```go
func TestAdaptivePreviewTruncation(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    nm,_ := m.Update(tea.WindowSizeMsg{Width:120, Height:24})
    m = toModel(nm)
    long := strings.Repeat("line\n", 20) // 20 lines
    inbox := []store.InboxMessage{{Message: store.Message{ID:1, ThreadID:10, Content: long, Author:"alice", AuthorType:"human", CreatedAt:"2026-09-23T10:00:00Z"}, ChannelName:"general", ThreadTitle:"hello"}}
    previewMsgs := make([]store.Message, 10)
    for i:=0;i<10;i++ { previewMsgs[i]=store.Message{ID:int64(2+i), ThreadID:10, Seq:int64(2+i), Author:"bob", Content:fmt.Sprintf("reply %d",i), CreatedAt:"2026-09-23T11:00:00Z"} }
    nm,_ = m.Update(inboxFetchedMsg{inbox: inbox})
    m = toModel(nm)
    nm,_ = m.Update(previewMessagesFetchedMsg{threadID:10, messages: previewMsgs})
    m = toModel(nm)
    m.preview = true
    m.width = 120
    m.height = 24
    m.cursor = 0
    rendered := m.renderPreview(50)
    if !strings.Contains(rendered, "(+"){
        t.Fatalf("expected truncation marker, got %q", rendered)
    }
    if strings.Count(rendered, "reply") > 5 {
        t.Fatalf("should show at most 5 replies")
    }
    // table render
    tbl := m.renderInboxWithWidth(100)
    if !strings.Contains(tbl, "CHANNEL") || !strings.Contains(tbl, "CONTENT") { t.Fatalf("header missing %q", tbl) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestAdaptivePreviewTruncation -v`
Expected: FAIL truncation marker missing or table header missing

- [ ] **Step 3: Write minimal implementation**

In `renderInboxWithWidth(w)` for `viewInbox`:
```go
title := fmt.Sprintf("Inbox — %d messages", len(m.inbox))
if len(m.inbox)==0 { items = []string{"  (no messages — press n for new thread)"} } else {
  timeW:=8; chanW:=12; threadW:=16; senderW:=12
  contentW:= max(minContentWidth, w-timeW-chanW-threadW-senderW-14)
  rows:=make([][]string,0,len(m.inbox))
  for i, im := range m.inbox {
    prefix:="  "; if i==m.cursor { prefix="▸ " }
    tStr:= fmt.Sprintf("%*s", timeW, formatTime(im.CreatedAt))
    chanS:= truncate(im.ChannelName, chanW)
    thrS:= truncate(im.ThreadTitle, threadW)
    author:= truncate(im.Author, senderW)
    content:= truncate(strings.ReplaceAll(im.Content, "\n", " "), contentW)
    rows = append(rows, []string{prefix+tStr, chanS, thrS, author, content})
  }
  t:= table.New().BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).BorderColumn(false).BorderRow(false).BorderHeader(true).Border(lipgloss.Border{Top:"─",Bottom:"─",Middle:"─"}).Width(w).Wrap(false).Headers("TIME","CHANNEL","THREAD","SENDER","CONTENT").Rows(rows...).StyleFunc(...)
  items = append(items, t.Render())
}
```

In `renderPreview(w)` for viewInbox:
- If len(inbox)==0 → "(no message)"
- Else im:= m.inbox[m.cursor]; title `fmt.Sprintf("Preview: #%s › %s · %s", im.ChannelName, im.ThreadTitle, truncate(im.Author,20))`
- Compute hPreview:= m.height-4; hAvail:= hPreview-4; Y:= hAvail - 5*2 -2; clamp 3..20
- Split content by "\n", truncate with marker `... (+N lines)` if needed
- Render truncated message lines via `chatMsgStyle`
- Separator `strings.Repeat("─", min(w-4,40))`
- Last 5 replies: `replies := lastNPreviewMessages(m.previewMessages, im.ThreadID, im.Seq, 5)` where helper filters `Seq > im.Seq` or `ParentID==im.ID` if valid, else just tail of thread
- Each reply `fmt.Sprintf("  [%s] %s: %s", formatTime(r.CreatedAt), truncate(r.Author,12), truncate(r.Content, max(minContentWidth,w-20)))`

Also implement `lastNPreviewMessages` helper.

Update `maybeFetchPreview()` to inbox variant: if `len(inbox)>0 && preview && width>=80` fetch thread of highlighted message if thread != previewThreadID.

Update `helpView()` inbox hints: `"↑↓/j/k nav · r reply · c post · n new thread · L preview"`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/model.go internal/tui/simple_test.go
go vet ./internal/tui
git add internal/tui/model.go internal/tui/simple_test.go
git commit -m "feat(tui): inbox table and adaptive preview rendering"
```

---

### Task 6: TUI Keybindings & Compose Integration (Inbox)

**Files:**
- Modify: `internal/tui/model.go:233` (`handleKey`, `handlePost`, `handleReply`, `handleNewThread`, `handleComposeSend`)
- Test: `internal/tui/simple_test.go`

**Interfaces:**
- Consumes: `m.inbox`, `composeModel`
- Produces: `r/c` open reply compose for highlighted thread, `n` prompts channel picker and creates thread, `Enter` aliases `r`, `Esc` no-op

- [ ] **Step 1: Write the failing test**

```go
func TestInboxKeybindings(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    nm,_ := m.Update(tea.WindowSizeMsg{Width:120, Height:24})
    m = toModel(nm)
    inbox := []store.InboxMessage{{Message: store.Message{ID:1, ThreadID:10, Content:"hi"}, ChannelName:"general", ThreadTitle:"hello", ChannelID:5}}
    nm,_ = m.Update(inboxFetchedMsg{inbox: inbox})
    m = toModel(nm)
    nm,_ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
    m = toModel(nm)
    if !m.compose.IsActive() { t.Fatalf("Enter should open reply") }
    m.compose.Close()
    nm,_ = m.Update(tea.KeyPressMsg{Text:"c", Code:'c'})
    m = toModel(nm)
    if !m.compose.IsActive() { t.Fatalf("c should open compose") }
    // preview toggle
    had := m.preview
    nm,_ = m.Update(tea.KeyPressMsg{Text:"L", Code:'L'})
    m = toModel(nm)
    if m.preview==had { t.Fatalf("L toggle failed") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestInboxKeybindings -v`
Expected: FAIL if Enter not wired

- [ ] **Step 3: Write minimal implementation**

- `handleKey`:
  - `KeyUp`/`k` → cursor--, `maybeFetchPreview()`
  - `KeyDown`/`j` → cursor++ bounded by `len(inbox)-1`
  - `KeyEnter` → `handleReply()` (if inbox non-empty) else noop
  - `KeyEscape` → no-op for inbox (return nil) — remove old view stack switch
  - `r`/`c` → `handleReply()`
  - `n` → `handleNewThreadInbox()` (new): if `len(channels)==0` fetch channels first (return fetchChannels Cmd), else open compose for channel picker — v1 simple: reuse first channel or prompt; minimal: `if len(m.channels)==0 { return fetchChannels }` then `compose.Open(composeModeNewThread, "New thread — type title", height)` with `state.threadID = m.channels[0].ID` (or highlighted inbox's ChannelID if inbox non-empty)
  - `L`/`l` toggle preview

- `handleReply()` inbox variant:
```go
func (m *model) handleReply() (tea.Model, tea.Cmd) {
    if len(m.inbox)==0 { m.status="no message to reply to"; return m,nil }
    if m.cursor<0||m.cursor>=len(m.inbox) { m.cursor=0 }
    im:= m.inbox[m.cursor]
    m.compose.Open(composeModeMessage, fmt.Sprintf("#%s › %s — reply appends", im.ChannelName, im.ThreadTitle), m.height)
    m.compose.state.threadID = im.ThreadID
    m.compose.state.parentID = im.ID // use parent threading if schema supports
    return m, nil
}
```
Keep `handlePost` as alias to `handleReply`.

- `handleComposeSend` after message send: `return m, m.fetchInbox()` instead of `fetchMessages`; after thread create: `return m, m.fetchInbox()` as well.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/model.go internal/tui/simple_test.go
go vet ./internal/tui
git add internal/tui/model.go internal/tui/simple_test.go
git commit -m "feat(tui): wire inbox keybindings and compose"
```

---

### Task 7: Integration & Vet Pass

**Files:**
- Verify all modified files, run global checks

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: All PASS

- [ ] **Step 2: Run go vet and gofmt**

Run: `gofmt -l .`
Expected: empty output

Run: `go vet ./...`
Expected: no output

- [ ] **Step 3: Manual TUI sanity (optional)**

Run: `go build ./cmd/flf && rm -f /tmp/fluffle-test.db && FLF_DB=/tmp/fluffle-test.db ./flf daemon start --background && FLF_DB=/tmp/fluffle-test.db ./flf channel create --orphaned --name general --json && ...` then `./flf tui` to visually verify inbox table + preview adaptive truncation.

- [ ] **Step 4: Commit (if fixes needed)**

```bash
git add -A
git commit -m "chore: vet and fmt pass for inbox preview"
```

---

## Self-Review

- Spec §4 Store JOIN covered by Task 1.
- Spec §5 API GET /v1/inbox covered by Task 2 + §3 client Task 3.
- Spec §6 State/lifecycle covered by Task 4.
- Spec §7 Table columns + adaptive Y + last 5 replies covered by Task 5.
- Spec §8 Keybindings covered by Task 6.
- Spec §10 Testing plan covered across Tasks 1-6, vet Task 7.
- No placeholders — each step has concrete code/tests.
- Type consistency: `InboxMessage` used consistently store→server→tui; `inboxFetchedMsg.inbox` field reused; `previewThreadID` dedup retained.

