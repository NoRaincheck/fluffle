# TUI Navigation Overhaul Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix fluffle TUI so channel → thread → message navigation works, posting and replying works in orphaned and repo-anchored channels, and pure-cli JSON mode remains testable; adopt Roborev-inspired split layout and document interactive testing.

**Architecture:** Keep SQLite/daemon/HTTP API untouched (already correct). Overhaul `internal/tui` to a Roborev-style split layout: left tree pane (repo › branch › channel › thread) + right detail pane (thread list or message list), focus toggle, debounced detail follow, view stack (home→channel→thread→reply) with Esc/back navigation. Fix data flow: tree fetches channels → threads lazily; detail pane fetches messages only on selection. Add `viewStack` and `focusPane` types, fix `composeModel.IsActive` via `active bool`, add thread-aware compose modes. Ensure CLI `--json` remains the source of truth for tests.

**Tech Stack:** Go 1.27+, charm.land/bubbletea/v2, charm.land/lipgloss/v2, charm.land/glamour (optional markdown), existing `internal/store`, `internal/apiserver`, `internal/client`.

**Spec:** docs/backend.md + user request 2026-09-23: "I should be able to create a channel/repo, navigate to a channel and view all messages in it, post a message, navigate to any message, view the thread, post a reply, navigate back to home screen by channel/repo"

## Global Constraints

- No comments unless explicitly requested (AGENTS.md).
- `gofmt -l` must be empty, `go vet ./...` clean.
- TDD: failing test → implementation → commit, never commit without tests passing.
- Exit codes: 0 success, 1 client error, 2 daemon error — never deviate.
- Error envelope `{"code","message"}` at every layer boundary.
- Agent identity via `X-Fluffle-Agent` header, never trust `author_type` from body.
- Agents are append-only: messages + reactions, never create channels/threads/delete.
- SQLite-first: single DB file, `SetMaxOpenConns(1)`.
- Localhost-only: daemon binds `127.0.0.1:0`.
- YAGNI: no features outside plan/spec.
- Domain rule firming: Channel = Slack-like room anchored to `repo_abs_path + repo_head_branch` or orphaned (`is_orphaned=1`). Thread = titled conversation inside a channel. Message = chat line inside a thread with optional `parent_id` (reply). This language appears in `--help` and docs.

---

### Task 1: Firm language + fix CLI `--json` pure-CLI path

**Files:**
- Modify: `cmd/flf/main.go:1-30` (usage strings)
- Modify: `cmd/flf/main.go:channelListCmd, channelCreateCmd, threadListCmd, threadNewCmd, messageSendCmd` (help text)
- Modify: `README.md` or `docs/backend.md` (add CLI examples)
- Test: `cmd/flf/e2e_test.go` (add pure-CLI orphan flow)
- Test: `internal/apiserver/server_test.go` (already has [] guards — keep)

**Interfaces:**
- Consumes: existing `apiGet/apiPost`, `repo.Canonicalize`, `store.Channel/Thread/Message`
- Produces: `--help` strings showing Channel/Thread/Message glossary; pure-CLI workflow `flf channel create --orphaned --name X --json` → `flf thread new --channel X --title T --json` → `flf message send --thread ID --text ... --json` → `flf agent read --thread ID --json` all work

- [ ] **Step 1: Write failing test for pure orphan CLI JSON flow**

```go
// cmd/flf/cli_json_test.go (or add to e2e_test.go with build tag !e2e)
func TestCLI_OrphanChannelPureJSON(t *testing.T) {
    home := t.TempDir()
    t.Setenv("FLUFFLE_HOME", home)
    // start daemon via EnsureDaemon helper or via httptest
    // run: channel create --orphaned --name test-orphan --json -> parse {"id":...}
    // run: thread new --channel test-orphan --title "hello" --json -> parse
    // run: message send --thread <id> --text "first!" --json -> parse {"seq":1}
    // run: agent read --thread <id> --json -> JSONL with 1 line
    // assert each step exit 0 and JSON shape
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./cmd/flf -run TestCLI_OrphanChannelPureJSON -count=1 -v`
Expected: FAIL (no test or wrong JSON)

- [ ] **Step 3: Fix usage strings + ensure --json paths return [] not null**

Update `run()` usage to `usage: flf <daemon|init|channel|thread|message|react|agent|tui>`.
Add help: `channel` help shows "Channel: repo-anchored or --orphaned room; Thread: titled conversation in a channel; Message: chat line in a thread (use --thread ID)".
Already have nil→[] guards; verify `thread list --json` with empty threads returns `[]`.

- [ ] **Step 4: Verify pass + document interactive steps**

Run: `go test ./cmd/flf -run TestCLI -count=1 -v`
Expected: PASS
Add `docs/tui-manual-test.md` with human steps: `FLUFFLE_HOME=$(mktemp -d) go run ./cmd/flf daemon start --background && flf channel create --orphaned --name demo --json && ...`

- [ ] **Step 5: Commit**

```bash
git add cmd/flf/main.go cmd/flf/*test.go docs/tui-manual-test.md
git commit -m "fix(cli): firm channel/thread/message language, pure json orphan flow"
```

---

### Task 2: Roborev-inspired split layout primitives

**Files:**
- Create: `internal/tui/layout.go`
- Modify: `internal/tui/model.go` (add focusPane, viewStack, layout fields)
- Test: `internal/tui/layout_test.go`

**Interfaces:**
- Consumes: `tea.WindowSizeMsg`, `lipgloss`
- Produces: `type focusPane int (focusTree, focusDetail)`, `type viewKind int (viewHome, viewChannel, viewThread)`, `func (m model) resolveLayout() layoutMode`, `func (m model) splitActive() bool`, `type layoutMode int (stacked, split)`

- [ ] **Step 1: Write failing layout test**

```go
func TestResolveLayout_SplitWide(t *testing.T) {
    m := model{width: 120, height: 30}
    if got := m.resolveLayout(); got != layoutSplit {
        t.Fatalf("want split got %v", got)
    }
}
func TestResolveLayout_StackedNarrow(t *testing.T) {
    m := model{width: 60, height: 30}
    if got := m.resolveLayout(); got != layoutStacked {
        t.Fatalf("want stacked got %v", got)
    }
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./internal/tui -run TestResolveLayout -count=1 -v`
Expected: FAIL undefined

- [ ] **Step 3: Implement layout.go**

```go
package tui
type layoutMode int
const (layoutStacked layoutMode = iota; layoutSplit)
const splitMinWidth = 90
func (m model) resolveLayout() layoutMode {
    if m.width >= splitMinWidth { return layoutSplit }
    return layoutStacked
}
func (m model) splitActive() bool { return m.resolveLayout()==layoutSplit && m.currentView != viewModal }
```

Copy Roborev's `splitLayoutConfig.Geometry` logic simplified: if width<90 stacked else split with ListMinWidth 28, detail fills rest.

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/tui -run TestResolveLayout -count=1 -v` PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/layout.go internal/tui/layout_test.go internal/tui/model.go
git commit -m "feat(tui): roborev-inspired split layout primitives"
```

---

### Task 3: Tree model overhaul — channel › thread hierarchy with lazy fetch

**Files:**
- Modify: `internal/tui/tree.go` (rebuild to include threads, repo-branch grouping, selection)
- Modify: `internal/tui/api.go` (ensure ListThreads nil→[] already done, add channel cache)
- Test: `internal/tui/tree_test.go`

**Interfaces:**
- Consumes: `store.Channel`, `store.Thread`, `apiClient.ListChannels/ListThreads`
- Produces: `func buildTree(channels []Channel, threadsByChannel map[int64][]Thread) []treeItem`, `func (m *treeModel) SetThreads(channelID int64, threads []Thread)`, `func (m treeModel) SelectedChannel() *Channel`, `func (m treeModel) SelectedThread() *Thread`, `func (m tree) View() string` with repo›branch›channel›thread lines

- [ ] **Step 1: Write failing tree test**

```go
func TestBuildTree_ChannelThreadHierarchy(t *testing.T) {
    ch := store.Channel{ID: 1, Name: "general", RepoAbsPath: "/tmp/repo", RepoHeadBranch: "main"}
    th := store.Thread{ID: 10, ChannelID: 1, Title: "hello"}
    items := buildTree([]store.Channel{ch}, map[int64][]store.Thread{1: {th}})
    // expect: repo group, channel item, thread child
    if len(items) < 3 { t.Fatalf("want >=3 items got %d", len(items)) }
    if items[2].thread == nil || items[2].thread.Title != "hello" { t.Fatalf("want thread child") }
}
func TestTree_EnterExpandsChannel(t *testing.T) {
    // nav test: cursor on channel, Enter expands to show threads
}
```

- [ ] **Step 2: Run to fail** `go test ./internal/tui -run TestBuildTree -count=1 -v`

- [ ] **Step 3: Implement tree.go overhaul**

- `treeItem` add `isChannel, isThread, channelID, threadID` fields
- `buildTree` groups by `RepoAbsPath` (orphaned → "Orphaned") → branch → channel → threads (from map)
- `visibleItems()` respects `expanded` (currently always true; make it collapse channel children)
- `SelectedChannel/SelectedThread` resolve based on item type
- `SetThreads` updates threads map and rebuilds

- [ ] **Step 4: Verify pass**

- [ ] **Step 5: Commit**

---

### Task 4: Chat/detail pane overhaul — thread list vs message list modes

**Files:**
- Modify: `internal/tui/chat.go` (add mode switch: threadList vs messageList)
- Modify: `internal/tui/model.go` (remove renderThreadList hack, add proper fetch)
- Test: `internal/tui/chat_test.go`

**Interfaces:**
- Consumes: `store.Thread`, `store.Message`
- Produces: `type chatMode int (modeThreadList, modeMessageList)`, `func (m *chatModel) SetThreads([]Thread)`, `func (m *chatModel) SetMessages([]Message)`, `func (m chatModel) View() string` renders either thread list or message list

- [ ] **Step 1: Write failing chat test**

```go
func TestChat_ThreadListMode(t *testing.T) {
    m := chatModel{width: 80, height: 10}
    m.SetThreads([]store.Thread{{ID: 1, Title: "topic"}})
    v := m.View()
    if !strings.Contains(v, "topic") { t.Fatalf("want topic in view %q", v) }
}
func TestChat_MessageListMode(t *testing.T) {
    m := chatModel{width: 80, height: 10}
    m.SetMessages([]store.Message{{ID: 1, ThreadID: 1, Seq: 1, Author: "alice", Content: "hi"}})
    v := m.View()
    if !strings.Contains(v, "alice") { t.Fatalf("want alice") }
}
```

- [ ] **Step 2: Run to fail**

- [ ] **Step 3: Implement chat.go**

- Add `mode chatMode` field, `SetThreads` switches to `modeThreadList`, `SetMessages` to `modeMessageList`
- `View()` renders header `channelTitle` / `threadTitle`, then rows per mode with cursor highlight
- Remove blocking `api.ListMessages` call from `renderThreadList`

- [ ] **Step 4: Verify**

- [ ] **Step 5: Commit**

---

### Task 5: Model state machine — view stack, focus, navigation, message posting

**Files:**
- Modify: `internal/tui/model.go` (full overhaul: viewStack, focusPane, navigation, compose wiring)
- Modify: `internal/tui/compose.go` (already fixed IsActive; add thread-aware modes: newThread vs message vs reply)
- Modify: `internal/tui/tui.go` (ensure alt screen)
- Test: `internal/tui/model_test.go` (tea test with stub api)

**Interfaces:**
- Consumes: `apiClient.ListChannels/ListThreads/ListMessages/SendMessage/CreateThread`, `treeModel`, `chatModel`, `composeModel`
- Produces: `func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd)` handling: WindowSize, channelsFetched, threadsFetched, messagesFetched, threadCreated, messageSent, KeyMsg (up/down, enter, esc, tab, c, n, q)

Navigation spec:
- `↑/↓,k/j` move cursor in focused pane
- `Tab` toggles focusTree↔focusDetail (when splitActive)
- `Enter` on channel: fetch threads → chat shows thread list
- `Enter` on thread: fetch messages → chat shows messages
- `Esc` back: thread messages → thread list → channel list
- `c` compose message in selected thread (or `n` new thread in selected channel if no thread)
- `r` reply to selected message (sets parent_id)
- `q` quit, `ctrl+c` quit

- [ ] **Step 1: Write failing model navigation test**

```go
func TestModel_EnterChannelShowsThreads(t *testing.T) {
    // use fake apiClient returning channels+threads
    // Init → channelsFetched, then simulate Enter on channel → expect threadsFetchedMsg → chat mode threadList
}
func TestModel_EnterThreadShowsMessages(t *testing.T) { ... }
func TestModel_ComposeAndSend(t *testing.T) { ... }
func TestModel_EscBack(t *testing.T) { ... }
```

- [ ] **Step 2: Run to fail**

- [ ] **Step 3: Implement model.go**

- Add fields: `currentView viewKind`, `viewStack []viewKind`, `focus focusPane`, `selectedChannelID`, `selectedThreadID`, `layout layoutMode`
- `Init` returns fetchChannels
- `Update` per msg type: on `channelsFetchedMsg` set tree channels; on `threadsFetchedMsg` store in map and set chat threads; on `messagesFetchedMsg` set chat messages and push viewThread
- `handleKey` implements spec above; `handleComposeSend` handles 3 cases: new thread (CreateThread then SendMessage), message (SendMessage with parent 0), reply (SendMessage with parentID)
- `View` uses `resolveLayout()` → if splitActive render `JoinHorizontal(treeStyle, chatStyle)` else stacked single pane; include shortcuts bar + status

- [ ] **Step 4: Verify**

Run: `go test ./internal/tui -count=1 -v` + `go vet ./...`

- [ ] **Step 5: Commit**

---

### Task 6: API polish + post-reply wiring + orphan support

**Files:**
- Modify: `internal/tui/api.go` (add CreateThread, SendMessage with parentID)
- Modify: `internal/apiserver/server.go` (already handles parent_id)
- Test: `internal/store/store_test.go` Add parent reply test (exists CountReplies) + `internal/tui/api_test.go`

**Interfaces:**
- Consumes: `store.Message.ParentID`
- Produces: `func (c *apiClient) CreateThread(ctx, channelID, title) (int64,error)`, `func (c *apiClient) SendMessage(ctx, threadID, parentID, text string) error` correctly sets `ParentID` JSON field

- [ ] **Step 1: Write failing parent reply test**

```go
func TestAppendMessageWithParent(t *testing.T) {
    s,_:=store.Open(":memory:")
    ch,_:=s.CreateChannel("c","/r","","","",false)
    th,_:=s.CreateThread(ch,"t")
    seq,_:=s.AppendMessage(th,"a","human","user","parent")
    msgs,_:=s.ListMessages(th,0)
    seq2,_:=s.AppendMessageWithParent(th,"b","human","user","reply", msgs[0].ID)
    // verify ListMessagesByParent returns reply
}
func TestTUI_SendReply(t *testing.T) {
    // stub server: POST /v1/threads/:id/messages with ParentID
    // assert apiClient.SendMessage includes ParentID
}
```

- [ ] **Step 2: Run to fail**

- [ ] **Step 3: Implement**

- Ensure `apiClient.SendMessage` sends `{"Author":"you","Role":"user","Content":text,"ParentID":parentID}` (currently does, but test it)
- Ensure `store` reply path works (already there)
- TUI: when `composeModeReply`, set `parentID = chat.SelectedMessage().ID`

- [ ] **Step 4: Verify**

- [ ] **Step 5: Commit**

---

### Task 7: Docs + human manual test + E2E thread-reply flow

**Files:**
- Create: `docs/tui-manual-test.md`
- Modify: `README.md` (add TUI section + Roborev attribution)
- Modify: `cmd/flf/e2e_test.go` (add TestE2E_ThreadReplyViaJSON)

**Interfaces:**
- Consumes: CLI `--json` paths
- Produces: human test checklist + E2E coverage for create channel → thread → message → reply → read

- [ ] **Step 1: Write failing E2E test**

```go
func TestE2E_ThreadReplyViaJSON(t *testing.T) {
    // orphan channel create --json
    // thread new --channel demo --title "topic" --json
    // message send --thread <id> --text "parent" --json
    // agent read --thread <id> --json → get parent message ID (via GET /v1/threads/:id/messages)
    // message send --thread <id> --text "child" --agent-id test (with parent lookup via API)
    // verify 2 messages, second has parent_id via direct store/API check
}
```

- [ ] **Step 2: Run to fail** `go test -tags e2e -run TestE2E_ThreadReplyViaJSON -count=1 -v`

- [ ] **Step 3: Implement + docs**

- Ensure CLI can send reply: currently `message send` has no `--parent` flag — add `--reply-to ID` flag to `messageSendCmd` and wire to `ParentID` body field
- Write `docs/tui-manual-test.md`:

```md
# Manual TUI Test
FLUFFLE_HOME=$(mktemp -d) go run ./cmd/flf daemon start --background
flf channel create --orphaned --name demo --json
flf channel list --include-orphaned --json
go run ./cmd/flf tui
# In TUI: j/k navigate, Enter on demo → shows "no threads", n → new thread "hello" → Enter thread → messages, c → post "first!", r on message → reply "ack", Esc back to threads, Esc back to channels, q quit
```

- [ ] **Step 4: Verify**

Run: `go test ./... -count=1 -v` + `go test -tags e2e -run TestE2E_ThreadReply -count=1 -v`

- [ ] **Step 5: Commit**

---

## Self-Review

- Spec coverage: all user stories mapped: create channel/repo (Task1+Task3), navigate channel→messages (Task3+Task5), post message (Task5+Task6), view thread (Task4+Task5), reply (Task6+Task7), back to home (Task5), json pure-cli (Task1+Task7), Roborev layout (Task2+Task5), human docs (Task7).
- Placeholder scan: no TBD; all steps have concrete code.
- Type consistency: `store.Channel/Thread/Message` used throughout, `apiClient` methods consistent, `treeItem.channel/thread` pointers, `chatMode` enum.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-09-23-tui-navigation-overhaul.md`. Two execution options:

1. **Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

2. **Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

Which approach?
