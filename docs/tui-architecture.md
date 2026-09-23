# TUI Architecture

Bubble Tea v2 terminal UI for Fluffle. Split-panel interface: left-hand channel/thread/message list, right-hand preview panel (wide terminals), centered compose modal overlay.

## Overview

```
┌─────────────────────┬──────────────────────────────────────────┐
│ Channels            │ Preview: #hello                          │
│                     │                                          │
│ ▸ demo (orphaned)   │ # hello  · now                           │
│   second (main)     │                                          │
│                     │ [now] alice: hi                          │
│                     │ [2m] bob: reply                          │
│                     │                                          │
├─────────────────────┤──────────────────────────────────────────┤
│ ↑↓ nav · Enter open · n new thread · L hide preview · q quit  │
│ Channels · last now                                                      │
└─────────────────────┴──────────────────────────────────────────┘
```

Three views in a stack: **Channels → Threads → Messages**. `Esc` backs up the stack. `L` toggles a right-side preview panel showing the next screen for the highlighted item. Preview auto-enables at ≥100 cols, respects toggle at ≥80 cols, forced off below 80.

## Package Structure

```
internal/tui/
├── tui.go          - Entry point: Run() → tea.Program(model)
├── model.go        - Bubble Tea model: state, Init, Update, View, rendering
├── api.go          - Thin HTTP wrapper over daemon REST API
├── compose.go      - Centered compose modal: text input, send/cancel
└── styles.go       - Lipgloss styles: colors, borders, typography
```

### Responsibilities

| File | Responsibility |
|------|----------------|
| `tui.go` | `Run()` entry point, daemon ensure, `tea.Model` initialization, exit codes (0=success, 1=error, 2=daemon down) |
| `model.go` | Full `model` struct, state transitions via `Update()`, rendering via `View()`, key handling, data fetching, time formatting, preview logic |
| `api.go` | HTTP calls to daemon endpoints, error parsing (`readAPIError`), channel filtering, `jsonBody` helper |
| `compose.go` | Compose modal state machine, text input handling, send/cancel, error display, context header rendering |
| `styles.go` | All lipgloss style definitions: tree panel, chat panel, modal, status bar, hints, colors |

## Dependencies

```
internal/tui/
├── internal/client   (daemon location, auto-spawn, health probe)
├── internal/store    (data types: Channel, Thread, Message, Reaction)
└── charm.land/bubbletea/v2
    ├── charm.land/lipgloss/v2
    └── github.com/mattn/go-runewidth
```

No new external dependencies beyond the Bubble Tea ecosystem. The TUI reuses `internal/client` for daemon communication — same auto-spawn, same health probe, same error handling.

## State Machine

```
                    ┌──────────────────────────────────────┐
                    │                                      │
                    ▼                                      │
  [Channels] ──Enter──▶ [Threads] ──Enter──▶ [Messages]   │
     ▲                  │    │                      │      │
     │                  │    │ Esc                  │ c    │
     │                  │    │                      ▼      │
     │              [Esc]  [n]               [Compose]     │
     │                    │                          │     │
     └────────────────────┴──────────────────────────┘     │
                    │                                      │
                    └────────────── Esc ────────────────────┘
```

Three views tracked by `viewKind` enum (`viewChannels`, `viewThreads`, `viewMessages`). Navigation is strictly stack-based: Enter advances, Esc retreats. No lateral navigation.

### State Fields

| Field | Purpose |
|-------|---------|
| `view` | Current view kind (Channels/Threads/Messages) |
| `cursor` | Index into current list (0-based, clamped to list length) |
| `preview` | Whether right-side preview panel is visible |
| `selectedChannel` | Channel pointed to by cursor (pointer for thread/message views) |
| `selectedThread` | Thread pointed to by cursor (pointer for message view) |
| `compose` | Active compose modal state (or inactive) |
| `status` | Status bar text (errors, counts, hints) |
| `width, height` | Current terminal dimensions (from `WindowSizeMsg`) |

### Data Fetching

Data is fetched on demand, never polled:

| Event | Fetch |
|-------|-------|
| `Init()` | Channels |
| Enter on channel | Threads for that channel |
| Enter on thread | Messages for that thread |
| After send/create | Refresh current list |
| Preview panel active + cursor moves | Preview data for highlighted item |

Preview fetching is idempotent: skips if cursor hasn't changed, or if width < 80.

### Messages (Bubble Tea Msg Types)

| Msg Type | Carries | Triggers |
|----------|---------|----------|
| `channelsFetchedMsg` | `[]Channel`, `error` | `fetchChannels()` completes |
| `threadsFetchedMsg` | `channelID`, `[]Thread`, `error` | `fetchThreads()` completes |
| `messagesFetchedMsg` | `threadID`, `[]Message`, `error` | `fetchMessages()` completes |
| `previewThreadsFetchedMsg` | `channelID`, `[]Thread`, `error` | `fetchPreviewThreads()` completes |
| `previewMessagesFetchedMsg` | `threadID`, `[]Message`, `error` | `fetchPreviewMessages()` completes |
| `composeSendMsg` | `text`, `composeMode`, `context` | User presses Enter in compose |
| `threadCreatedMsg` | `channelID`, `threadID`, `title`, `error` | Thread creation response |
| `tea.WindowSizeMsg` | `Width`, `Height` | Terminal resize |
| `tea.KeyMsg` | key code/text | Any key press |

## Rendering Pipeline

```
View()
  ├─ compose active? ──yes──▶ render list (full width) + center(compose view)
  │
  └─ no ──▶ preview? ──yes──▶ render list (left half) + render preview (right half)
  │                            └── lipgloss.JoinHorizontal(Top, left, right)
  │
  └─ no ──▶ render list (full width)
  │
  └─ append helpView() + status
```

### `renderListWithWidth(w int)`

Renders the current view as a bordered box:

1. Determines title from view kind + latest activity timestamp
2. Renders list items with cursor indicator (`▸`) or blank prefix (`  `)
3. Items are styled with `treeItemSelectedStyle` (cursor) or `treeItemStyle` (normal)
4. Messages show `[timestamp] author: content` with `↳` for replies
5. Channels show name + `(orphaned)` or `[branch] repo` + last activity
6. Truncated to panel height, padded with empty lines if short
7. Wrapped in rounded border via lipgloss

### `renderPreview(w int)`

Renders the preview panel for the cursor-highlighted item:

- **Channels view**: shows threads of highlighted channel
- **Threads view**: shows messages of highlighted thread
- **Messages view**: shows full content of highlighted message

### `helpView()`

Context-sensitive help bar at bottom of list. Shows available key bindings for current view.

### `center(s string, width int)`

Centers a multi-line string horizontally within `width` columns. Used for compose modal overlay.

## Error Handling

| Error | TUI Behavior |
|-------|-------------|
| Daemon down | `tui.Run()` returns exit code 2, prints to stderr |
| HTTP fetch error | Status bar shows "error: ..."; user must navigate away and back |
| Compose send error | Compose modal header shows error; modal stays open |
| Empty compose | Compose modal shows "cannot be empty" in red |
| No channel selected | Status bar shows "select a channel first" |
| No thread selected | Status bar shows "no thread — n to create" |

All errors go through the status bar or compose modal — never panic. The only panics are in `toModel()` (type assertion failure) and `minInt`/`max` helpers (never panic, just comparisons).

## Styling System

All colors use the **Tokyo Night** palette via lipgloss `color.RGBA` values:

| Element | Background | Foreground |
|---------|-----------|------------|
| Tree panel | `#1e1e2e` (dark) | `#c9d6f4` (light) |
| Tree selected | `#313244` | `#f5f5f5` |
| Chat panel | `#282838` | `#b4bee2` |
| Chat selected | `#313244` | `#f5f5f5` |
| Modal | `#313244` | `#f5f5f5` |
| Modal border | — | `#89b4fa` (blue accent) |
| Status bar | `#181825` | `#89b4fa` |
| Hints | — | `#646478` (dim) |
| Key hints | — | `#89b4fa` (accent, bold) |

Styles are defined as package-level `var`s in `styles.go`. Each style uses lipgloss's fluent builder pattern with `.Border()`, `.BorderForeground()`, `.Background()`, `.Foreground()`, `.Padding()`, `.Width()`, `.Margin*()`.

## Data Flow

```
User presses key
  │
  ▼
model.Update(tea.KeyMsg)
  │
  ├─ compose active? ──yes──▶ composeModel.Update(key) ──▶ composeSendMsg
  │
  └─ no ──▶ handleKey(key)
              │
              ├─ ↑↓/j/k ──▶ cursor++, cursor-- ──▶ maybeFetchPreview()
              ├─ Enter ──▶ advance view (channels→threads→messages) ──▶ fetch()
              ├─ Esc ──▶ retreat view ──▶ clear selection
              ├─ n ──▶ handleNewThread() ──▶ compose.Open(composeModeNewThread)
              ├─ c ──▶ handlePost() ──▶ compose.Open(composeModeMessage)
              ├─ L/l ──▶ toggle preview ──▶ maybeFetchPreview()
              └─ q ──▶ quitting = true ──▶ tea.Quit
  │
  ▼
tea.View() called by Bubble Tea runtime
  │
  └─ renders based on current model state
```

## Testing

### Unit Test (`simple_test.go`)

Tests the core navigation flow: window resize → channel fetch → cursor down → Enter channel → thread fetch → Enter thread → message fetch → Esc back × 2 → new thread compose. Uses `toModel()` to extract `model` from `tea.Model` interface.

### Manual Testing (`tui-manual-test.md`)

Comprehensive manual test checklist covering all navigation paths, compose flows, preview toggling, and troubleshooting.

## Design Decisions

1. **Single list, not split panel**: Unlike the original spec (dual panels with independent cursors), the TUI uses one cursor shared across views. Simpler, less confusing.
2. **Preview panel instead of tree**: The preview panel shows the next screen for the highlighted item, rather than a hierarchical tree. This avoids the complexity of expandable/collapsible tree nodes.
3. **`c` for both post and reply**: There's no separate `r` key. `c` always appends to the end of a thread. No per-message reply targeting.
4. **No filter/sort**: The original spec included `/` for repo filter and `s` for sort cycling. These are not implemented in the current TUI.
5. **Preview auto-enables at ≥100 cols**: Below 100 cols, user must press `L` to enable preview. At 80-99 cols, preview is available but off by default. Below 80 cols, preview is forced off.
6. **Status bar at bottom**: Always visible, shows action feedback, error messages, and context-sensitive hints.
7. **Compose modal centered**: Full width of terminal, height scales with terminal height (min 5, max 12 lines).
