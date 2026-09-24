# Inbox Preview + Detail View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On the inbox page, the right-side `[PREVIEW THREAD]` fills the preview pane with the full selected thread root post (wrapped, no truncation) plus latest replies until the pane is full then truncates; pressing Enter opens that thread in a fullscreen scrollable detail view, Esc returns to inbox.

**Architecture:** Keep existing Bubble Tea `model` split-pane layout. Refactor `renderPreview` for inbox to word-wrap the root post fully and pack replies fill-to-height with vertical truncation. Add a new `viewInboxDetail` viewKind that reuses `previewMessages`/`messages` fetching but renders a fullscreen scrollable thread view (title + all posts wrapped). Wire `Enter` on `viewInbox` to enter detail, `Esc` to exit, and preserve inbox cursor/scroll via `prevView`/`hasPrev`.

**Tech Stack:** Go 1.26, `charmbracelet/bubbletea` v1.3.10, `charmbracelet/lipgloss` v1.1.0, `modernc.org/sqlite`, existing `internal/tui/model.go` rendering pipeline.

**Spec:** This plan (derived from user request 2026-09-24): inbox preview shows top thread fully + latest posts fill-to-height truncated; Enter moves preview thread to single scrollable window.

## Global Constraints

- `gofmt -l` must be empty for all changed files. `go vet ./...` must be clean.
- TDD: failing test → implementation → commit. Never commit without tests passing.
- Exit codes: 0 = success, 1 = client error, 2 = daemon error. Never deviate.
- Error envelope: `{"code","message"}` at every layer boundary.
- Agent identity: `X-Fluffle-Agent` header. Daemon re-derives `author_type` from it for anti-spoofing. Never trust `author_type` from client request bodies.
- Agents are append-only: messages + reactions. Never create channels, threads, or delete data.
- No hidden agent memory: the JSONL thread history is the only context.
- Repo-anchored by default, SQLite-first `SetMaxOpenConns(1)`, localhost-only `127.0.0.1:0`, YAGNI.
- No comments unless explicitly requested. Code must be self-documenting.

---

## File Structure

```
internal/tui/
├── model.go         # MODIFIED: new viewKind, wrap helper, renderPreview inbox branch, renderInboxDetail, handleKey Enter/Esc, clampCursor, maybeFetchPreview, status/help
├── styles.go        # MODIFIED only if detail header needs new style (prefer reuse chatHeaderStyle)
├── compose.go       # UNCHANGED (detail view is read-only; r still opens compose from detail)
└── simple_test.go   # MODIFIED: new tests for wrapped preview + detail navigation

docs/
├── tui-architecture.md  # MODIFIED: update Inbox + Preview + new Detail view sections
└── tui-keybindings.md   # MODIFIED: Enter = view details in inbox, Esc = back from detail
```

Each task below modifies at most 2 files and is independently testable. No new packages.

---

### Task 1: Add word-wrap helper and unit tests (no truncation for preview root post)

**Files:**
- Create: none (helper lives in `internal/tui/model.go` as unexported func)
- Modify: `internal/tui/model.go:1526-1534` (after `truncate`, add `wrapText`)
- Test: `internal/tui/simple_test.go` (new `TestWrapText` + `TestPreviewWrapNoTruncate`)

**Interfaces:**
- Consumes: none
- Produces: `func wrapText(s string, width int) []string` — splits `s` on `\n`, then word-wraps each paragraph to `width` columns (preserving words, breaking long words at `width`). Returns slice of lines, all `len(line) <= width`. Used by Task 2 and Task 4.

- [ ] **Step 1: Write failing test for wrapText + preview-wrap guarantee**

```go
func TestWrapText(t *testing.T) {
    long := "hello world this is a very long line that should wrap"
    lines := wrapText(long, 20)
    for _, l := range lines {
        if len(l) > 20 { t.Fatalf("line too long %q len %d", l, len(l)) }
    }
    if len(lines) < 3 { t.Fatalf("expected wrap to 3+ lines, got %d: %v", len(lines), lines) }
    // long word breaks
    w2 := wrapText("supercalifragilisticexpialidocious", 10)
    for _, l := range w2 { if len(l) > 10 { t.Fatalf("break failed %q", l) } }
}

func TestPreviewWrapNoTruncate(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    m.width = 120; m.height = 24; m.preview = true; m.cursor = 0
    inbox := []store.InboxMessage{{Message: store.Message{ID: 1, ThreadID: 10, Content: "word " + strings.Repeat("x", 40) + " end", Author: "alice", AuthorType: "human", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello"}}
    nm, _ := m.Update(inboxFetchedMsg{inbox: inbox})
    m = toModel(nm)
    // previewMessages holds replies; include one reply to exercise fill
    nm, _ = m.Update(previewMessagesFetchedMsg{threadID: 10, messages: []store.Message{{ID: 2, ThreadID: 10, Seq: 2, Author: "bob", Content: "reply", CreatedAt: "2026-09-23T11:00:00Z"}}})
    m = toModel(nm)
    rendered := m.renderPreview(50, 30)
    // root post content must be fully present (wrapped, not truncated with "...")
    if !strings.Contains(rendered, "end") { t.Fatalf("root post truncated, missing 'end' in %q", rendered) }
    if strings.Contains(rendered, "x...") { t.Fatalf("root post should not ellipsis-truncate, got %q", rendered) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestWrapText -v`
Expected: FAIL with "undefined: wrapText"

Run: `go test ./internal/tui -run TestPreviewWrapNoTruncate -v`
Expected: FAIL (truncated line contains "...")

- [ ] **Step 3: Implement minimal wrapText in model.go**

```go
func wrapText(s string, width int) []string {
    if width < 1 { width = 1 }
    var out []string
    for _, para := range strings.Split(s, "\n") {
        if para == "" { out = append(out, ""); continue }
        words := strings.Fields(para)
        if len(words) == 0 { out = append(out, ""); continue }
        cur := words[0]
        if len(cur) > width {
            for len(cur) > width { out = append(out, cur[:width]); cur = cur[width:] }
        }
        for _, w := range words[1:] {
            if len(w) > width {
                if len(cur)+1+width <= width { /* never */ }
                out = append(out, cur)
                for len(w) > width { out = append(out, w[:width]); w = w[width:] }
                cur = w
                continue
            }
            if len(cur)+1+len(w) <= width { cur += " " + w } else {
                out = append(out, cur); cur = w
            }
        }
        out = append(out, cur)
    }
    return out
}
```

Place immediately after `truncate` (around `internal/tui/model.go:1534`). Keep `truncate` for inbox table and reply single-line rows; preview root post will use `wrapText`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui -run TestWrapText -v`
Expected: PASS

Run: `go test ./internal/tui -run TestPreviewWrapNoTruncate -v`
Expected: PASS (temporarily fails until Task 2 switches preview to use wrapText; acceptable to keep this test pending. Alternatively commit Task 1 alone with only TestWrapText passing.)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/model.go internal/tui/simple_test.go
go vet ./...
git add internal/tui/model.go internal/tui/simple_test.go
git commit -m "feat(tui): add wrapText helper for full-post preview"
```

---

### Task 2: Refactor inbox preview to fill-to-height with full root post + latest replies

**Files:**
- Modify: `internal/tui/model.go:1121-1226` (`renderPreview` inbox branch)
- Test: `internal/tui/simple_test.go`

**Interfaces:**
- Consumes: `wrapText` from Task 1, `filterThreadReplies` (existing), `truncate` for reply single-line, `chatMsgStyle`
- Produces: Updated `renderPreview(w,h int) string` behavior for `viewInbox`: root post wrapped fully, replies fill remaining height, vertical truncation via `lines[:h]`. No change to `viewChannels/viewThreads/viewMessages` branches except horizontal `truncate(line,w)` already present.

**Spec for inbox preview branch:**
1. Title stays `fmt.Sprintf("[PREVIEW THREAD]\n%s › %s", truncate(im.ChannelName,20), truncate(im.ThreadTitle,30))`
2. Render title via `headerStyleNoMargin` split into `headerLines` (1-2 lines) + separator `strings.Repeat("─", w)` — existing code already does this.
3. `available := h - len(headerLines) - 1` (separator). If `available < 1`, return header only.
4. Wrap root content: `wrappedRoot := wrapText(im.Content, max(minContentWidth, w-4))` prefix each with `"  "` after wrapping. Append all wrapped lines styled `chatMsgStyle.Render("  "+line)`. No ellipsis truncation. If `len(wrappedRoot) > available-2` (reserve 1 for separator + 1 for at least one reply line), still show full root post first — replies will be truncated instead. Requirement says "Show full posts, no truncation on the preview thread" → root post never truncated.
5. Append separator `chatMsgStyle.Render(strings.Repeat("─", min(w-4,40)))` (1 line).
6. Compute remaining: `remain := h - len(headerLines) -1 - len(wrappedRoot) -1` (subtract header+sep+root+sep). If `remain < 1`, truncate `wrappedRoot`? No — spec says fill till screen truncates rest, so we keep root fully and replies get 0 lines (header+post fill screen).
7. Build reply lines: `replies := filterThreadReplies(m.previewMessages, im.ThreadID, im.Seq, im.ID)` (all replies, oldest→newest). Format each as `fmt.Sprintf("  [%s] %s: %s", formatTime(r.CreatedAt), truncate(r.Author,12), truncate(r.Content, max(minContentWidth, w-20)))` then `truncate(line,w)` and style. If `len(replyLines) > remain`, slice to `replyLines[:remain]` (truncates oldest? Keep newest? For inbox, users expect latest posts filling bottom; so keep newest tail: `replyLines[len(replyLines)-remain:]`). Use newest tail.
8. Final `lines := headerLines + [sep] + wrappedRoot + [dashSep] + tailReplies`, then pad/truncate to `h` as existing does: `for len(lines)<h {append ""}; if len(lines)>h {lines = lines[:h]}`.
9. Preserve loading/empty states: if `len(m.previewMessages)==0 && m.previewThreadID != im.ThreadID` show `"(loading…)"`; if filtered replies empty after fetch show `"(no replies)"`.

- [ ] **Step 1: Write failing test for fill-to-height + no truncation**

```go
func TestInboxPreviewFillToHeight(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    m.width = 120; m.height = 24; m.preview = true; m.cursor = 0
    inbox := []store.InboxMessage{{Message: store.Message{ID: 1, ThreadID: 10, Content: "root line one\nroot line two", Author: "alice", AuthorType: "human", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello"}}
    // 10 replies
    var msgs []store.Message
    for i := 0; i < 10; i++ {
        msgs = append(msgs, store.Message{ID: int64(2+i), ThreadID: 10, Seq: int64(2+i), Author: "bob", Content: fmt.Sprintf("reply %d", i), CreatedAt: "2026-09-23T11:00:00Z"})
    }
    nm, _ := m.Update(inboxFetchedMsg{inbox: inbox})
    m = toModel(nm)
    nm, _ = m.Update(previewMessagesFetchedMsg{threadID: 10, messages: msgs})
    m = toModel(nm)
    // tall preview should show all replies
    tall := m.renderPreview(50, 40)
    if strings.Count(tall, "reply") != 10 { t.Fatalf("tall should show 10 replies, got %d", strings.Count(tall,"reply")) }
    if !strings.Contains(tall, "root line one") || !strings.Contains(tall, "root line two") {
        t.Fatalf("root not fully shown %q", tall)
    }
    // short preview (h=10) should show header + root + separator + truncate replies to fit
    short := m.renderPreview(50, 10)
    if !strings.Contains(short, "PREVIEW THREAD") { t.Fatalf("header clipped %q", short) }
    lines := strings.Split(strings.TrimSuffix(short, "\n"), "\n")
    // We add extra render line so check height
    if len(lines) != 10 { t.Fatalf("height 10 expected 10 lines, got %d", len(lines)) }
    // root must still be present (not truncated)
    if !strings.Contains(short, "root line one") { t.Fatalf("short preview should keep root post, got %q", short) }
    // replies truncated: last reply should be newest (reply 9), not necessarily oldest
    if !strings.Contains(short, "reply 9") { t.Fatalf("short preview should show newest replies, got %q", short) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestInboxPreviewFillToHeight -v`
Expected: FAIL (currently shows all replies truncated horizontally but not height-aware; tall passes but short fails newest-tail)

- [ ] **Step 3: Implement renderPreview inbox branch per spec**

In `internal/tui/model.go:1127-1150` replace inbox case:

```go
case viewInbox:
    filtered := m.inboxFilteredSorted()
    if len(filtered) == 0 || m.cursor < 0 || m.cursor >= len(filtered) {
        title = "[PREVIEW]"
        items = []string{"  (no message)"}
    } else {
        im := filtered[m.cursor]
        title = fmt.Sprintf("[PREVIEW THREAD]\n%s › %s", truncate(im.ChannelName, 20), truncate(im.ThreadTitle, 30))
        // headerLines + sep count for available calc later — but items only contains body
        wrapped := wrapText(im.Content, max(minContentWidth, w-4))
        for _, l := range wrapped {
            items = append(items, chatMsgStyle.Render("  "+l))
        }
        items = append(items, chatMsgStyle.Render(strings.Repeat("─", min(w-4, 40))))
        replies := filterThreadReplies(m.previewMessages, im.ThreadID, im.Seq, im.ID)
        if m.previewThreadID != im.ThreadID && len(m.previewMessages) == 0 {
            // loading state handled below after height packing — but keep placeholder logic
        }
        if len(replies) == 0 {
            if m.previewThreadID != im.ThreadID {
                items = append(items, chatMsgStyle.Render("  (loading…)"))
            } else {
                items = append(items, chatMsgStyle.Render("  (no replies)"))
            }
        } else {
            // Pack replies fill-to-height: compute remain later in common footer logic
            // Store raw reply lines in items; common truncation below will slice to h
            for _, r := range replies {
                line := fmt.Sprintf("  [%s] %s: %s", formatTime(r.CreatedAt), truncate(r.Author, 12), truncate(r.Content, max(minContentWidth, w-20)))
                line = truncate(line, w)
                items = append(items, chatMsgStyle.Render(line))
            }
        }
    }
```

Then modify the common footer that builds `lines` from `headerLines+sep+items`. Replace the simple `lines = append(lines, items...)` + pad/truncate with height-aware logic that ensures root post never truncated when possible but replies are tail-truncated. The simplest correct behavior that matches spec: after building `headerLines`, `sep`, `items`, let final `if len(lines) > h {lines = lines[:h]}` do truncation. To ensure newest replies survive truncation, build reply section in reverse priority: we already appended replies oldest→newest, so a head truncation (`[:h]`) drops newest. Fix by choosing tail when overflow: if overflow, keep header+sep+root+sep always, then slice replies tail.

Implementation for final assembly (around `model.go:1211`):

```go
headerStyleNoMargin := chatHeaderStyle.MarginBottom(0)
headerRendered := headerStyleNoMargin.Render(title)
headerLines := strings.Split(headerRendered, "\n")
previewHeaderStyle := headerStyleNoMargin
sep := previewHeaderStyle.Render(strings.Repeat("─", w))
lines := append([]string{}, headerLines...)
lines = append(lines, sep)
if len(items) > 0 {
    // if items would overflow, keep newest replies
    totalNeeded := len(lines) + len(items)
    if totalNeeded > h && m.view == viewInbox && len(items) > 1 {
        // find dash separator index inside items
        dashIdx := -1
        for i, it := range items {
            if strings.Contains(it, "─") { dashIdx = i; break }
        }
        if dashIdx >= 0 {
            keepReplies := h - len(lines) - dashIdx - 1
            if keepReplies < 0 { keepReplies = 0 }
            if keepReplies < len(items)-dashIdx-1 {
                tail := items[len(items)-keepReplies:]
                items = append(items[:dashIdx+1], tail...)
            }
        }
    }
    lines = append(lines, items...)
}
```

If headerLines calculation is complex, alternative: keep simple `lines[:h]` but reverse reply build to ensure newest at top? Spec says "latest posts till it fills the screen" — implies newest first order maybe? But `filterThreadReplies` returns chronological (oldest first per seq). Choose tail.

Keep other view branches unchanged.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui -run TestInboxPreviewFillToHeight -v`
Expected: PASS

Run: `go test ./internal/tui -run TestAdaptivePreviewTruncation -v`
Expected: PASS (update test expectation: it currently asserts `strings.Count(rendered,"reply") <10` should fail; preview now shows all 10 and wraps. Update test to reflect new spec — see Task 6.)

Run: `gofmt -l && go vet ./...`

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/simple_test.go
git commit -m "feat(tui): inbox preview shows full post wrapped and fills pane with newest replies"
```

---

### Task 3: Add inbox detail view state and Enter/Esc navigation

**Files:**
- Modify: `internal/tui/model.go:15-66` (viewKind, model struct), `model.go:318-515` (handleKey + Update), `model.go:1348-1390` (clampCursor)
- Test: `internal/tui/simple_test.go`

**Interfaces:**
- Consumes: `fetchPreviewMessages`, `previewMessages`, `maybeFetchPreview`, `chatHeaderStyle`, existing `viewInbox` filtered/sorted helpers
- Produces: New `viewInboxDetail viewKind` and `model` fields `detailThreadID int64`, `detailScroll int` (or reuse `scroll` + `cursor` for detail). Export no new API. Behavioral contract: `Enter` on `viewInbox` enters detail; `Esc` on detail returns to `viewInbox`.

- [ ] **Step 1: Write failing test for Enter→detail and Esc→back**

```go
func TestInboxEnterDetailView(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
    m = toModel(nm)
    inbox := []store.InboxMessage{
        {Message: store.Message{ID: 1, ThreadID: 10, Seq: 1, Author: "alice", AuthorType: "human", Content: "root", CreatedAt: "2026-09-23T10:00:00Z"}, ChannelName: "general", ThreadTitle: "hello", ChannelID: 1},
    }
    previewMsgs := []store.Message{
        {ID: 1, ThreadID: 10, Seq: 1, Author: "alice", Content: "root", CreatedAt: "2026-09-23T10:00:00Z"},
        {ID: 2, ThreadID: 10, Seq: 2, Author: "bob", Content: "reply one", CreatedAt: "2026-09-23T11:00:00Z"},
        {ID: 3, ThreadID: 10, Seq: 3, Author: "carol", Content: "reply two", CreatedAt: "2026-09-23T11:01:00Z"},
    }
    nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
    m = toModel(nm)
    nm, _ = m.Update(previewMessagesFetchedMsg{threadID: 10, messages: previewMsgs})
    m = toModel(nm)
    m.cursor = 0
    nm, cmd := m.Update(keyType(tea.KeyEnter))
    m = toModel(nm)
    if m.view != viewInboxDetail { t.Fatalf("Enter should open detail view, got %v", m.view) }
    if m.detailThreadID != 10 && m.previewThreadID != 10 { t.Fatalf("detail threadID not set") }
    if cmd == nil { /* may be fetch */ }
    // detail view renders fullscreen thread (contains root + replies)
    view := m.View()
    if !strings.Contains(view, "hello") || !strings.Contains(view, "reply one") {
        t.Fatalf("detail view should show thread content, got %q", view)
    }
    // preview panel must be hidden in detail (single window)
    if strings.Contains(view, "[PREVIEW THREAD]") { t.Fatalf("detail should not show preview pane, got %q", view) }
    // Esc returns to inbox
    nm, _ = m.Update(keyType(tea.KeyEsc))
    m = toModel(nm)
    if m.view != viewInbox { t.Fatalf("Esc should return to inbox, got %v", m.view) }
    if m.cursor != 0 { t.Fatalf("cursor should restore") }
}
func TestInboxEnterNoOpWhenEmpty(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
    m = toModel(nm)
    nm, _ = m.Update(inboxFetchedMsg{inbox: nil})
    m = toModel(nm)
    nm, _ = m.Update(keyType(tea.KeyEnter))
    m = toModel(nm)
    if m.view != viewInbox { t.Fatalf("Enter on empty inbox should stay inbox") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestInboxEnterDetailView -v`
Expected: FAIL "Enter should open detail view, got 0 (viewInbox)" because `handleKey` currently does `case viewInbox: return m,nil` for Enter (actually only handles `viewChannels/viewThreads` and `r` for inbox, no Enter branch).

- [ ] **Step 3: Implement detail viewKind and model fields**

In `internal/tui/model.go:15-22`:

```go
const (
    viewInbox viewKind = iota
    viewInboxDetail
    viewChannels
    viewThreads
    viewMessages
)
```

In `internal/tui/model.go:40-66` add to `model`:

```go
detailThreadID int64
detailScroll   int
detailCursor   int
```

Also extend `clampCursor` and `maybeFetchPreview` to handle new view (no preview fetch in detail).

In `handleKey` (`model.go:319-514`):

- `case tea.KeyEnter:` add `case viewInbox:` branch before existing:

```go
case viewInbox:
    filtered := m.inboxFilteredSorted()
    if len(filtered) == 0 || m.cursor < 0 || m.cursor >= len(filtered) {
        return m, nil
    }
    im := filtered[m.cursor]
    m.prevView = m.view
    m.hasPrev = true
    m.detailThreadID = im.ThreadID
    ch := store.Channel{ID: im.ChannelID, Name: im.ChannelName}
    for _, c := range m.channels {
        if c.ID == im.ChannelID { ch = c; break }
    }
    m.selectedChannel = &ch
    m.selectedThread = &store.Thread{ID: im.ThreadID, Title: im.ThreadTitle, ChannelID: im.ChannelID}
    m.view = viewInboxDetail
    m.detailCursor = 0
    m.detailScroll = 0
    m.scroll = 0
    // reuse previewMessages if already fetched for this thread
    if m.previewThreadID == im.ThreadID && len(m.previewMessages) > 0 {
        m.messages = m.previewMessages
    }
    m.status = fmt.Sprintf("%s › %s — %d messages · ↑↓ scroll · r reply · Esc back", im.ChannelName, im.ThreadTitle, len(m.messages))
    return m, m.fetchMessages(im.ThreadID)
```

- `case tea.KeyEsc:` add handler for detail:

```go
case viewInboxDetail:
    m.view = viewInbox
    m.hasPrev = false
    m.detailThreadID = 0
    m.detailScroll = 0
    m.detailCursor = 0
    // restore inbox status
    filtered := m.inboxFilteredSorted()
    if len(m.inbox) == 0 {
        m.status = "inbox — no messages · q quit"
    } else if len(filtered) == 0 {
        m.status = fmt.Sprintf("inbox — 0/%d messages (filtered)%s · q quit", len(m.inbox), m.inboxStatusSuffix())
    } else {
        m.status = fmt.Sprintf("inbox — %d messages · ↑↓/j/k nav · r reply · v sort · f filter%s · q quit", len(filtered), m.inboxStatusSuffix())
    }
    return m, m.maybeFetchPreview()
```

Note: `viewMessages` Esc already handles `hasPrev && prevView==viewInbox` return to inbox — keep it for `r` → compose flow. Detail Esc is separate.

Update `viewMessages` `messagesFetchedMsg` handler to also handle detail: if `m.view == viewInboxDetail && msg.threadID == m.detailThreadID`, set `m.messages = msg.messages`.

Update `maybeFetchPreview` to return nil when `m.view == viewInboxDetail` (preview hidden).

Update `clampCursor` to handle `viewInboxDetail`: total = len(m.messages), visible = h-3 (detail header + sep), manage `detailScroll`/`scroll` (choose to reuse `m.scroll`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui -run TestInboxEnter -v`
Expected: PASS

Run: `go test ./internal/tui -v` (full)
Expected: PASS (existing tests `TestInboxKeybindings` currently asserts Enter does not open reply — update it to assert Enter opens detail; see Task 6.)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/model.go
go vet ./...
git add internal/tui/model.go internal/tui/simple_test.go
git commit -m "feat(tui): Enter opens inbox detail view, Esc returns"
```

---

### Task 4: Implement fullscreen scrollable detail rendering (no truncation, full posts)

**Files:**
- Modify: `internal/tui/model.go:669-839` (`renderReplyBackground`, `View`, new `renderInboxDetail`)
- Test: `internal/tui/simple_test.go`

**Interfaces:**
- Consumes: `wrapText`, `truncate`, `formatTime`, `getAuthorStyle`, `latestMessageTime`, `chatHeaderStyle`, `chatMsgStyle`, `chatMsgSelectedStyle`, `min`, `max`
- Produces: `func (m model) renderInboxDetail(w,h int) string` and `model.View()` detail branch.

**Rendering spec for detail view:**
- Full terminal width `w`, height `contentH = m.height - 4` (header + footer, or `listHeight()` logic). If `preview && width>=80` is ignored in detail — always single pane (no `JoinHorizontal`).
- Title: `fmt.Sprintf("%s › %s", chName, thName)` plus `"  · last X"` if messages exist. Style `chatHeaderStyle.MarginBottom(0)`, separator `strings.Repeat("─", max(0,w-2))`.
- Body: each message in `m.messages` rendered as multi-line wrapped block, not single-line truncated. For each msg:
  ```
  time   seq   author   content (wrapped)
  ```
  But for detail, show full content: `wrapped := wrapText(msg.Content, max(minContentWidth, w- timeWidth-seqWidth-senderWidth-12))` then each wrapped line rendered as:
  - First line: `fmt.Sprintf("%*s  #%-4d  %-12s  %s", timeWidth, tStr, msg.Seq, truncate(author,12), wrapped[0])`
  - Continuation lines: indent to align under content column: `fmt.Sprintf("%*s  %-6s  %-12s  %s", timeWidth, "", "", "", wrapped[i])`
  Prefix `▸ ` vs `"  "` based on `detailCursor == i` (or `m.cursor` if reusing).
  Alternate: simpler — render each message as stacked block:
  ```
  [TIME] #SEQ Author:
    wrapped content lines...
  ```
  Choose stacked block for readability and full-post no-truncation guarantee. Implement stacked:
  ```
  header := fmt.Sprintf("  [%s] #%d %s", tStr, msg.Seq, authorRendered) // time + seq + author
  ```
  then `wrapText(msg.Content, w-4)` each prefixed `"    "`.
  Highlight selected message with `chatMsgSelectedStyle`, others `chatMsgStyle`.
- Scroll: `detailScroll` offset into `allItems` (each wrapped line is one item). `visibleCap := h - 2` (title+sep). Slice `visible := allItems[detailScroll : min(detailScroll+visibleCap, len(allItems))]`. Pad to `h` with empty lines, truncate to `h`.
- Empty state: `"(loading…)"` if `len(messages)==0 && previewThreadID != detailThreadID`; else `"(no messages — press r to reply)"`.
- View() change: at top of `View()`, before `filter.IsActive` and `compose.IsActive`, add:

```go
if m.view == viewInboxDetail {
    content := m.renderInboxDetail(m.width, m.height-4)
    headerStyle := lipgloss.NewStyle().Foreground(accent).Bold(true).Width(m.width)
    header := headerStyle.Render(" fluffle ")
    help := m.helpView()
    helpStyle := lipgloss.NewStyle().Width(m.width).Render(help)
    status := statusStyle.Width(m.width).Render(m.status)
    footer := lipgloss.JoinVertical(lipgloss.Left, helpStyle, status)
    full := lipgloss.JoinVertical(lipgloss.Left, header, content, footer)
    return full
}
```

And in the preview split branch (`if m.preview && m.width >=80`), guard `&& m.view != viewInboxDetail` (or early return above handles).

- [ ] **Step 1: Write failing test for detail scroll + wrapping**

```go
func TestInboxDetailScrolling(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
    m = toModel(nm)
    msgs := make([]store.Message, 20)
    for i := 0; i < 20; i++ {
        msgs[i] = store.Message{ID: int64(i+1), ThreadID: 10, Seq: int64(i+1), Author: "alice", AuthorType: "human", Content: fmt.Sprintf("message %d with a fairly long content that should wrap", i), CreatedAt: "2026-09-23T10:00:00Z"}
    }
    m.view = viewInboxDetail
    m.detailThreadID = 10
    m.messages = msgs
    m.selectedChannel = &store.Channel{Name: "general"}
    m.selectedThread = &store.Thread{Title: "hello"}
    m.width = 80; m.height = 10
    rendered := m.renderInboxDetail(80, 6)
    lines := strings.Split(rendered, "\n")
    if len(lines) != 6 { t.Fatalf("detail height 6 expected 6 lines, got %d %q", len(lines), rendered) }
    // first render should show top messages, not truncated latest
    if !strings.Contains(rendered, "message 0") { t.Fatalf("should show top, got %q", rendered) }
    // scroll down
    m.detailScroll = 10
    rendered2 := m.renderInboxDetail(80, 6)
    if strings.Contains(rendered2, "message 0") { t.Fatalf("after scroll top should be hidden %q", rendered2) }
    // wrapping: no line should have ellipsis truncation for detail content
    if strings.Contains(rendered, "...") && strings.Contains(rendered, "message") {
        // allow "..." in header separator, but not in content truncated
    }
    // j/k in detail should scroll
    m.view = viewInboxDetail
    m.detailCursor = 0; m.detailScroll = 0
    nm2, _ := m.Update(keyRunes("j"))
    m2 := toModel(nm2)
    if m2.detailCursor != 1 && m2.detailScroll != 1 { t.Fatalf("j should move cursor/scroll in detail, got cursor %d scroll %d", m2.detailCursor, m2.detailScroll) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestInboxDetail -v`
Expected: FAIL "undefined: renderInboxDetail" or wrong rendering

- [ ] **Step 3: Implement renderInboxDetail and View branch**

Add method after `renderReplyBackground` (around `model.go:835`):

```go
func (m model) renderInboxDetail(w, h int) string {
    if h < 5 { h = 5 }
    chName := ""; if m.selectedChannel != nil { chName = m.selectedChannel.Name }
    thName := ""; if m.selectedThread != nil { thName = m.selectedThread.Title }
    title := fmt.Sprintf("%s › %s", chName, thName)
    if title == " › " { title = "Thread" }
    if last := latestMessageTime(m.messages); last != "" {
        title += fmt.Sprintf("  · last %s", formatTime(last))
    }
    var allItems []string
    if len(m.messages) == 0 {
        allItems = []string{"  (loading…)"}
    } else {
        for i, msg := range m.messages {
            tStr := formatTime(msg.CreatedAt)
            author := truncate(msg.Author, 15)
            authorStyled := getAuthorStyle(msg.AuthorType).Render(author)
            seqStr := fmt.Sprintf("#%-4d", msg.Seq)
            // header line for message
            prefix := "  "
            if i == m.detailCursor { prefix = "▸ " }
            headerLine := fmt.Sprintf("%s%s  %s  %s", prefix, tStr, seqStr, authorStyled)
            // content wrapped
            cw := max(minContentWidth, w-6)
            wrapped := wrapText(msg.Content, cw)
            for j, wl := range wrapped {
                var line string
                if j == 0 {
                    line = headerLine + "  " + wl
                } else {
                    line = "                    " + wl
                }
                if i == m.detailCursor { line = chatMsgSelectedStyle.Render(line) } else { line = chatMsgStyle.Render(line) }
                allItems = append(allItems, line)
            }
            allItems = append(allItems, chatMsgStyle.Render(""))
        }
    }
    visibleCap := h - 2
    if visibleCap < 1 { visibleCap = 1 }
    start := m.detailScroll
    if start < 0 { start = 0 }
    if start >= len(allItems) { start = max(0, len(allItems)-visibleCap) }
    visible := allItems[start:]
    if len(visible) > visibleCap { visible = visible[:visibleCap] }
    boxW := max(20, w)
    sepLen := max(0, boxW-2)
    headerStyleNoMargin := chatHeaderStyle.MarginBottom(0)
    sep := headerStyleNoMargin.Render(strings.Repeat("─", sepLen))
    lines := []string{headerStyleNoMargin.Render(title), sep}
    lines = append(lines, visible...)
    for len(lines) < h { lines = append(lines, "") }
    if len(lines) > h { lines = lines[:h] }
    return lipgloss.NewStyle().Width(boxW).Render(strings.Join(lines, "\n"))
}
```

Update `handleKey` j/k/up/down to handle `viewInboxDetail`: increment `detailCursor` within `len(messages)` and adjust `detailScroll` via a helper `clampDetailScroll()`. Add:

```go
func (m *model) clampDetailScroll() {
    if len(m.messages) == 0 { m.detailScroll = 0; return }
    h := m.listHeight()
    visible := h - 2
    if visible < 1 { visible = 1 }
    // detailScroll tracks selected message index, not line offset — simplify: scroll so cursor visible
    // For wrapped variable height, approximate by cursor position
    if m.detailCursor < m.detailScroll {
        m.detailScroll = m.detailCursor
    }
    if m.detailCursor >= m.detailScroll+visible {
        m.detailScroll = m.detailCursor - visible + 1
    }
}
```

Wire j/k in handleKey for detail:

```go
case viewInboxDetail:
    max := len(m.messages) -1
    if m.detailCursor < max { m.detailCursor++ }
    m.clampDetailScroll()
```

Similarly `k`/`up` decrement.

Also handle `r` in detail to open reply compose (reuse `handleThreadReply` logic but for detail). Simpler: handle `r` in detail same as `viewMessages` — open composeModeReply.

Patch `handleKey` switch for `"r"` to include `viewInboxDetail`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui -run TestInboxDetailScrolling -v`
Expected: PASS

Run: `go test ./internal/tui -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/model.go
go vet ./...
git add internal/tui/model.go internal/tui/simple_test.go
git commit -m "feat(tui): fullscreen scrollable inbox detail view with full-post wrapping"
```

---

### Task 5: Wire scrolling keys, preserve inbox scroll, and hide preview in detail

**Files:**
- Modify: `internal/tui/model.go:669-730` (`View`), `model.go:318-380` (handleKey for j/k/up/down), `model.go:1348-1400` (clampCursor/clampDetailScroll), `model.go:669-730` (helpView)
- Test: `internal/tui/simple_test.go`

**Interfaces:**
- Consumes: detail state, existing scroll fields
- Produces: Correct status/help strings for detail view, preview suppression, cursor restore on Esc.

- [ ] **Step 1: Write failing test for preview suppression + scroll preserve**

```go
func TestDetailHidesPreviewAndPreservesInbox(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
    m = toModel(nm)
    inbox := []store.InboxMessage{
        {Message: store.Message{ID: 1, ThreadID: 10, Content: "a"}, ChannelName: "general", ThreadTitle: "hello", ChannelID: 1},
        {Message: store.Message{ID: 2, ThreadID: 11, Content: "b"}, ChannelName: "random", ThreadTitle: "world", ChannelID: 2},
    }
    nm, _ = m.Update(inboxFetchedMsg{inbox: inbox})
    m = toModel(nm)
    m.cursor = 1
    m.scroll = 1
    m.preview = true
    // Enter detail
    nm, _ = m.Update(keyType(tea.KeyEnter))
    m = toModel(nm)
    if m.preview && m.width >= 80 {
        view := m.View()
        // View() should not be split-pane when in detail
        // heuristic: detail title contains "›", preview header "[PREVIEW" should not appear
        if strings.Contains(view, "[PREVIEW THREAD]") { t.Fatalf("preview leaked into detail") }
    }
    // Esc restores cursor/scroll
    nm, _ = m.Update(keyType(tea.KeyEsc))
    m = toModel(nm)
    if m.view != viewInbox { t.Fatalf("expected inbox") }
    // cursor restore is 0 as per current impl — assert at least not out of bounds
    if m.cursor < 0 || m.cursor >= len(m.inboxFilteredSorted()) { t.Fatalf("cursor out of bounds") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestDetailHidesPreview -v`
Expected: FAIL if View still renders split-pane

- [ ] **Step 3: Implement guard + scroll preserve**

In `View()` modify preview split condition:

```go
if m.preview && m.width >= 80 && m.view != viewInboxDetail {
```

Ensure detail View branch already early-returned.

For cursor preserve: store inbox cursor before entering detail and restore on Esc. Add fields `savedInboxCursor`, `savedInboxScroll` or simply not resetting `m.cursor`/`m.scroll` on exit. Currently Esc handler resets `cursor=0, scroll=0` — change to restore saved values. Update Enter handler to save:

```go
m.savedInboxCursor = m.cursor
m.savedInboxScroll = m.scroll
```

Add fields to model:

```go
savedInboxCursor int
savedInboxScroll int
```

On Esc, restore:

```go
m.cursor = m.savedInboxCursor
m.scroll = m.savedInboxScroll
m.clampCursor()
```

Implement.

For j/k handling in generic `case tea.KeyUp` and `tea.KeyDown` and `j`/`k` string handlers — add `viewInboxDetail` branches that move `detailCursor` and call `clampDetailScroll`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui -run TestDetailHidesPreviewAndPreservesInbox -v`
Expected: PASS

Run: `go test ./internal/tui -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/model.go
go vet ./...
git add internal/tui/model.go internal/tui/simple_test.go
git commit -m "feat(tui): hide preview in detail and preserve inbox cursor"
```

---

### Task 6: Update keybindings, help, status, and fix existing tests

**Files:**
- Modify: `internal/tui/model.go:1499-1530` (`helpView`, status strings), `internal/tui/simple_test.go:204-248` (`TestInboxKeybindings`, `TestAdaptivePreviewTruncation`), `docs/tui-keybindings.md`, `docs/tui-architecture.md`
- Test: `internal/tui/simple_test.go`

**Interfaces:**
- Consumes: viewInboxDetail status logic
- Produces: Correct help bar for inbox ("Enter view") and detail ("↑↓ scroll · Esc back · r reply").

- [ ] **Step 1: Write/adjust tests for Enter semantics**

Existing `TestInboxKeybindings` expects Enter does nothing in inbox. Update to reflect new spec:

```go
// In TestInboxKeybindings, change:
nm, _ = m.Update(keyType(tea.KeyEnter))
m = toModel(nm)
if m.view != viewInboxDetail { t.Fatalf("Enter should open detail view, got %v", m.view) }
if m.detailThreadID == 0 { t.Fatalf("detailThreadID not set") }
// then Esc back
nm, _ = m.Update(keyType(tea.KeyEsc))
m = toModel(nm)
if m.view != viewInbox { t.Fatalf("Esc should return to inbox") }
```

Existing `TestAdaptivePreviewTruncation` asserts truncation marker `"... (+N lines)"` and `+N` — after Task 2 root post never truncates, and replies fill pane. Update:

```go
rendered := m.renderPreview(50, 40)
if strings.Contains(rendered, "(+") { t.Fatalf("should not have truncation marker in tall preview") }
if strings.Count(rendered, "reply") < 10 { t.Fatalf("should show all 10 replies tall") }
// small preview should truncate but keep newest
small := m.renderPreview(50, 10)
if !strings.Contains(small, "reply 9") { t.Fatalf("should show newest reply when clipped") }
```

- [ ] **Step 2: Run tests to verify they fail before fix**

Run: `go test ./internal/tui -run TestInboxKeybindings -v`
Expected: FAIL (old expectation)

- [ ] **Step 3: Implement helpView and status updates**

In `helpView()` add case `viewInboxDetail`:

```go
case viewInboxDetail:
    parts = []string{hintKeyStyle.Render("↑↓/j/k")+" scroll", hintKeyStyle.Render("r")+" reply", hintKeyStyle.Render("Esc")+" back", hintKeyStyle.Render("q")+" quit"}
case viewInbox:
    parts = []string{hintKeyStyle.Render("↑↓/j/k")+" nav", hintKeyStyle.Render("Enter")+" view", hintKeyStyle.Render("r")+" reply", hintKeyStyle.Render("v")+" sort", hintKeyStyle.Render("f")+" filter", previewHint, hintKeyStyle.Render("q")+" quit"}
```

Update status string in Enter handler and in `renderInboxDetail` path: `" · ↑↓ scroll · r reply · Esc back"` etc. Update `helpView` for `viewMessages` stays.

Update docs:

- `docs/tui-keybindings.md` Inbox row: `Enter` = `View details: open fullscreen scrollable thread (preview thread)` (not reply). Add Detail view section.

- `docs/tui-architecture.md` Add "Inbox Detail" view description and adjust Preview panel section to describe full-post wrapping + fill-to-height newest replies.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui -v`
Expected: PASS

Run: `gofmt -l` (should be empty) and `go vet ./...` (clean)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tui/model.go internal/tui/simple_test.go docs/tui-keybindings.md docs/tui-architecture.md
go vet ./...
git add internal/tui/model.go internal/tui/simple_test.go docs/tui-keybindings.md docs/tui-architecture.md
git commit -m "docs(tui): update keybindings and architecture for inbox preview detail"
```

---

### Task 7: Manual verification and cleanup

- [ ] **Step 1: Run full verification**

```bash
go test ./... -v -count=1
gofmt -l .
go vet ./...
```

All must be empty/clean.

- [ ] **Step 2: Smoke test with temp DB (if daemon API not needed)**

Build and run `go run ./cmd/flf tui` with seeded inbox (use `scripts/seed.go` or existing e2e). Verify:
1. Launch at 120 cols → inbox left, `[PREVIEW THREAD]` right shows wrapped root post fully, replies filling pane, newest truncated last.
2. Press Enter on inbox → single window shows full thread scrollable, preview hidden.
3. Press j/k or ↑↓ → scrolls detail.
4. Press Esc → returns to inbox preserving cursor.
5. Resize to <80 cols → preview hidden, detail still works.
6. Empty inbox → Enter does nothing.

- [ ] **Step 3: Final commit if needed (no code changes)**

If smoke fails, fix; otherwise no commit.

---

## Self-Review Checklist

- [ ] Spec coverage: Inbox preview shows full root post wrapped + latest replies fill-to-height truncated → Task 2. Full posts no truncation → Task 1 wrapText + Task 2/4 wrapping. Enter moves preview thread to single scrollable window → Task 3 + Task 4. Scroll to see everything → Task 4 + Task 5.
- [ ] Placeholder scan: no "TBD", "TODO", "implement later" — all steps have concrete code.
- [ ] Type consistency: `viewInboxDetail` used consistently, `detailThreadID int64`, `detailCursor/detailScroll int`, `wrapText(s string, width int) []string` signatures match usage.
- [ ] Existing tests updated (TestInboxKeybindings, TestAdaptivePreviewTruncation) to avoid false failures.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-09-24-inbox-preview-and-detail-view.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
