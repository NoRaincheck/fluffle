# Extending the TUI

How to add features to the Fluffle TUI. See [TUI Architecture](tui-architecture.md) for full details.

## Adding a New View

1. Add `viewKind` constant in `model.go`
2. Add state fields to `model` struct
3. Add fetch method + message type (`fetchX() tea.Cmd`, `xFetchedMsg`)
4. Handle fetch in `Update()` switch
5. Add rendering in `renderInboxWithWidth()` or new render function
6. Add key handling in `handleKey()`
7. Update `helpView()` for the new view

## Adding a New Compose Mode

1. Add `composeMode` constant in `compose.go`
2. Extend `composeState` if needed
3. Handle in `handleComposeSend()` switch in `model.go`
4. Add hints in `composeModel.View()`
5. Open from `handleKey()`: `m.compose.Open(composeModeNewMode, "title", m.height)`

## Adding a New Key Binding

Two switch blocks in `handleKey()` in `model.go`:

- **Control keys** (`key.Code`): `tea.KeyUp`, `tea.KeyEnter`, etc.
- **Character keys** (`key.Text`): `"q"`, `"n"`, `"c"`, etc.

Always update `helpView()` for relevant views.

## Adding a New API Endpoint

1. Add store method in `internal/store/store.go`
2. Add HTTP handler in `internal/apiserver/server.go`
3. Add TUI wrapper in `internal/tui/api.go` (pattern: GET URL → decode JSON → nil→empty guard)
4. Add fetch method + message type in `model.go`
5. Handle in `Update()` switch

## Common Patterns

| Pattern | Code |
|---------|------|
| Refresh after action | `return m, m.fetchInbox()` |
| Error in status bar | `m.status = fmt.Sprintf("error: %v", msg.err)` |
| Empty state | `items = []string{"  (nothing here — press n to create)"}` |
| Guard empty list | `if len(items) == 0 || cursor < 0 || cursor >= len(items)` |

## Modifying the Preview Panel

- Content: edit `renderPreview()` — shows full message + last 5 replies
- Fetch trigger: edit `maybeFetchPreview()` — debounced by thread ID; `syncVisibleData()` batches it with `maybeFetchFullRows()`
- Dimensions: computed in `View()` — `leftW = m.width/2 - 1`, `rightW = m.width - leftW - 3`

## Modifying Styles

All styles in `styles.go`. Add color + style var, apply in rendering.

## File Locations Quick Reference

| Feature | Files to touch |
|---------|---------------|
| New view | `model.go` (state, Update, View, render, helpView, syncVisibleData) |
| New compose mode | `compose.go` (mode constant, View hints), `model.go` (handleComposeSend, handleKey) |
| New key binding | `model.go` (handleKey, helpView) |
| New API endpoint | `api.go` (wrapper), `model.go` (fetch, message type, Update handler) |
| New data type | `store/store.go` (type + query), `apiserver/` (HTTP handler), `api.go` (wrapper) |
| New style | `styles.go` (color + style var), `model.go` (apply in rendering) |
| Preview changes | `model.go` (renderPreview, maybeFetchPreview, View layout) |
| Inbox layout | `model.go` (`inboxLayout`, `renderInboxFullWithWidth`, `inboxFullGeometry`, `clampInboxFullScroll`) |
