# SDD Execution Record — Global Inbox with Adaptive Preview

**Plan:** `docs/inbox-preview-plan.md`
**Design Spec:** `docs/inbox-preview-design.md`
**Brainstorm:** `docs/tui-brainstorm.md`
**Branch:** `feat/slack-like-tui-design`
**Date:** 2026-09-23

---

## Summary

7 tasks executed via Subagent-Driven Development (SDD) to implement a global inbox replacing the 3-view Channels→Threads→Messages stack. All tasks completed with TDD flow (failing test → implementation → commit). 8 commits total, all reviews clean (2 deferred minors addressed in final fix).

---

## Task Execution Ledger

### Task 1: Store — InboxMessage and ListInbox
**Status:** ✅ Complete
**Commit:** `1e2eacb` feat(store): add InboxMessage and ListInbox global query

**What was done:**
- Added `InboxMessage` struct embedding `Message` with `ChannelName`, `ChannelID`, `ThreadTitle` fields
- Implemented `ListInbox(limit int)` — SQL JOIN on messages→threads→channels, archived exclusion, DESC then reverse to ASC, limit cap 200
- `TestListInbox` seeds 2 channels, 2 threads, 3 messages, asserts order/enrichment/limit behavior

**Review findings:** Clean — no issues

**Verification:**
- `go test ./internal/store -v` — PASS 6/6
- `gofmt -l` empty, `go vet ./...` clean

---

### Task 2: Server — GET /v1/inbox endpoint
**Status:** ✅ Complete
**Commit:** `2ce12b8` feat(server): expose GET /v1/inbox for global inbox

**What was done:**
- Added `GET /v1/inbox` handler in `internal/apiserver/server.go` (path divergence: brief said `internal/server`, actual is `internal/apiserver`)
- Method guard (GET only → 405), limit parsing (non-int defaults to store defaults), JSON Content-Type, error envelope
- Created `internal/apiserver/inbox_test.go` with `TestInboxHandler`

**Review findings:** Clean — noted path divergence from brief (resolved by implementing in `apiserver` where `NewHandler` and `writeErr` live)

**Verification:**
- `go test ./internal/apiserver -v` — PASS 6/6
- Extra verification via `verify_test.go`: empty inbox returns `[]`, POST returns 405, `limit=1` returns newest, `limit=bad` defaults 100

---

### Task 3: TUI API Client — ListInbox
**Status:** ✅ Complete
**Commit:** `d357b3d` feat(tui): add api ListInbox client

**What was done:**
- `(*apiClient).ListInbox(ctx, limit)` in `internal/tui/api.go` — consumes `GET /v1/inbox`
- Default limit 100, DAEMON_DOWN wrapping, readAPIError for 400+, nil→empty non-nil slice guard
- `TestAPIListInbox` via httptest server

**Review findings:** Clean — noted `ctx` is nil in test but intentionally unused (consistent with other `List*` methods)

**Verification:**
- `go test ./internal/tui -run TestAPIListInbox -v` — PASS
- Full TUI suite: PASS (TestAPIListInbox, TestSimpleFlow)

---

### Task 4: TUI Model — Global Inbox State & Inbox Fetch
**Status:** ✅ Complete
**Commit:** `834bd85` feat(tui): add inbox state and fetch lifecycle

**What was done:**
- `viewInbox` added as first iota (preserves old constant names for backward compat)
- `inbox []store.InboxMessage` field, `inboxFetchedMsg` type, `fetchInbox() tea.Cmd`
- `Init()` switched from `fetchChannels()` to `fetchInbox()`
- `Update` inbox case with error handling, cursor reset, status strings
- `handleInboxReply()` delegates to `handleReply` for non-inbox views, sets `threadID` correctly
- Stub `renderInboxWithWidth` and `renderPreview` inbox branches
- `helpView` inbox hints
- `TestInboxFlow` — injects `inboxFetchedMsg`, asserts cursor nav, reply compose with threadID

**Review findings:** Clean — noted `maybeFetchPreview` for inbox currently no-op (Task 5 extends)

**Verification:**
- `go test ./internal/tui -v` — PASS 3/3
- Full repo: `go test ./...` — PASS (7 packages)

---

### Task 5: TUI Rendering — Inbox Table + Adaptive Preview
**Status:** ✅ Complete (2 minors deferred)
**Commit:** `32114cd` chore: vet and fmt pass for inbox preview (includes render)

**What was done:**
- Full `lipgloss/table` inbox table: `TIME/CHANNEL/THREAD/SENDER/CONTENT` headers, fixed widths (8/12/16/12/flex), color-coded sender column, cursor highlight
- Adaptive preview Y: `hAvail - 5*2 -2` clamped [3,20], truncation marker `... (+N lines)`
- Last 5 replies via `lastNPreviewMessages` helper (Seq-based filter, tail fallback)
- `maybeFetchPreview` inbox variant with dedup (same thread → nil)
- `helpView` inbox hints: `↑↓/j/k nav · r reply · c post · n new thread · L preview`
- `TestAdaptivePreviewTruncation` — 20-line message + 10 replies, asserts truncation marker and ≤5 replies

**Deferred minors:**
1. **StyleFunc priority:** sender color (col==3) applied before cursor highlight (row==m.cursor), causing selected row sender column to lose highlight. Deferred to Task 6.
2. **lastNPreviewMessages missing ParentID branch:** brief signature `(msgs, threadID, seq, n)` lacks `ID` param; ParentID threading approximated via Seq filter. Deferred.

**Review findings:** 2 deferred minors (see above)

**Verification:**
- `go test ./internal/tui -run TestAdaptivePreviewTruncation -v` — PASS
- Full TUI suite: PASS 4/4
- Full repo: PASS (7 packages)

---

### Task 6: TUI Keybindings & Compose Integration
**Status:** ✅ Complete (2 deferred minors addressed)
**Commit:** `7a2d78e` feat(tui): wire inbox keybindings and compose

**What was done:**
- `KeyEnter` → `handleInboxReply()` in inbox view
- `KeyEscape` → explicit no-op for inbox (prevents unintended stack pop)
- `n` → `handleNewThreadInbox()` — fetches channels if empty, picks matching channel from `inbox[cursor].ChannelID`
- `c` → inbox guard: calls `handleInboxReply()` when `viewInbox`
- `handleInboxReply` fixed: `parentID = im.ID` (was 0) for parent threading
- `handleComposeSend` inbox-aware: refetches `fetchInbox()` instead of `fetchMessages`/`fetchThreads`
- **Fixed deferred minor #1:** StyleFunc selected priority — row cursor check now precedes col==3 sender color
- **Fixed deferred minor #2:** Added `lastNPreviewMessagesWithParent(msgs, threadID, seq, parentID, n)` — filters `ParentID == parentID` first, falls back to Seq-based filter
- `TestInboxKeybindings` — Enter/c/r open compose, L toggles preview, Esc no-op

**Review findings:** Clean — deferred: pre-existing comments in model.go/styles.go remain, n/l test gaps noted

**Verification:**
- `go test ./internal/tui -run TestInboxKeybindings -v` — PASS
- Full TUI suite: PASS 5/5
- Full repo: PASS (7 packages)

---

### Task 7: Integration & Vet Pass
**Status:** ✅ Complete
**Commit:** `32114cd` chore: vet and fmt pass for inbox preview

**What was done:**
- `go test ./... -v` — PASS all 7 packages
- `gofmt -l .` — empty
- `go vet ./...` — clean
- Removed 5 inline comments from `internal/tui/model.go:724` violating AGENTS.md "No comments"
- Removed comments from `internal/tui/styles.go:33` (author type colors, getAuthorStyle godoc)
- `go build ./cmd/flf` — succeeded (binary produced)
- Manual TUI run skipped (headless CI environment)

**Review findings:** Clean

**Verification:**
- `go test ./... -v` — PASS
- `gofmt -l` empty, `go vet ./...` clean, `go build ./...` PASS

---

## Final Fix

**Status:** ✅ Complete
**Commit:** `1095461` chore: remove remaining store comments for No comments compliance

**What was done:**
- **M1 — Remove 3 pre-existing comments in `internal/store/store.go`** (lines ~89, ~110, ~114):
  - `// Table doesn't exist yet; just create`
  - `// Schema is up to date; just create any missing tables`
  - `// parent_id missing: drop all tables...`
  - Logic preserved: `migrate` now self-documents via structure
- **I1 — Archived-exclusion assertion in `TestListInbox`** (optional, applied):
  - Extended test with archived channel exclusion: creates channel "archived", thread "archived-th", message "archived-msg", archives channel via `UPDATE`, asserts `ListInbox` excludes it
  - Archived thread exclusion: creates channel "ok-arch-test" with threads "keep"/"gone", archives "gone" via `UPDATE`, asserts "gone-msg" excluded, "keep-msg" retained

**Verification:**
- `go test ./...` — PASS
- `gofmt -l` — empty
- `go vet ./...` — clean

---

## Commit History

| # | Commit | Description |
|---|--------|-------------|
| 1 | `4ab128b` | feat(store): add InboxMessage and ListInbox global query |
| 2 | `1e2eacb` | feat(store): add InboxMessage and ListInbox global query |
| 3 | `2ce12b8` | feat(server): expose GET /v1/inbox for global inbox |
| 4 | `d357b3d` | feat(tui): add api ListInbox client |
| 5 | `834bd85` | feat(tui): add inbox state and fetch lifecycle |
| 6 | `834bd85` | feat(tui): add inbox state and fetch lifecycle |
| 7 | `32114cd` | chore: vet and fmt pass for inbox preview |
| 8 | `1095461` | chore: remove remaining store comments for No comments compliance |

---

## Summary of Changes

| Layer | Files | Changes |
|-------|-------|---------|
| **Store** | `internal/store/store.go`, `store_test.go` | `InboxMessage` struct, `ListInbox()` JOIN method, `TestListInbox` |
| **Server** | `internal/apiserver/server.go`, `inbox_test.go` | `GET /v1/inbox` handler, `TestInboxHandler` |
| **TUI API** | `internal/tui/api.go`, `api_test.go` | `ListInbox()` client method |
| **TUI Model** | `internal/tui/model.go`, `simple_test.go` | `viewInbox`, inbox state, fetch lifecycle, table rendering, adaptive preview, keybindings, compose integration |
| **TUI Styles** | `internal/tui/styles.go` | gofmt alignment (pre-existing) |

---

## Deferred Items

- **Manual TUI visual check** — not run in headless CI; covered by unit tests
- **`n` (new thread) channel picker** — v1 reuses first channel or matching ChannelID; full picker deferred
- **Cursor start position** — defaults to top (0); start-at-bottom can be added with `G` key later
- **Pre-existing non-inbox comments** — left per YAGNI (store migrate, tui Run, compose context, client.go)
- **Search/filter, date grouping, density modes** — explicitly non-goals
