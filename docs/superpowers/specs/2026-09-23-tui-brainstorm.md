# TUI Brainstorm: Gaps & Alternative Layouts

## Current State Analysis

The current TUI has a **3-view stack** (Channels → Threads → Messages) with optional preview panel. Key characteristics:
- Single cursor navigation (↑↓/j/k)
- Preview panel on right when width ≥ 80 cols
- Compose modal overlay
- Status bar at bottom
- **Table-style message view** with columns: Time | Sender | Content
- **Color-coded senders**: Blue (human), Purple (agent), Gray (system)

---

## 🔍 IDENTIFIED GAPS

### 1. ~~**Sender Identity Not Glancible**~~ ✅ ADDRESSED
**Status:** Implemented with color-coding
- Human authors: Blue (#89B4FA)
- Agent/bot authors: Purple (#B7B4FA)  
- System messages: Gray (#646478)

**Implementation:** Table-style layout with responsive columns and color-coded sender names.

---

### 2. **Date/Time Not Optimized for Scanning**
**Problem:** Timestamps are in brackets `[12:34]` which:
- Takes up horizontal space
- Hard to scan quickly for "recent vs old"
- No visual grouping by date (today, yesterday, last week)
- No relative time indicators ("2m ago", "3h ago")

**Slack comparison:** Slack uses visual grouping with date headers and relative time that's more glanceable.

---

### 3. ~~**Table-like Scrolling Missing**~~ ✅ IMPLEMENTED
**Status:** Now using table-style layout with:
- Fixed-width time column (8 chars, right-aligned)
- Fixed-width sender column (15 chars, color-coded)
- Flexible content column (fills remaining width)
- Responsive width calculation based on terminal size

---

### 4. **Information Density Trade-offs**
**Problem:** Can't toggle between:
- **Compact mode**: More messages visible, less detail per message
- **Expanded mode**: Full message content, reactions, metadata
- **Thread preview**: See reply count without entering thread

**Dense preview ideas:**
- Show reply count inline: ` ↳ 5 replies`
- Show reaction counts: `👍 3  🔥 2`
- Truncate long messages with `…` indicator
- Multi-line messages collapse to single line in list view

---

### 5. **Header & Status Bar Underutilized**
**Current header:** Just shows view title + last activity time
**Current status bar:** Shows keybindings + context

**Missing opportunities:**
- **Header could show:** Unread count, channel description, thread participants
- **Status bar could show:** Connection status, sync state, current filter/sort mode
- **No breadcrumbs:** Hard to know "where am I" in the navigation stack

---

### 6. **Preview Panel Wasted Potential**
**Current preview:** Shows same list as main panel but for highlighted item

**Better uses:**
- **Message preview:** Full formatted content, reactions, thread tree
- **Thread preview:** All messages in thread without leaving channels view
- **Channel preview:** Recent threads + activity heatmap
- **User preview:** Who sent this message, their recent activity, role

---

### 7. **No Visual Hierarchy for Message Types**
**Problem:** All messages rendered with same style regardless of:
- Role (user/assistant/system)
- Author type (human/agent)
- Is a reply vs top-level message
- Has reactions vs no reactions

---

## 💡 ALTERNATIVE LAYOUT PROPOSALS

### Layout A: **Slack-Inspired 3-Panel**
```
┌──────────────┬───────────────────────────────┬─────────────────┐
│ CHANNELS     │ # general                     │ THREAD PREVIEW  │
│              │                               │                 │
│ ▸ general    │ ────────────────────────────  │ Selected Thread │
│   random     │                               │                 │
│   builds     │ [Today]                       │                 │
│   PRs        │                               │ # Design Review │
│              │                               │                 │
│              │ [👤 alice] 10:30              │ [👤 bob] 11:00  │
│              │   hey team                    │                 │
│              │                               │   here's the    │
│              │ [🤖 ci-bot] 10:45             │   mockup        │
│              │   ✅ build passed             │                 │
│              │   👍 3                        │ [👤 alice] 11:05│
│              │                               │   looks great!  │
│              │ [👤 bob] 11:00                │                 │
│              │   check out this PR           │ ↳ 4 replies     │
│              │   ↳ 5 replies                 │ 👍 2  ❤️ 1      │
│              │                               │                 │
├──────────────┴───────────────────────────────┴─────────────────┤
│ 12 msgs · 3 unread · 👤 alice · 📶 online · ↑↓ nav · Enter open │
└─────────────────────────────────────────────────────────────────┘
```

**Key features:**
- Left: Channel list (narrow, ~20 cols)
- Middle: Message list with visual sender types
- Right: Thread preview (selected message's thread)
- Sender icons: 👤 human, 🤖 agent, ⚙️ system
- Inline reaction counts
- Reply count without expanding

---

### Layout B: **Table-First Dense View**
```
┌──────────────────────────────────────────────────────────────────┐
│ #general  ·  24 msgs  ·  🔔 3 unread  ·  L: preview on           │
├──────┬──────────┬─────────┬──────────────────────────────────────┤
│ TIME │ AUTHOR   │ TYPE    │ CONTENT                              │
├──────┼──────────┼─────────┼──────────────────────────────────────┤
│ 11:05│ ▸alice   │ 👤 human│ looks great!                         │
│ 11:00│   bob    │ 👤 human│ check out this PR → github.com/...   │
│ 10:45│   ci-bot │ 🤖 agent│ ✅ build passed [test|lint|deploy]   │
│ 10:30│   alice  │ 👤 human│ hey team, quick update:              │
│      │          │         │                                      │
│ 09:15│   system │ ⚙️ sys │ Channel created                      │
├──────┴──────────┴─────────┴──────────────────────────────────────┤
│ c: compose · r: reply · t: toggle type filter · /: search        │
└──────────────────────────────────────────────────────────────────┘
```

**Key features:**
- True table layout with aligned columns
- Column headers for clarity
- Author type badges (human/agent/system)
- Long content truncation with `…`
- Cursor row highlighted
- Compact: fits 15-20 messages on screen

---

### Layout C: **Chronological Groups (Slack-style)**
```
┌──────────────────────────────────────────────────────────────────┐
│ #general                                                          │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Today                                                           │
│  ─────────────────────────────────────────────────────────────   │
│                                                                  │
│  [👤 alice]  11:05                                               │
│            │  looks great! let's ship it                        │
│            │  👍 2  🚀 1                                        │
│                                                                  │
│  [👤 bob]    11:00                                               │
│            │  check out this PR                                 │
│            │  ↳ 5 replies  ·  last reply 2m ago                 │
│                                                                  │
│  [🤖 ci-bot] 10:45                                               │
│            │  ✅ Build #42 passed                               │
│            │  [tests: 142 passed] [coverage: 87%]               │
│                                                                  │
│  Yesterday                                                       │
│  ─────────────────────────────────────────────────────────────   │
│                                                                  │
│  [👤 carol]  Dec 10                                              │
│            │  who's working on the API refactor?                │
│                                                                  │
├──────────────────────────────────────────────────────────────────┤
│ ↑↓ nav · Enter thread · c compose · r reply · / search          │
└──────────────────────────────────────────────────────────────────┘
```

**Key features:**
- Date group headers ("Today", "Yesterday", "Dec 10")
- Indented multi-line messages
- Relative timestamps for recent, absolute for old
- Inline metadata (reply count, reaction count)
- More vertical space per message = better readability

---

### Layout D: **Split-View Master-Detail**
```
┌───────────────────┬──────────────────────────────────────────────┐
│ THREADS           │ # general › API Discussion                   │
│                   │                                              │
│ ▸ API Discussion  │ ───────────────────────────────────────────  │
│   Bug Reports     │                                              │
│   Feature Ideas   │                                              │
│   Random          │ [👤 alice]  Today 10:30                      │
│                   │ │  hey team                                  │
│ Last Active       │                                              │
│ ▸ PR #42          │ [👤 bob]  Today 10:45                        │
│   Deploy Issues   │ │  reviewing now                             │
│   Sprint Planning │ │                                            │
│                   │ │  [🤖 ci-bot] 10:50                         │
│                   │ │  │  automated comment:                      │
│                   │ │  │  coverage dropped 2%                     │
│                   │ │                                            │
│                   │ [👤 alice]  Today 11:00                      │
│                   │ │  will fix tomorrow                         │
│                   │ │                                            │
│                   │                                              │
├───────────────────┴──────────────────────────────────────────────┤
│ Thread list adapts: shows threads in channel OR recent threads   │
└──────────────────────────────────────────────────────────────────┘
```

**Key features:**
- Left panel always shows navigation (threads or channels)
- Right panel is full message thread view
- Nested replies indented with visual tree
- Bot messages visually nested under triggering message
- No "preview" toggle — detail always visible

---

### Layout E: **Hybrid Compact/Expand**
```
┌──────────────────────────────────────────────────────────────────┐
│ #general  ·  compact mode (press 'v' to expand)                  │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│ 10:30  👤 alice     hey team, quick update on the project       │
│ 10:45  🤖 ci-bot    ✅ build passed · 👍 3 · ↳ 2               │
│ 11:00  👤 bob       PR ready for review → #42                   │
│ 11:05  👤 alice     shipping now 🚀                              │
│                                                                  │
│ Press Enter on any message to expand:                           │
│ ┌────────────────────────────────────────────────────────────┐  │
│ │ [👤 alice] 11:05                                           │  │
│ │                                                            │  │
│ │ shipping now 🚀                                            │  │
│ │                                                            │  │
│ │ Reactions: 👍 bob, carol  |  🎉 dave                       │  │
│ │ Replies: 2  |  Thread ID: 123                              │  │
│ └────────────────────────────────────────────────────────────┘  │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

**Key features:**
- Toggle between compact list and expanded detail
- Compact: one line per message, max info density
- Expanded: full content, reactions, metadata inline
- Best for terminals of varying sizes

---

## 🎨 VISUAL ENHANCEMENTS

### Sender Type (Color-Only Approach)
**No icons needed** - color alone provides instant recognition:

```
Colors (if terminal supports):
  Human:   Blue foreground or default white
  Agent:   Cyan or Purple foreground  
  System:  Gray/dimmed
```

This is cleaner and more subtle than icons, matching Slack's approach of using visual distinction without emoji clutter.

### Message Type Styling
```go
// Pseudo-code for styles
humanMsgStyle   = fgBlue.Bold()
agentMsgStyle   = fgPurple.Italic()
systemMsgStyle  = fgGray.Dim()
errorMsgStyle   = bgRed.FgWhite
successMsgStyle = bgGreen.FgBlack
```

### Reply Threading Visualization
```
Top-level message
↳ Reply 1 (indented 2 spaces, vertical line)
   ↳ Nested reply (indented 4 spaces)
↳ Reply 2
```

ASCII art:
```
[alice] hey team
│
├─ [bob] on it
│  │
│  └─ [ci-bot] tests passing
│
└─ [carol] reviewing
```

---

## 📊 INFORMATION DENSITY MODES

### Mode 1: Ultra-Compact (40+ lines visible)
```
10:30 alice: hey team
10:45 ci-bot: ✅ build
11:00 bob: PR #42
```

### Mode 2: Standard (20-30 lines visible)
```
[👤 alice] 10:30
  hey team
  
[🤖 ci-bot] 10:45
  ✅ build passed · 👍 3
```

### Mode 3: Expanded (10-15 lines visible)
```
[👤 alice]  Today 10:30
│  hey team, quick update on the project
│  we're making good progress
│
│  Reactions: 👍 bob, carol
│  Replies: 5 · Last reply 2m ago
```

---

## 🔧 IMPLEMENTATION PRIORITIES

### Phase 1: Quick Wins
1. **Add author_type icon** (👤/🤖/⚙️) to message rendering
2. **Relative timestamps** ("2m ago" vs "10:30")
3. **Reply count inline** (` ↳ 5`)
4. **Date group headers** ("Today", "Yesterday")

### Phase 2: Table Feel
5. **Column alignment** for time/author/content
6. **Header row** showing column names
7. **Alternating row colors** or subtle borders
8. **Fixed-width columns** with overflow handling

### Phase 3: Advanced Features
9. **Toggle density modes** (compact/standard/expanded)
10. **Enhanced preview panel** (full message detail, reactions)
11. **Visual reply threading** (indentation + tree lines)
12. **Reaction display** in list view

### Phase 4: Polish
13. **Color coding** by author type
14. **Breadcrumb navigation** in header
15. **Search/filter** with visual feedback
16. **Unread indicators** per channel/thread

---

## 🎯 RECOMMENDED APPROACH

**Start with Layout B (Table-First)** because:
1. Retains 2-panel design you want
2. Easy to implement incrementally
3. Provides immediate "Slack-like" feel with sender types
4. Scales well across terminal sizes
5. Compatible with existing navigation model

**Then add:**
- Date grouping from Layout C
- Expand/collapse from Layout E
- Enhanced preview from Layout A

This gives you the best of all worlds: dense information, clear sender identity, chronological context, and powerful preview capabilities.

---

## 📝 NEXT STEPS

1. **Pick a layout direction** (recommend starting with Table-First)
2. **Define exact column widths** based on typical terminal sizes
3. **Implement sender type icons** as first enhancement
4. **Add relative timestamp formatting**
5. **Create toggle for density modes**
6. **Iterate based on real usage feedback**
