# Extending the TUI

How to add features to the Fluffle TUI. Covers adding views, compose modes, new key bindings, panel layouts, and data fetching.

## Prerequisites

- Understand the [TUI Architecture](tui-architecture.md) — state machine, rendering pipeline, message types
- Understand the [TUI Key Bindings](tui-keybindings.md) — current bindings, context sensitivity
- Read `internal/tui/model.go` end-to-end — it's the heart of the TUI

## Adding a New View

The TUI uses a three-view stack: `viewChannels` → `viewThreads` → `viewMessages`. To add a new view:

### 1. Add view kind

```go
// In model.go, add to viewKind enum:
type viewKind int

const (
    viewChannels viewKind = iota
    viewThreads
    viewMessages
    viewNewThing  // <-- add here
)
```

### 2. Add state fields

Add any fields needed to the `model` struct:

```go
type model struct {
    // ... existing fields ...
    newThingData  []SomeType
    selectedNew   *SomeType
}
```

### 3. Add data fetching

Add a fetch method and corresponding message type:

```go
// In model.go:
func (m *model) fetchNewThing() tea.Cmd {
    return func() tea.Msg {
        data, err := m.api.ListNewThing(nil, someID)
        return newThingFetchedMsg{data: data, err: err}
    }
}

// In compose.go (or a new file):
type newThingFetchedMsg struct {
    data []SomeType
    err  error
}
```

### 4. Handle fetch in Update

```go
case newThingFetchedMsg:
    if msg.err != nil {
        m.status = fmt.Sprintf("error: %v", msg.err)
        return m, nil
    }
    m.newThingData = msg.data
    m.cursor = 0
    m.view = viewNewThing
    m.status = fmt.Sprintf("%d items — ↑↓ nav · Enter open · Esc back", len(msg.data))
    return m, nil
```

### 5. Add navigation

In `handleKey()`, add Enter handling to enter the view:

```go
case tea.KeyEnter:
    switch m.view {
    // ... existing cases ...
    case viewPreviousView:
        // Enter on previous view to enter new view
        if len(m.previousData) > 0 && m.cursor >= 0 && m.cursor < len(m.previousData) {
            item := m.previousData[m.cursor]
            m.selectedNew = &item
            return m, m.fetchNewThing()
        }
    }
```

And Esc handling to leave:

```go
case tea.KeyEscape:
    switch m.view {
    case viewNewThing:
        m.view = viewPreviousView
        m.cursor = 0
        m.selectedNew = nil
        return m, nil
    }
```

### 6. Add rendering

In `renderListWithWidth()`, add a case for the new view:

```go
case viewNewThing:
    title = "New Thing"
    if len(m.newThingData) == 0 {
        items = []string{"  (nothing here — press n to create)"}
    } else {
        for i, item := range m.newThingData {
            prefix := "  "
            if i == m.cursor {
                prefix = "▸ "
            }
            line := prefix + fmt.Sprintf("%s", item.Name)
            if i == m.cursor {
                line = treeItemSelectedStyle.Render(line)
            } else {
                line = treeItemStyle.Render(line)
            }
            items = append(items, line)
        }
    }
```

### 7. Update helpView

```go
case viewNewThing:
    parts = []string{hintKeyStyle.Render("↑↓") + " nav", hintKeyStyle.Render("Enter") + " open", hintKeyStyle.Render("Esc") + " back", hintKeyStyle.Render("q") + " quit"}
```

### 8. Update preview (optional)

If the new view should show in the preview panel, add to `renderPreview()` and `maybeFetchPreview()`.

## Adding a New Compose Mode

The compose modal supports three modes: `composeModeMessage`, `composeModeReply`, `composeModeNewThread`.

### 1. Add mode constant

```go
type composeMode int

const (
    composeModeMessage composeMode = iota
    composeModeReply
    composeModeNewThread
    composeModeNewMode  // <-- add here
)
```

### 2. Add state fields if needed

If the new mode needs additional state, extend `composeState`:

```go
type composeState struct {
    mode     composeMode
    context  string
    text     string
    cursor   int
    error    string
    threadID int64
    parentID int64
    newItemID int64  // <-- if needed
}
```

### 3. Handle mode in Update

In `composeModel.Update()`, the mode is already handled by `composeSendMsg` which carries the mode. The main model's `handleComposeSend()` switches on mode:

```go
func (m *model) handleComposeSend(msg composeSendMsg) (tea.Model, tea.Cmd) {
    switch msg.mode {
    case composeModeNewThread:
        // existing logic
    case composeModeMessage:
        // existing logic
    case composeModeNewMode:
        // <-- add new mode handling
        err := m.api.DoNewThing(nil, msg.text)
        if err != nil {
            m.compose.SetError(err.Error())
            return m, nil
        }
        m.compose.Close()
        m.status = "done"
        return m, m.fetchChannels()
    }
}
```

### 4. Handle mode in View

In `composeModel.View()`, add hints for the new mode:

```go
switch m.state.mode {
case composeModeMessage:
    lines = append(lines, modalHintStyle.Render("Enter to send, Esc to cancel"))
case composeModeReply:
    lines = append(lines, modalHintStyle.Render("Enter to reply, Esc to cancel"))
case composeModeNewThread:
    lines = append(lines, modalHintStyle.Render("Enter to create thread, Esc to cancel"))
case composeModeNewMode:
    lines = append(lines, modalHintStyle.Render("Enter to create, Esc to cancel"))
}
```

### 5. Open the compose modal

From `handleKey()`, open the new mode:

```go
case "x":  // new key binding
    m.compose.Open(composeModeNewMode, "New thing", m.height)
    m.compose.state.threadID = 0
    return m, nil
```

## Adding a New Key Binding

Key bindings are in `handleKey()` in `model.go`. Two switch blocks:

### 1. Control keys (codes)

```go
switch key.Code {
case tea.KeyUp:
    // ...
case tea.KeyEnter:
    // ...
case tea.KeyEscape:
    // ...
// Add new control keys here
case tea.KeyCtrlT:
    // Ctrl+T handling
}
```

### 2. Character keys (text)

```go
switch key.Text {
case "q":
    // ...
case "n":
    // ...
case "c":
    // ...
case "L":
    // ...
// Add new character keys here
case "x":
    // x key handling
}
```

### 3. Update helpView

Always document new keys in `helpView()` for the relevant views.

## Adding a New API Endpoint

New endpoints go in `api.go`:

```go
func (c *apiClient) ListNewThing(ctx context.Context, id int64) ([]SomeType, error) {
    url := c.base + "/v1/some-path/" + fmt.Sprintf("%d", id) + "/new-thing"
    resp, err := c.http.Get(url)
    if err != nil {
        return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 {
        return nil, readAPIError(resp)
    }
    var items []SomeType
    if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
        return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
    }
    if items == nil {
        items = []SomeType{}
    }
    return items, nil
}
```

## Adding a New Data Type

If the new feature requires a new store type, add it to the `store` package first, then use it in the TUI.

### 1. Add to store types

```go
// In internal/store/store.go:
type NewThing struct {
    ID        int64
    ChannelID int64
    Name      string
    CreatedAt string
}
```

### 2. Add query methods to store

```go
func (s *Store) ListNewThings(channelID int64) ([]NewThing, error) {
    // SQL query
}
```

### 3. Add handler to apiserver

```go
// In internal/apiserver/:
// GET /v1/channels/:id/new-things
```

### 4. Add TUI API wrapper

See "Adding a New API Endpoint" above.

## Modifying the Preview Panel

The preview panel is rendered by `renderPreview()` in `model.go`. To change what it shows:

### 1. Change preview content

Edit the switch cases in `renderPreview()`:

```go
case viewChannels:
    // Preview shows threads of highlighted channel
    // Modify how threads are rendered
```

### 2. Change preview fetch trigger

Edit `maybeFetchPreview()`:

```go
func (m *model) maybeFetchPreview() tea.Cmd {
    if !m.preview || m.width < 80 {
        return nil
    }
    // Add new preview data fetching here
    switch m.view {
    case viewChannels:
        // ...
    }
    return nil
}
```

### 3. Change preview dimensions

Preview panel dimensions are computed in `View()`:

```go
if m.preview && m.width >= 80 {
    leftW := m.width/2 - 1
    rightW := m.width - leftW - 3
    // Change these calculations to adjust panel widths
}
```

## Modifying Styles

All styles are in `styles.go`. To change colors or layout:

### 1. Add new color

```go
var (
    // ... existing colors ...
    newColor = color.RGBA{R: 100, G: 100, B: 100, A: 255}
)
```

### 2. Add new style

```go
var (
    // ... existing styles ...
    newStyle = lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        BorderForeground(newColor).
        Background(treeBg).
        Foreground(treeFg).
        Padding(0, 1)
)
```

### 3. Use in rendering

Apply the style in `renderListWithWidth()` or `renderPreview()`:

```go
line = newStyle.Render(line)
```

## Removing a View

To remove a view (e.g., deprecating one):

1. Remove the `viewKind` constant
2. Remove all switch cases referencing it in `Update()`, `View()`, `renderListWithWidth()`, `renderPreview()`, and `helpView()`
3. Remove associated state fields from `model`
4. Remove associated fetch methods and message types
5. Remove associated key bindings from `handleKey()`
6. Update `docs/tui-keybindings.md`

## Common Patterns

### Refreshing data after an action

```go
case composeSendMsg:
    // ... send logic ...
    m.compose.Close()
    m.status = "sent"
    return m, m.fetchChannels()  // refresh current list
```

### Showing error in status bar

```go
if msg.err != nil {
    m.status = fmt.Sprintf("error: %v", msg.err)
    return m, nil
}
```

### Showing empty state

```go
if len(m.items) == 0 {
    items = []string{"  (nothing here — press n to create)"}
}
```

### Guarding against empty lists

```go
if len(m.items) == 0 || m.cursor < 0 || m.cursor >= len(m.items) {
    return m, nil
}
```

## Testing New Features

### Unit test

Add to `internal/tui/simple_test.go` or create a new `_test.go` file:

```go
func TestNewFeature(t *testing.T) {
    m := toModel(New("http://127.0.0.1:0"))
    nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
    m = toModel(nm)
    // Test the new feature flow
    nm, _ = m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
    m = toModel(nm)
    if !m.compose.IsActive() {
        t.Fatalf("want compose active")
    }
}
```

### Manual test

Add to `docs/tui-manual-test.md`:

```markdown
- [ ] **New feature**: describe the test steps
```

## File Locations Quick Reference

| Feature | Files to touch |
|---------|---------------|
| New view | `model.go` (state, Update, View, renderListWithWidth, helpView, maybeFetchPreview) |
| New compose mode | `compose.go` (mode constant, View hints), `model.go` (handleComposeSend, handleKey) |
| New key binding | `model.go` (handleKey, helpView) |
| New API endpoint | `api.go` (wrapper), `model.go` (fetch method, message type, Update handler) |
| New data type | `store/store.go` (type + query), `apiserver/` (HTTP handler), `api.go` (wrapper) |
| New style | `styles.go` (color + style var), `model.go` (apply in rendering) |
| Preview changes | `model.go` (renderPreview, maybeFetchPreview, View layout) |
