# TUI Rows Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Fluffle TUI's five views, three presentation modes, and six-field poll with one list of server-shaped rows at three granularities, one thread renderer in two placements, one unconditional tick, and one `names` package owning both name rules.

**Architecture:** The daemon owns what a row is. `store.ListRows(ctx, granularity, limit)` answers `message`, `thread`, or `channel` from three sqlc queries, and the TUI renders `[]Row` through a single fixed-width line function. Grouping, representative selection, and reply counts move out of the client entirely. The TUI keeps fifteen model fields, one tick, and seven keys.

**Tech Stack:** Go 1.26, Bubble Tea v1, `github.com/charmbracelet/lipgloss` v1, sqlc, `modernc.org/sqlite`, vendored `screen` + `termtext` from `go.kenn.io/kit`.

**Spec:** `docs/superpowers/specs/2026-09-27-tui-rows-rewrite-design.md`

## Global Constraints

- `MaxSlug = 12` and `MaxName = 12`, byte lengths. Slug charset `^[A-Za-z][A-Za-z0-9-]*$`, name charset `^[A-Za-z][A-Za-z.]*$`. Both must start with an ASCII letter.
- `threads.title` keeps its column name, its `Title` field, and the `--title` flag. It becomes a slug. Do not rename it to `slug`.
- No schema change. The `length(trim(x)) > 0` CHECKs stay. Character rules are enforced in Go only.
- No migration, no data-repair path, no rebuild probe. `~/.fluffle` was deleted before this work.
- Name validation lives in `internal/names` and nowhere else. `agentcfg` and `mentions` must not carry their own regex.
- Geometry constants: `ColW = 12`, `CountW = 3`, `ColGap = 2`, `CursorW = 2`, `rowPrefixW = 61`, `MinContentW = 10`, `MinWidth = 71`, `MinHeight = 24`, `chromeH = 3`, `MinSplitWidth = 110`, `ListW = 74`.
- Granularity cycle order is `message` → `thread` → `channel` → `message`.
- The TUI keymap is exactly: `↑`/`k`, `↓`/`j`, `g`, `v`, `Enter`, `Esc`, `r`, `q`, plus `ctrl+c`. No `?`, no `l`, `p`, `s`, `n`, `C`, `e`, `f`, or `c`.
- Untrusted text (message content, names, channel and thread labels) reaches the terminal only through `termtext.SanitizeLine` or `termtext.SanitizeBlock`.
- Exit codes unchanged: 0 success, 1 client error, 2 daemon error. Error envelope `{"code","message"}` at every layer boundary.
- The gate is `go vet ./...`, `test -z "$(gofmt -l .)"`, `go test ./...`, `go tool sqlc diff`. All must pass before every commit.
- TDD: failing test, then implementation, then commit. Never commit a red test.
- Go 1.21 builtins `min` and `max` are used. Never declare them again.

---

### Task 1: Vendor `screen` and `termtext`

Copy two packages from `go.kenn.io/kit` at pinned commit `d5cebe88834deec78b0633723590db6796c48739`, verbatim, without their upstream tests. A fork rather than a module requirement because kit's `go.mod` declares `go 1.27.0` and this module is on 1.26. Verbatim copies stay diffable against upstream.

**Files:**
- Create: `internal/tui/screen/{doc.go,screen.go,width.go,NOTICE,smoke_test.go}`
- Create: `internal/tui/termtext/{doc.go,termtext.go,NOTICE,smoke_test.go}`
- Modify: `go.mod` (promote `github.com/charmbracelet/x/ansi` from indirect to direct)

**Interfaces:**
- Consumes: nothing.
- Produces: `screen.Width(string) int`, `screen.Truncate(string, int) string`, `screen.OverlayCentered(background, panel string, width, height int) string`, `termtext.StripANSI(string) string`, `termtext.SanitizeLine(string) string`, `termtext.SanitizeBlock(string) string`, `termtext.Truncate(s string, width int, tail string) string`, `termtext.Wrap(s string, width int) []string`, `termtext.DisplayWidth(string) int`.

- [ ] **Step 1: Create the directories and fetch the five source files**

```bash
cd /Users/crn/dev/projects/fluffle
mkdir -p internal/tui/screen internal/tui/termtext
SHA=d5cebe88834deec78b0633723590db6796c48739
for f in screen/doc.go screen/screen.go screen/width.go termtext/doc.go termtext/termtext.go; do
  curl -fsSL "https://raw.githubusercontent.com/kenn-io/kit/$SHA/tui/$f" -o "internal/tui/$f"
done
wc -c internal/tui/screen/*.go internal/tui/termtext/*.go
```

Expected: `screen.go` 4008, `width.go` 3437, `screen/doc.go` 2033, `termtext.go` 4153, `termtext/doc.go` 896.

- [ ] **Step 2: Write the NOTICE files**

`internal/tui/screen/NOTICE`:

```
This directory is a verbatim copy of tui/screen from go.kenn.io/kit at commit
d5cebe88834deec78b0633723590db6796c48739, without the upstream tests.

Upstream: https://github.com/kenn-io/kit
Licensed under the Apache License, Version 2.0.
Copyright 2026 Kenn Software LLC

Licensed under the Apache License, Version 2.0 (the "License"); you may not use
these files except in compliance with the License. You may obtain a copy of the
License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed
under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.
```

`internal/tui/termtext/NOTICE`: identical text with `tui/termtext` in place of `tui/screen`.

- [ ] **Step 3: Write the failing smoke tests**

`internal/tui/termtext/smoke_test.go`:

```go
package termtext

import "testing"

func TestSmokeSanitizeLineDropsNonSGRSequences(t *testing.T) {
	if got := SanitizeLine("a\x1b]0;title\x07b"); got != "ab" {
		t.Fatalf("SanitizeLine = %q, want %q", got, "ab")
	}
}

func TestSmokeTruncateKeepsTailWithinWidth(t *testing.T) {
	if got := Truncate("abcdefgh", 5, "…"); got != "abcd…" {
		t.Fatalf("Truncate = %q, want %q", got, "abcd…")
	}
}

func TestSmokeWrapNeverExceedsWidth(t *testing.T) {
	lines := Wrap("the quick brown fox jumps", 10)
	if len(lines) == 0 {
		t.Fatal("Wrap returned no lines")
	}
	for _, line := range lines {
		if DisplayWidth(line) > 10 {
			t.Fatalf("line %q is wider than 10", line)
		}
	}
}
```

`internal/tui/screen/smoke_test.go`:

```go
package screen

import "testing"

func TestSmokeWidthIsWidestLine(t *testing.T) {
	if got := Width("ab\nabcd\nabc"); got != 4 {
		t.Fatalf("Width = %d, want 4", got)
	}
}

func TestSmokeOverlayCenteredReplacesCells(t *testing.T) {
	if got := OverlayCentered("aaaa\nbbbb\ncccc", "ZZ", 4, 3); got != "aaaa\nZZbb\ncccc" {
		t.Fatalf("OverlayCentered = %q", got)
	}
}
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test ./internal/tui/screen/ ./internal/tui/termtext/`
Expected: FAIL — `github.com/charmbracelet/x/ansi` is only an indirect dependency, so the build fails to resolve it.

- [ ] **Step 5: Promote the dependency**

Run: `go get github.com/charmbracelet/x/ansi@v0.11.8 && go mod tidy`
Expected: `github.com/charmbracelet/x/ansi v0.11.8` moves out of the `// indirect` block. No new module is downloaded; it is already in `go.sum`.

- [ ] **Step 6: Run the gate**

Run: `go test ./internal/tui/screen/ ./internal/tui/termtext/ && gofmt -l internal/tui/screen internal/tui/termtext`
Expected: PASS, and `gofmt -l` prints nothing.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/screen internal/tui/termtext go.mod go.sum
git commit -m "feat(tui): vendor screen and termtext from go.kenn.io/kit

ANSI- and grapheme-correct line composition, truncation, wrapping, and
sanitization, copied verbatim from go.kenn.io/kit at d5cebe88 so they stay
diffable against upstream. A fork rather than a module requirement because
kit's go.mod declares go 1.27.0 and this module is on 1.26.

Only moves github.com/charmbracelet/x/ansi into the direct require block; it
was already in the tree as a lipgloss dependency."
```

---

### Task 2: The `names` package

**Files:**
- Create: `internal/names/names.go`
- Create: `internal/names/names_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `names.MaxSlug = 12`, `names.MaxName = 12`, `names.Slug(s string) error`, `names.Name(s string) error`, `names.ValidSlug(s string) bool`, `names.ValidName(s string) bool`, `names.IsNameByte(b byte, pos int) bool`, `names.IsNameContinuation(b byte) bool`.

- [ ] **Step 1: Write the failing test**

`internal/names/names_test.go`:

```go
package names

import "testing"

func TestSlug(t *testing.T) {
	ok := []string{"a", "eng", "pr-review", "schema-migration-2", "AbC123", "twelvechars"}
	bad := map[string]string{
		"":                 "empty",
		"-lead":            "leading dash",
		"1lead":            "leading digit",
		".lead":            "leading dot",
		"has space":        "space",
		"has_underscore":   "underscore",
		"has.dot":          "dot",
		"thirteencharsabc": "thirteen bytes",
	}
	for _, s := range ok {
		if err := Slug(s); err != nil {
			t.Errorf("Slug(%q) = %v, want nil", s, err)
		}
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	for s, why := range bad {
		if err := Slug(s); err == nil {
			t.Errorf("Slug(%q) = nil, want an error (%s)", s, why)
		}
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false (%s)", s, why)
		}
	}
}

func TestName(t *testing.T) {
	ok := []string{"a", "alice", "bob.smith", "ci.bot", "X"}
	bad := map[string]string{
		"":            "empty",
		"-lead":       "leading dash",
		"1lead":       "leading digit",
		".lead":       "leading dot",
		"ci-bot":      "dash",
		"ci_bot":      "underscore",
		"ci2":         "digit",
		"twelvecharsX": "thirteen bytes",
	}
	for _, s := range ok {
		if err := Name(s); err != nil {
			t.Errorf("Name(%q) = %v, want nil", s, err)
		}
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false, want true", s)
		}
	}
	for s, why := range bad {
		if err := Name(s); err == nil {
			t.Errorf("Name(%q) = nil, want an error (%s)", s, why)
		}
	}
}

func TestIsNameByte(t *testing.T) {
	if !IsNameByte('a', 0) || !IsNameByte('Z', 0) {
		t.Error("letters must be name bytes at any position")
	}
	if IsNameByte('.', 0) {
		t.Error("a dot may not lead a name")
	}
	if !IsNameByte('.', 1) {
		t.Error("a dot may follow a name byte")
	}
	for _, b := range []byte{'2', '-', '_', ' '} {
		if IsNameByte(b, 1) {
			t.Errorf("%q must not be a name byte", b)
		}
	}
}

func TestIsNameContinuation(t *testing.T) {
	for _, b := range []byte{'2', '-', '_'} {
		if !IsNameContinuation(b) {
			t.Errorf("IsNameContinuation(%q) = false, want true", b)
		}
	}
	for _, b := range []byte{',', '.', ' ', '@', 'a'} {
		if IsNameContinuation(b) {
			t.Errorf("IsNameContinuation(%q) = true, want false", b)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/names/`
Expected: FAIL — no Go files in the directory.

- [ ] **Step 3: Write the implementation**

`internal/names/names.go`:

```go
// Package names owns Fluffle's two name rules: a slug names a channel or a
// thread, and a name identifies an author. Both are ASCII, both must start
// with a letter, and both are bounded so a terminal can render them in a
// fixed-width column.
package names

import (
	"fmt"
	"regexp"
)

const (
	// MaxSlug is the byte limit on channels.name and threads.title.
	MaxSlug = 12
	// MaxName is the byte limit on an author's name.
	MaxName = 12
)

var (
	slugRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)
	nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z.]*$`)
)

// Slug reports whether s is a legal channel or thread slug: ASCII letters,
// digits, and dashes, leading letter, at most MaxSlug bytes.
func Slug(s string) error {
	if len(s) > MaxSlug {
		return fmt.Errorf("slug %q is %d bytes, max %d", s, len(s), MaxSlug)
	}
	if !slugRe.MatchString(s) {
		return fmt.Errorf("slug %q must start with a letter and hold only letters, digits, and dashes", s)
	}
	return nil
}

// Name reports whether s is a legal author name: ASCII letters and dots,
// leading letter, at most MaxName bytes. It admits no digits, dashes, or
// underscores, so an agent profile is named ci.bot and not ci-bot.
func Name(s string) error {
	if len(s) > MaxName {
		return fmt.Errorf("name %q is %d bytes, max %d", s, len(s), MaxName)
	}
	if !nameRe.MatchString(s) {
		return fmt.Errorf("name %q must start with a letter and hold only letters and dots", s)
	}
	return nil
}

// ValidSlug reports whether s is a legal slug.
func ValidSlug(s string) bool { return Slug(s) == nil }

// ValidName reports whether s is a legal name.
func ValidName(s string) bool { return Name(s) == nil }

// IsNameByte reports whether b may appear in a name at offset pos. A dot is
// legal only after the first byte, so a name never starts with one.
func IsNameByte(b byte, pos int) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
		return true
	case b == '.':
		return pos > 0
	}
	return false
}

// IsNameContinuation reports whether b would have continued a name under a
// looser charset. A mention token followed by one of these is not a name at
// all, so @alice2 does not resolve to an agent called alice.
func IsNameContinuation(b byte) bool {
	switch {
	case b >= '0' && b <= '9', b == '-', b == '_':
		return true
	}
	return false
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/names/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/names
git commit -m "feat(names): one rule per name kind, both bounded to 12 bytes

A slug names a channel or a thread: ASCII letters, digits, and dashes,
leading letter. A name identifies an author: ASCII letters and dots, leading
letter. No digits, dashes, or underscores in a name, so an agent profile is
ci.bot rather than ci-bot.

The 12-byte bound is what lets the TUI give all three text columns one width
constant instead of a per-column width and an adaptive drop ladder."
```

---

### Task 3: Adopt `names` everywhere a name is written or mentioned

`AppendMessageAtWithParent` and `addReaction` are the low-level writers every caller funnels through, so validating there covers the TUI, the CLI, the agent reply, and JSONL import in one place.

**Files:**
- Modify: `internal/store/store.go:77-81` (`CreateChannel`), `:248-251` (`CreateThread`), `:283` (`AppendMessageAtWithParent`), `:483-485` (`AddReaction`)
- Modify: `internal/agentcfg/agentcfg.go:8` (`regexp` import), `:25` (`nameRe`), `:124-126` (`validate`)
- Rewrite: `internal/mentions/mentions.go`
- Test: `internal/mentions/mentions_test.go`, `internal/store/store_test.go`, `internal/agentcfg/agentcfg_test.go`

**Interfaces:**
- Consumes: `names.Slug`, `names.Name`, `names.IsNameByte`, `names.IsNameContinuation`, `names.MaxName` from Task 2.
- Produces: `mentions.Parse(content string) (found []string, request string)` with the new charset; store writes reject bad slugs and names with `ErrInvalid`.

- [ ] **Step 1: Write the failing mention tests**

Replace `internal/mentions/mentions_test.go` with:

```go
package mentions

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		content string
		names   []string
		request string
	}{
		{"@alice what changed?", []string{"alice"}, "what changed?"},
		{"@alice @bob ship it", []string{"alice", "bob"}, "ship it"},
		{"@alice @alice twice", []string{"alice"}, "twice"},
		{"@alice.", []string{"alice"}, ""},
		{"@alice. what changed?", []string{"alice"}, "what changed?"},
		{"@ci.bot fix it", []string{"ci.bot"}, "fix it"},
		{"@alice, ship it", []string{"alice"}, ", ship it"},
		{"hello @alice", nil, ""},
		{"@alice2 ship it", nil, ""},
		{"@alice-bot ship it", nil, ""},
		{"@2alice ship it", nil, ""},
		{"@thirteencharsabc ship it", nil, ""},
		{"", nil, ""},
		{"@", nil, ""},
	}
	for _, tt := range tests {
		gotNames, gotRequest := Parse(tt.content)
		if !reflect.DeepEqual(gotNames, tt.names) || gotRequest != tt.request {
			t.Errorf("Parse(%q) = %q, %q; want %q, %q",
				tt.content, gotNames, gotRequest, tt.names, tt.request)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/mentions/`
Expected: FAIL on `@ci.bot fix it`, `@alice2 ship it`, `@alice-bot ship it`, `@thirteencharsabc ship it`, and `@2alice ship it`.

- [ ] **Step 3: Rewrite `internal/mentions/mentions.go`**

```go
// Package mentions extracts the leading @name run from a message.
package mentions

import (
	"strings"

	"github.com/NoRaincheck/fluffle/internal/names"
)

// Parse returns the distinct names in a leading @name run and the request
// that follows it. The name charset, not a delimiter, ends a token, and a
// token that a looser charset would have continued is not a name at all, so
// @alice2 stays prose.
func Parse(content string) (found []string, request string) {
	rest := content
	seen := map[string]bool{}
	for {
		trimmed := strings.TrimLeft(rest, " \t\n\r")
		if !strings.HasPrefix(trimmed, "@") {
			break
		}
		token, width := scanName(trimmed[1:])
		if width == 0 {
			break
		}
		name := strings.TrimRight(token, ".")
		if name != "" && !seen[name] {
			seen[name] = true
			found = append(found, name)
		}
		rest = trimmed[1+width:]
	}
	if len(found) == 0 {
		return nil, ""
	}
	return found, strings.TrimSpace(rest)
}

// scanName returns the leading run of name bytes in s and its width, or a
// zero width when the run is not a name: too long, or followed by a byte a
// looser charset would have continued.
func scanName(s string) (string, int) {
	end := 0
	for end < len(s) && names.IsNameByte(s[end], end) {
		end++
	}
	if end > names.MaxName {
		return "", 0
	}
	if end < len(s) && names.IsNameContinuation(s[end]) {
		return "", 0
	}
	return s[:end], end
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/mentions/`
Expected: PASS.

- [ ] **Step 5: Add the failing store tests**

Append to `internal/store/store_test.go`. Match the file's existing style: `Open(":memory:")` inline, `defer s.Close()`, and `context.Background()`. Do not introduce a shared test helper.

```go
func TestCreateChannelRejectsBadSlug(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"", "-lead", "1lead", "has space", "has_underscore", "has.dot", "thirteencharsabc"} {
		if _, err := s.CreateChannel(context.Background(), name, "", "", "", "", true); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateChannel(%q) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestCreateThreadRejectsBadSlug(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	chID, err := s.CreateChannel(context.Background(), "eng", "", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"", "Schema migration", "-lead", "has_underscore", "thirteencharsabc"} {
		if _, err := s.CreateThread(context.Background(), chID, title); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateThread(%q) = %v, want ErrInvalid", title, err)
		}
	}
}

func TestAppendMessageRejectsBadName(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	chID, err := s.CreateChannel(context.Background(), "eng", "", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	thID, err := s.CreateThread(context.Background(), chID, "pr-review")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "ci-bot", "ci_bot", "ci2", ".lead"} {
		if _, err := s.AppendMessage(context.Background(), thID, name, "agent", "assistant", "hi"); !errors.Is(err, ErrInvalid) {
			t.Errorf("AppendMessage(name=%q) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestAddReactionRejectsBadName(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	chID, err := s.CreateChannel(context.Background(), "eng", "", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	thID, err := s.CreateThread(context.Background(), chID, "pr-review")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AppendMessage(context.Background(), thID, "alice", "human", "user", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddReaction(context.Background(), id, "\U0001F440", "ci-bot", "agent"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("AddReaction = %v, want ErrInvalid", err)
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/store/ -run 'RejectsBad'`
Expected: FAIL — every one of those calls currently succeeds.

- [ ] **Step 7: Wire the rules into the store**

Add `"github.com/NoRaincheck/fluffle/internal/names"` to `internal/store/store.go`'s imports, then make four edits.

`CreateChannel` — replace the blank check:

```go
	if err := names.Slug(name); err != nil {
		return 0, invalid("%v", err)
	}
```

`CreateThread` — replace the blank check:

```go
	if err := names.Slug(title); err != nil {
		return 0, invalid("%v", err)
	}
```

`AppendMessageAtWithParent` — insert as its first statement:

```go
	if err := names.Name(name); err != nil {
		return 0, invalid("%v", err)
	}
```

`AddReaction` — insert as its first statement:

```go
	if err := names.Name(name); err != nil {
		return err
	}
```

- [ ] **Step 8: Point `agentcfg` at the shared rule**

In `internal/agentcfg/agentcfg.go`, `regexp` is used only by `nameRe`, so delete the `nameRe` declaration and the `"regexp"` import, add `"github.com/NoRaincheck/fluffle/internal/names"`, and replace the check in `validate`:

```go
	if err := names.Name(a.Name); err != nil {
		return fmt.Errorf("%s: invalid name: %v", where, err)
	}
```

- [ ] **Step 9: Run the gate and fix the fallout**

Run: `go test ./... 2>&1 | grep -v '^ok' | head -40`
Expected: failures wherever a test used a name or slug the new rules reject. Fix the *inputs*, never the rules: agent `ci-bot` becomes `ci.bot`, thread title `something broke` becomes `something-broke`, and a title of `test` stays. Where a test asserted that a spaced title was accepted, that assertion is a behavior change and is updated deliberately here.

- [ ] **Step 10: Commit**

```bash
git add internal/store internal/mentions internal/agentcfg
git commit -m "feat(names): enforce the two name rules at every write path

AppendMessageAtWithParent and AddReaction are the low-level writers every
caller funnels through, so validating there covers the TUI, the CLI, the
agent reply, and JSONL import in one place. CreateChannel and CreateThread
validate slugs.

agentcfg and mentions now read internal/names instead of carrying their own
regex, so there is one definition of a legal name. A mention token that a
looser charset would have continued is not a name at all, which keeps
@alice2 from resolving to an agent called alice."
```

---

### Task 4: `store.ListRows`

Grouping, representative selection, and reply counts move into SQL, where they are a subquery rather than client-side bookkeeping. The `CAST(... AS TEXT)` wrapper is required: sqlc's SQLite parser infers `interface{}` for a bare `COALESCE` over a correlated subquery. Verified against `d5cebe88`'s toolchain to generate exact `string` and `int64` fields.

**Files:**
- Create: `internal/store/queries/rows.sql`
- Modify: `internal/store/queries/messages.sql` (delete the `ListInbox` query)
- Modify: `internal/store/store.go:241-246` (delete `InboxMessage`), `:443-480` (replace `inboxFromRow` and `ListInbox`)
- Regenerate: `internal/db/*.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `store.Row`, `store.GranularityMessage`, `store.GranularityThread`, `store.GranularityChannel`, `(*Store).ListRows(ctx context.Context, granularity string, limit int) ([]Row, error)`. Deletes `store.InboxMessage` and `(*Store).ListInbox`.

- [ ] **Step 1: Write the queries**

`internal/store/queries/rows.sql`:

```sql
-- name: ListMessageRows :many
SELECT m.id AS id,
       m.thread_id AS thread_id,
       COALESCE(m.created_at, '') AS created_at,
       c.name AS channel,
       t.title AS thread,
       m.name AS name,
       m.content AS content
FROM messages m
JOIN threads t ON t.id = m.thread_id
JOIN channels c ON c.id = t.channel_id
ORDER BY m.created_at DESC, m.id DESC
LIMIT ?;

-- name: ListThreadRows :many
SELECT t.id AS id,
       t.id AS thread_id,
       COALESCE((SELECT m.created_at FROM messages m
                 WHERE m.thread_id = t.id
                 ORDER BY m.created_at DESC, m.id DESC LIMIT 1), t.created_at) AS created_at,
       c.name AS channel,
       t.title AS thread,
       CAST(COALESCE((SELECT m.name FROM messages m
                      WHERE m.thread_id = t.id
                      ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '') AS TEXT) AS name,
       CAST(COALESCE((SELECT m.content FROM messages m
                      WHERE m.thread_id = t.id
                      ORDER BY m.seq ASC LIMIT 1), '') AS TEXT) AS content,
       (SELECT COUNT(*) FROM messages m WHERE m.thread_id = t.id) AS msg_count
FROM threads t
JOIN channels c ON c.id = t.channel_id
ORDER BY created_at DESC, t.id DESC
LIMIT ?;

-- name: ListChannelRows :many
SELECT c.id AS id,
       0 AS thread_id,
       COALESCE((SELECT m.created_at FROM messages m JOIN threads t2 ON t2.id = m.thread_id
                 WHERE t2.channel_id = c.id
                 ORDER BY m.created_at DESC, m.id DESC LIMIT 1), c.created_at) AS created_at,
       c.name AS channel,
       '' AS thread,
       CAST(COALESCE((SELECT m.name FROM messages m JOIN threads t2 ON t2.id = m.thread_id
                      WHERE t2.channel_id = c.id
                      ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '') AS TEXT) AS name,
       CAST(COALESCE((SELECT m.content FROM messages m JOIN threads t2 ON t2.id = m.thread_id
                      WHERE t2.channel_id = c.id
                      ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '') AS TEXT) AS content,
       (SELECT COUNT(*) FROM messages m JOIN threads t2 ON t2.id = m.thread_id
        WHERE t2.channel_id = c.id) AS msg_count
FROM channels c
ORDER BY created_at DESC, c.id DESC
LIMIT ?;
```

- [ ] **Step 2: Delete the `ListInbox` query**

Remove the `-- name: ListInbox :many` block from `internal/store/queries/messages.sql`, from its comment through its trailing `LIMIT ?;`.

- [ ] **Step 3: Generate and confirm the field types**

Run: `go tool sqlc generate && grep -n 'type List.*Row struct' -A 9 internal/db/rows.sql.go`
Expected: three row structs, each with `ID int64`, `ThreadID int64`, `CreatedAt string`, `Channel string`, `Thread string`, `Name string`, `Content string`, and `MsgCount int64` on the two grouped ones. `ListInbox` is gone from `internal/db/messages.sql.go` and `internal/db/querier.go`.

If `Name` or `Content` came out as `interface{}`, the `CAST` wrapper is missing on that column; add it and regenerate.

- [ ] **Step 4: Write the failing test**

Append to `internal/store/store_test.go`:

```go
func TestListRowsGranularityCounts(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	eng, err := s.CreateChannel(context.Background(), "eng", "", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChannel(context.Background(), "dev", "", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	alpha, err := s.CreateThread(context.Background(), eng, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := s.CreateThread(context.Background(), eng, "beta")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.CreateThread(context.Background(), eng, "empty")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		thread int64
		name   string
		body   string
	}{
		{alpha, "alice", "first line of alpha\nsecond line"},
		{alpha, "bob", "reply in alpha"},
		{alpha, "ci.bot", "another reply in alpha"},
		{beta, "alice", "only line in beta"},
	} {
		if _, err := s.AppendMessage(context.Background(), m.thread, m.name, "human", "user", m.body); err != nil {
			t.Fatal(err)
		}
	}

	msgs, err := s.ListRows(context.Background(), GranularityMessage, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("message rows = %d, want 4", len(msgs))
	}
	for _, r := range msgs {
		if r.Count != 1 {
			t.Errorf("message row count = %d, want 1", r.Count)
		}
		if r.Channel != "eng" {
			t.Errorf("message row channel = %q, want eng", r.Channel)
		}
	}

	threads, err := s.ListRows(context.Background(), GranularityThread, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 3 {
		t.Fatalf("thread rows = %d, want 3", len(threads))
	}
	if threads[0].Thread != "alpha" || threads[0].Count != 3 {
		t.Errorf("first thread row = %q/%d, want alpha/3", threads[0].Thread, threads[0].Count)
	}
	if threads[0].Content != "first line of alpha\nsecond line" {
		t.Errorf("thread row content = %q, want the original post", threads[0].Content)
	}
	if threads[0].Name != "ci.bot" {
		t.Errorf("thread row name = %q, want the newest reply's author", threads[0].Name)
	}
	if threads[0].ID != alpha || threads[0].ThreadID != alpha {
		t.Errorf("thread row ids = %d/%d, want %d", threads[0].ID, threads[0].ThreadID, alpha)
	}
	var sawEmpty bool
	for _, r := range threads {
		if r.Thread == "empty" {
			sawEmpty = true
			if r.Count != 0 {
				t.Errorf("an empty thread must report 0, got %d", r.Count)
			}
			if r.Content != "" || r.Name != "" {
				t.Errorf("an empty thread must carry no content or name: %+v", r)
			}
		}
	}
	if !sawEmpty {
		t.Error("a thread with no messages must still appear")
	}

	channels, err := s.ListRows(context.Background(), GranularityChannel, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 {
		t.Fatalf("channel rows = %d, want 2", len(channels))
	}
	if channels[0].Channel != "eng" || channels[0].Count != 4 {
		t.Errorf("first channel row = %q/%d, want eng/4", channels[0].Channel, channels[0].Count)
	}
	if channels[0].Thread != "" || channels[0].ThreadID != 0 {
		t.Errorf("a channel row must carry no thread, got %q/%d", channels[0].Thread, channels[0].ThreadID)
	}
	if channels[0].Name != "ci.bot" {
		t.Errorf("channel row name = %q, want the newest message's author", channels[0].Name)
	}
}

func TestListRowsRejectsUnknownGranularity(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ListRows(context.Background(), "folder", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ListRows(folder) = %v, want ErrInvalid", err)
	}
}

func TestListRowsClampsLimit(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, limit := range []int{0, -5, 100000} {
		if _, err := s.ListRows(context.Background(), GranularityMessage, limit); err != nil {
			t.Fatalf("limit %d must not error: %v", limit, err)
		}
	}
}
```

- [ ] **Step 5: Run it to verify it fails**

Run: `go test ./internal/store/ -run TestListRows`
Expected: FAIL — `ListRows` and `GranularityMessage` are undefined.

- [ ] **Step 6: Implement**

In `internal/store/store.go`, delete the `InboxMessage` type, `inboxFromRow`, and `ListInbox`. Add:

```go
// Granularity selects what one row of a ListRows result is.
const (
	GranularityMessage = "message"
	GranularityThread  = "thread"
	GranularityChannel = "channel"
)

const (
	defaultRowLimit = 200
	maxRowLimit     = 500
)

// Row is one line of the cross-channel feed. ID is the message, thread, or
// channel id depending on the granularity. ThreadID is 0 at channel
// granularity. Name is the author of the group's newest message, except at
// message granularity where it is that message's own author. Content is the
// whole representative message, not its first line: choosing a line is a
// rendering decision. Count is the group's message count, and 1 at message
// granularity.
type Row struct {
	ID       int64
	ThreadID int64
	Time     string
	Channel  string
	Thread   string
	Name     string
	Content  string
	Count    int
}

// ListRows returns the feed at one granularity, newest first. Grouping,
// representative selection, and reply counts are the query's job, so the
// caller holds no rule about which message represents a group.
func (s *Store) ListRows(ctx context.Context, granularity string, limit int) ([]Row, error) {
	if limit <= 0 {
		limit = defaultRowLimit
	}
	if limit > maxRowLimit {
		limit = maxRowLimit
	}
	switch granularity {
	case GranularityMessage:
		rows, err := s.q.ListMessageRows(ctx, int64(limit))
		if err != nil {
			return nil, err
		}
		out := make([]Row, 0, len(rows))
		for _, r := range rows {
			out = append(out, Row{
				ID: r.ID, ThreadID: r.ThreadID, Time: r.CreatedAt,
				Channel: r.Channel, Thread: r.Thread, Name: r.Name,
				Content: r.Content, Count: 1,
			})
		}
		return out, nil
	case GranularityThread:
		rows, err := s.q.ListThreadRows(ctx, int64(limit))
		if err != nil {
			return nil, err
		}
		out := make([]Row, 0, len(rows))
		for _, r := range rows {
			out = append(out, Row{
				ID: r.ID, ThreadID: r.ThreadID, Time: r.CreatedAt,
				Channel: r.Channel, Thread: r.Thread, Name: r.Name,
				Content: r.Content, Count: int(r.MsgCount),
			})
		}
		return out, nil
	case GranularityChannel:
		rows, err := s.q.ListChannelRows(ctx, int64(limit))
		if err != nil {
			return nil, err
		}
		out := make([]Row, 0, len(rows))
		for _, r := range rows {
			out = append(out, Row{
				ID: r.ID, ThreadID: r.ThreadID, Time: r.CreatedAt,
				Channel: r.Channel, Thread: r.Thread, Name: r.Name,
				Content: r.Content, Count: int(r.MsgCount),
			})
		}
		return out, nil
	default:
		return nil, invalid("granularity %q must be %s, %s, or %s",
			granularity, GranularityMessage, GranularityThread, GranularityChannel)
	}
}
```

- [ ] **Step 7: Run the gate**

Run: `go test ./internal/store/ && go tool sqlc diff`
Expected: PASS, and sqlc reports that `internal/db` matches the generator.

- [ ] **Step 8: Commit**

```bash
git add internal/store internal/db
git commit -m "feat(store): one ListRows at three granularities

The daemon decides what a row is. message, thread, and channel are three
sqlc queries behind one dispatcher, so the client has no
representative-selection rule, no group-then-filter ordering rule, and no
reply-count arithmetic.

A thread row carries the original post as its content and the newest reply's
author as its name. A thread or channel with no messages still appears,
ordered by its own creation time, with an empty name and content.

The CAST wrapper on the correlated subqueries is load-bearing: sqlc infers
interface{} for a bare COALESCE over one. Replaces InboxMessage and
ListInbox."
```

---

### Task 5: `GET /v1/rows`

**Files:**
- Modify: `internal/apiserver/server.go:383-400`
- Replace: `internal/apiserver/inbox_test.go` with `internal/apiserver/rows_test.go`

**Interfaces:**
- Consumes: `(*Store).ListRows` from Task 4.
- Produces: `GET /v1/rows?g=<granularity>&limit=<n>` → `[]store.Row`. Deletes `GET /v1/inbox`.

- [ ] **Step 1: Write the failing test**

`internal/apiserver/rows_test.go`:

```go
package apiserver

import (
	"encoding/json"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func seedRows(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chID, err := s.CreateChannel(context.Background(), "eng", "", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	thID, err := s.CreateThread(context.Background(), chID, "pr-review")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"looks great", "ship it"} {
		if _, err := s.AppendMessage(context.Background(), thID, "alice", "human", "user", body); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestRowsEndpoint(t *testing.T) {
	h := NewHandler(seedRows(t))
	for _, g := range []string{"message", "thread", "channel"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rows?g="+g, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("g=%s status = %d, want 200", g, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("g=%s Content-Type = %q", g, ct)
		}
		var rows []store.Row
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Errorf("g=%s returned no rows", g)
		}
	}
}

func TestRowsRejectsBadGranularity(t *testing.T) {
	h := NewHandler(seedRows(t))
	for _, q := range []string{"", "?g=folder", "?g="} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rows"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("/v1/rows%s status = %d, want 400", q, rec.Code)
		}
		var eb struct{ Code, Message string }
		if err := json.Unmarshal(rec.Body.Bytes(), &eb); err != nil {
			t.Fatal(err)
		}
		if eb.Code == "" || eb.Message == "" {
			t.Fatalf("/v1/rows%s error envelope = %+v", q, eb)
		}
	}
}

func TestInboxEndpointIsGone(t *testing.T) {
	h := NewHandler(seedRows(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inbox", nil))
	if rec.Code == http.StatusOK {
		t.Fatal("/v1/inbox still answers")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/apiserver/ -run 'TestRows|TestInboxEndpointIsGone'`
Expected: FAIL — `/v1/rows` is an unknown route and `/v1/inbox` still answers 200.

- [ ] **Step 3: Replace the handler**

Delete the `/v1/inbox` handler block in `internal/apiserver/server.go` and add, in its place:

```go
	mux.HandleFunc("/v1/rows", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		rows, err := s.ListRows(r.Context(), r.URL.Query().Get("g"), limit)
		if err != nil {
			if errors.Is(err, store.ErrInvalid) {
				writeErr(w, http.StatusBadRequest, "BAD_ARGS", err.Error())
				return
			}
			writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rows)
	})
```

- [ ] **Step 4: Run the gate**

Run: `go test ./internal/apiserver/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git rm -q internal/apiserver/inbox_test.go
git add internal/apiserver
git commit -m "feat(api): replace GET /v1/inbox with GET /v1/rows?g=

One endpoint, three granularities, one row type shared with the CLI. A
missing or unknown g is a 400 with the standard error envelope rather than a
silently defaulted feed."
```

---

### Task 6: `flf inbox`, thread titles, and import slugs

**Files:**
- Modify: `cmd/flf/main.go:237-250` (`validateInbox`), `:352-381` (`inboxCmd`), `:994` (import title)
- Test: `cmd/flf/main_test.go`, `cmd/flf/e2e_test.go`

**Interfaces:**
- Consumes: `store.Row` from Task 4, `names` from Tasks 2 and 3.
- Produces: `flf inbox --limit N --json` prints `[]store.Row`; `flf thread import` titles a thread `imp-<8 hex>`.

- [ ] **Step 1: Write the failing test**

Append to `cmd/flf/main_test.go`:

```go
func TestValidateRows(t *testing.T) {
	rows := []store.Row{{ID: 1, ThreadID: 2, Channel: "eng", Thread: "pr-review", Name: "alice", Content: "hi", Count: 1}}
	if err := validateRows(&rows)(); err != nil {
		t.Fatalf("a well-formed row was rejected: %v", err)
	}
	for label, bad := range map[string]store.Row{
		"zero id":      {ID: 0, ThreadID: 2, Channel: "eng", Thread: "t", Name: "alice", Content: "hi"},
		"blank name":   {ID: 1, ThreadID: 2, Channel: "eng", Thread: "t", Content: "hi"},
		"blank thread": {ID: 1, ThreadID: 2, Channel: "eng", Name: "alice", Content: "hi"},
	} {
		rows := []store.Row{bad}
		if err := validateRows(&rows)(); err == nil {
			t.Errorf("%s: the row was accepted", label)
		}
	}
}

func TestImportSlugIsValid(t *testing.T) {
	slug := importSlug("session.jsonl")
	if err := names.Slug(slug); err != nil {
		t.Fatalf("importSlug = %q: %v", slug, err)
	}
	if len(slug) > names.MaxSlug {
		t.Fatalf("importSlug = %q is %d bytes", slug, len(slug))
	}
	if importSlug("a.jsonl") == importSlug("b.jsonl") {
		t.Fatal("two files must not produce the same slug")
	}
}
```

Add `"github.com/NoRaincheck/fluffle/internal/names"` to the test imports if absent.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/flf/ -run 'TestValidateRows|TestImportSlugIsValid'`
Expected: FAIL — `validateRows` and `importSlug` are undefined.

- [ ] **Step 3: Implement**

Replace `validateInbox` in `cmd/flf/main.go` with:

```go
func validateRows(list *[]store.Row) apiResponseCheck {
	return validateList(list, func(row store.Row) error {
		if row.ID <= 0 {
			return fmt.Errorf("row has an invalid id %d", row.ID)
		}
		if strings.TrimSpace(row.Name) == "" || strings.TrimSpace(row.Content) == "" {
			return errors.New("row has an empty name or content")
		}
		if strings.TrimSpace(row.Channel) == "" {
			return errors.New("row has an empty channel")
		}
		return nil
	})
}

// importSlug derives a valid thread title from an import file path: four
// characters of prefix and eight of hash, which is always a legal slug and
// never truncated.
func importSlug(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "imp-" + hex.EncodeToString(sum[:])[:8]
}
```

Replace the body of `inboxCmd` from the `var messages` line through the print loop:

```go
	var rows []store.Row
	u := base + "/v1/rows?g=message&limit=" + strconv.Itoa(*limit)
	if code := apiGet(u, "", &rows, validateRows(&rows)); code != 0 {
		return code
	}
	if rows == nil {
		rows = []store.Row{}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rows)
		return 0
	}
	for _, row := range rows {
		first, _, _ := strings.Cut(row.Content, "\n")
		fmt.Printf("%s/%d %s/%d %s: %s\n", row.Channel, row.ID, row.Thread, row.ThreadID, row.Name, first)
	}
	return 0
```

Replace the import title in the `thread import` path:

```go
	body := map[string]any{"Title": importSlug(*file)}
```

Add the imports `"crypto/sha256"` and `"encoding/hex"`, and drop `"path/filepath"` if nothing else uses it.

- [ ] **Step 4: Run the gate and fix the fallout**

Run: `go test ./cmd/flf/ 2>&1 | head -40`
Expected: failures in CLI and e2e tests that assert on `/v1/inbox` fixtures or on free-text thread titles. Repoint fixtures at `/v1/rows?g=message` with `Row` bodies, and re-slug the titles: `--title "something broke"` becomes `--title something-broke`, `--title "test"` stays. Where a test asserted that a spaced title was accepted, that is a behavior change and the assertion changes deliberately here.

- [ ] **Step 5: Commit**

```bash
git add cmd/flf
git commit -m "feat(cli): inbox prints rows and import titles are slugs

flf inbox reads /v1/rows?g=message and prints channel and thread ids, the
author, and the message's first line. Its --json shape changes from a message
record to a Row and it loses the per-thread seq column, because a row is no
longer a message record. seq is still available through flf thread export and
/v1/threads/:id/messages.

flf thread import derived its title from the file's base name, which produced
a space and is now illegal. It derives imp-<8 hex of the path> instead, which
is always a valid 12-byte slug and never truncated."
```

---

### Task 7: The TUI lists rows

**Files:**
- Rewrite: `internal/tui/model.go`, `internal/tui/api.go`, `internal/tui/styles.go`
- Create: `internal/tui/rows.go`, `internal/tui/rows_test.go`
- Delete: `internal/tui/session.go`, `model_test.go`, `session_test.go`, `overlay_test.go`, `inbox_layout_test.go`, `simple_test.go`, `api_test.go`, `sort_filter_test.go`, `create_test.go`, `thread_table_test.go`

**Interfaces:**
- Consumes: `store.Row`, `store.Granularity*` from Task 4; `screen`, `termtext` from Task 1.
- Produces: `tui.New(base string) tea.Model`; `model` with fifteen fields; `rowLine(w int, row store.Row, selected bool) string`; `renderRows(w, h int, rows []store.Row, cursor, scroll int) string`; `contentWidth(w int) int`; `(*model).syncThread() tea.Cmd`; `(*model).clamp()`; `(*model).applyRows([]store.Row)`; `(*apiClient).ListRows`, `ListMessages`, `SendReply`.

- [ ] **Step 1: Write the failing tests and shared test helpers**

Create `internal/tui/rows_test.go`. The `key` and `toModel` helpers live here and are reused by Tasks 8 through 10, because `handleKey` returns `*model` while `New` returns `model`.

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// toModel unwraps a tea.Model for assertions, whether it holds a model or a
// pointer to one.
func toModel(t tea.Model) model {
	switch v := t.(type) {
	case model:
		return v
	case *model:
		return *v
	}
	panic("not a tui model")
}

// key builds a KeyMsg for a named key or a literal rune run.
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func plain(s string) string { return termtext.StripANSI(s) }

func testRow() store.Row {
	return store.Row{
		ID: 7, ThreadID: 3, Time: "2026-09-23T15:04:12Z",
		Channel: "eng", Thread: "pr-review", Name: "ci.bot",
		Content: "build passed", Count: 3,
	}
}

func TestRowLineFillsItsWidth(t *testing.T) {
	for _, w := range []int{MinWidth, 80, 200} {
		line := plain(rowLine(w, testRow(), false))
		if got := termtext.DisplayWidth(line); got != w {
			t.Errorf("rowLine(%d) is %d cells: %q", w, got, line)
		}
	}
}

func TestRowLineColumns(t *testing.T) {
	line := plain(rowLine(80, testRow(), false))
	if !strings.HasPrefix(line, "  Sep 23 15:04  eng         pr-review    ci.bot  ") {
		t.Fatalf("row layout changed: %q", line)
	}
	if !strings.Contains(line, "build passed") {
		t.Errorf("row is missing its content: %q", line)
	}
}

func TestRowLineSelectedCarriesTheCursor(t *testing.T) {
	if !strings.HasPrefix(plain(rowLine(80, testRow(), true)), "▸ ") {
		t.Error("a selected row must carry the cursor marker")
	}
}

func TestRowLineTruncatesOverlongColumns(t *testing.T) {
	r := testRow()
	r.Channel = strings.Repeat("x", 40)
	r.Name = strings.Repeat("y", 40)
	r.Content = "first line\nsecond line"
	line := plain(rowLine(80, r, false))
	if got := termtext.DisplayWidth(line); got != 80 {
		t.Fatalf("an overlong column broke the row width: %d", got)
	}
	if strings.Contains(line, "second line") {
		t.Error("only the first line of the content may be rendered")
	}
}

func TestRowLineCountColumn(t *testing.T) {
	prefix := func(r store.Row) string {
		cells := []rune(plain(rowLine(80, r, false)))
		return string(cells[rowPrefixW-CountW : rowPrefixW])
	}
	if got := prefix(testRow()); got != "  3" {
		t.Errorf("count cell = %q, want %q", got, "  3")
	}
	one := testRow()
	one.Count = 1
	if got := prefix(one); strings.TrimSpace(got) != "" {
		t.Errorf("a count of 1 must leave the count column blank, got %q", got)
	}
	huge := testRow()
	huge.Count = 5000
	if got := prefix(huge); got != "99+" {
		t.Errorf("an overflowing count = %q, want %q", got, "99+")
	}
}

func TestRowLineSanitizesContent(t *testing.T) {
	r := testRow()
	r.Content = "clean\x1b]0;pwned\x07 text"
	line := plain(rowLine(80, r, false))
	if strings.Contains(line, "\x1b") {
		t.Errorf("an escape sequence reached the row: %q", line)
	}
	if strings.Contains(line, "pwned") {
		t.Errorf("the OSC payload must be dropped entirely: %q", line)
	}
	if !strings.Contains(line, "clean text") {
		t.Errorf("sanitization must keep the surrounding text: %q", line)
	}
}

func TestRenderRowsEmptyState(t *testing.T) {
	got := plain(renderRows(80, 10, nil, 0, 0))
	if !strings.Contains(got, "no rows") {
		t.Errorf("empty state = %q", got)
	}
	if n := strings.Count(got, "\n") + 1; n != 10 {
		t.Errorf("renderRows returned %d lines, want 10", n)
	}
}

func TestRenderRowsWindowFollowsScroll(t *testing.T) {
	rows := make([]store.Row, 20)
	for i := range rows {
		rows[i] = store.Row{ID: int64(i), Channel: "eng", Name: "alice", Content: "body"}
	}
	got := plain(renderRows(80, 5, rows, 10, 10))
	if !strings.Contains(got, "body") {
		t.Fatalf("window is empty: %q", got)
	}
	if n := strings.Count(got, "\n") + 1; n != 5 {
		t.Errorf("renderRows returned %d lines, want 5", n)
	}
}

func TestNewStartsOnMessageGranularity(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	if m.granularity != store.GranularityMessage {
		t.Fatalf("granularity = %q, want message", m.granularity)
	}
}

func TestApplyRowsPreservesCursorByID(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.height = 40
	m.rows = []store.Row{{ID: 1}, {ID: 2}, {ID: 3}}
	m.cursor = 2
	m.applyRows([]store.Row{{ID: 9}, {ID: 2}, {ID: 7}})
	if m.rows[m.cursor].ID != 2 {
		t.Fatalf("cursor landed on id %d, want 2", m.rows[m.cursor].ID)
	}
}

func TestApplyRowsReversesOnDemand(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.reversed = true
	m.applyRows([]store.Row{{ID: 1}, {ID: 2}, {ID: 3}})
	if m.rows[0].ID != 3 {
		t.Fatalf("reversed first row = %d, want 3", m.rows[0].ID)
	}
}

func TestApplyRowsClampsAnEmptyResult(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.rows = []store.Row{{ID: 1}}
	m.cursor = 5
	m.applyRows(nil)
	if m.cursor != 0 || m.scroll != 0 {
		t.Fatalf("cursor=%d scroll=%d, want 0 0", m.cursor, m.scroll)
	}
}

func TestTooNarrow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 40, 40
	if !m.tooNarrow() {
		t.Error("40 columns must be too narrow")
	}
	m.width, m.height = 120, 10
	if !m.tooNarrow() {
		t.Error("10 rows must be too narrow")
	}
	m.width, m.height = 120, 40
	if m.tooNarrow() || !m.split() {
		t.Error("120x40 must be a split")
	}
	m.width, m.height = 80, 40
	if m.tooNarrow() || m.split() {
		t.Error("80 columns must be usable but not split")
	}
}

func TestNarrowNoticeNamesTheMinimum(t *testing.T) {
	got := narrowNotice(40, 10)
	if !strings.Contains(got, "71") || !strings.Contains(got, "24") || !strings.Contains(got, "40x10") {
		t.Fatalf("notice = %q", got)
	}
}

func TestTitleLineNamesGranularity(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityChannel
	m.rows = []store.Row{{ID: 1}}
	if !strings.Contains(plain(m.titleLine()), "channel") {
		t.Fatalf("title = %q", m.titleLine())
	}
}

func TestThreadTitleIsEmptyForAChannelRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.rows = []store.Row{{ID: 1, Channel: "eng", ThreadID: 0}}
	if m.threadTitle() != "" {
		t.Fatalf("threadTitle = %q, want empty", m.threadTitle())
	}
	m.rows = []store.Row{{ID: 1, Channel: "eng", Thread: "pr-review", ThreadID: 5}}
	if m.threadTitle() != "eng › pr-review" {
		t.Fatalf("threadTitle = %q", m.threadTitle())
	}
}

func TestViewIsNarrowNoticeWhenTooNarrow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 40, 10
	if !strings.Contains(plain(m.View()), "needs 71 columns") {
		t.Fatalf("view = %q", m.View())
	}
}

func TestViewIsExactlyTerminalHeight(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 200, 40
	m.rows = []store.Row{testRow()}
	if n := strings.Count(m.View(), "\n") + 1; n != 40 {
		t.Errorf("View is %d lines, want 40", n)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tui/ -run 'TestRowLine|TestRenderRows|TestNew|TestApplyRows|TestTooNarrow|TestNarrow|TestTitleLine|TestThreadTitle|TestView'`
Expected: FAIL — `rowLine` and `MinWidth` are undefined.

- [ ] **Step 3: Write `internal/tui/rows.go`**

```go
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// The row prefix is fixed: a cursor, four 12-cell columns, four gaps of two
// cells, and a three-cell count. Every text column is MaxSlug or MaxName wide,
// so one constant replaces a per-column width and an adaptive drop ladder.
const (
	ColW        = 12
	CountW      = 3
	ColGap      = 2
	CursorW     = 2
	rowPrefixW  = CursorW + ColW + ColGap + ColW + ColGap + ColW + ColGap + ColW + ColGap + CountW
	MinContentW = 10
	MinWidth    = rowPrefixW + MinContentW
	MinHeight   = 24
	chromeH     = 3

	// MinSplitWidth is the width at which the list and the thread can sit
	// side by side. Below it the list takes the whole width and Enter shows
	// the thread in its place.
	MinSplitWidth = 110
	ListW         = 74
)

// contentWidth is a row's content column width, or zero when the pane is too
// narrow to draw one.
func contentWidth(w int) int {
	if cw := w - rowPrefixW; cw > 0 {
		return cw
	}
	return 0
}

// cell is one fixed-width column: sanitized, then truncated so a hand-edited
// database cannot shift the row.
func cell(s string) string {
	return fmt.Sprintf("%-*s", ColW, termtext.Truncate(termtext.SanitizeLine(s), ColW, ""))
}

// firstLine is the representative message's first line. A row is a preview,
// and a preview is one line.
func firstLine(content string) string {
	line, _, _ := strings.Cut(content, "\n")
	return line
}

func rowLine(w int, row store.Row, selected bool) string {
	marker := "  "
	if selected {
		marker = rowSelectedStyle.Render("▸ ")
	}
	count := ""
	if row.Count > 1 {
		count = strconv.Itoa(row.Count)
		if len(count) > CountW-1 {
			count = "99+"
		}
	}
	content := termtext.Truncate(termtext.SanitizeLine(firstLine(row.Content)), contentWidth(w), "…")
	return marker +
		cell(formatTime(row.Time)) + strings.Repeat(" ", ColGap) +
		cell(row.Channel) + strings.Repeat(" ", ColGap) +
		cell(row.Thread) + strings.Repeat(" ", ColGap) +
		cell(row.Name) + strings.Repeat(" ", ColGap) +
		fmt.Sprintf("%*s", CountW, count) + content
}

// renderRows draws the visible window and pads to exactly h rows so the
// title, hint, and status bands keep their places.
func renderRows(w, h int, rows []store.Row, cursor, scroll int) string {
	if h <= 0 || w <= 0 {
		return ""
	}
	lines := make([]string, 0, h)
	if len(rows) == 0 {
		lines = append(lines, pad(placeholder("no rows — press g to change group"), w))
		scroll = 0
	}
	for i := scroll; i < len(rows) && len(lines) < h; i++ {
		lines = append(lines, pad(rowLine(w, rows[i], i == cursor), w))
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return strings.Join(lines, "\n")
}

// pad right-fills a rendered line to w cells, measuring cells rather than
// runes so styling and wide graphemes do not shift the row.
func pad(s string, w int) string {
	if n := w - termtext.DisplayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// formatTime is exactly ColW cells for every parseable input.
func formatTime(t string) string {
	if t == "" {
		return strings.Repeat(" ", ColW)
	}
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return cell(t)
	}
	return parsed.Format("Jan 02 15:04")
}

// formatClock is the short form used in the thread view.
func formatClock(t string) string {
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return "     "
	}
	return parsed.Format("15:04")
}
```

- [ ] **Step 4: Rewrite `internal/tui/styles.go`**

```go
package tui

import "github.com/charmbracelet/lipgloss"

var (
	accent      = lipgloss.Color("#89b4fa")
	dim         = lipgloss.Color("#646478")
	humanColor  = lipgloss.Color("#89b4fa")
	agentColor  = lipgloss.Color("#b7b4fa")
	systemColor = lipgloss.Color("#646478")
	fg          = lipgloss.Color("#cdd6f4")
	sep         = lipgloss.Color("#585062")
	selBg       = lipgloss.Color("#313244")
	selFg       = lipgloss.Color("#f5f5f5")
	statusBg    = lipgloss.Color("#181825")
	modalBorder = lipgloss.Color("#89b4fa")
	modalFg     = lipgloss.Color("#f5f5f5")
	errColor    = lipgloss.Color("#f38ba8")
)

var (
	rowSelectedStyle  = lipgloss.NewStyle().Foreground(selFg).Background(selBg)
	titleStyle        = lipgloss.NewStyle().Foreground(accent).Bold(true)
	threadHeaderStyle = lipgloss.NewStyle().Foreground(sep)
	threadBodyStyle   = lipgloss.NewStyle().Foreground(fg)
	sepStyle          = lipgloss.NewStyle().Foreground(sep)
	dimStyle          = lipgloss.NewStyle().Foreground(dim)
	placeholderStyle  = lipgloss.NewStyle().Foreground(dim).Italic(true)
	humanNameStyle    = lipgloss.NewStyle().Foreground(humanColor)
	agentNameStyle    = lipgloss.NewStyle().Foreground(agentColor)
	systemNameStyle   = lipgloss.NewStyle().Foreground(systemColor)
	modalTitleStyle   = lipgloss.NewStyle().Foreground(accent).Bold(true)
	modalHintStyle    = lipgloss.NewStyle().Foreground(dim)
	modalErrorStyle   = lipgloss.NewStyle().Foreground(errColor)
	statusStyle       = lipgloss.NewStyle().Background(statusBg).Foreground(accent).Padding(0, 1)
	hintKeyStyle      = lipgloss.NewStyle().Foreground(accent).Bold(true)
)

// nameStyle colours an author by author_type. This is the only colour rule
// in the app.
func nameStyle(authorType string) lipgloss.Style {
	switch authorType {
	case "human":
		return humanNameStyle
	case "agent":
		return agentNameStyle
	default:
		return systemNameStyle
	}
}
```

- [ ] **Step 5: Rewrite `internal/tui/api.go`**

Keep `EnsureDaemon`, `do`, `mutationTransportError`, `readAPIError`, `decodeStrictJSON`, and `jsonBody` exactly as they are. Delete `ListChannels`, `CreateChannel`, `ListThreads`, `CreateThread`, `ListSessions`, `GetSession`, `ListInbox`, `AddReaction`, `filterChannels`, and `doJSON`. Add:

```go
// get performs a GET and decodes a non-null JSON array into T, replacing a
// null body with an empty slice so no caller handles nil.
func get[T any](c *apiClient, ctx context.Context, url string) ([]T, error) {
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var out []T
	if err := decodeStrictJSON(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("DAEMON_ERROR: %w", err)
	}
	if out == nil {
		return []T{}, nil
	}
	return out, nil
}

func (c *apiClient) ListRows(ctx context.Context, granularity string, limit int) ([]store.Row, error) {
	if limit <= 0 {
		limit = 200
	}
	url := fmt.Sprintf("%s/v1/rows?g=%s&limit=%d", c.base, granularity, limit)
	return get[[]store.Row](c, ctx, url)
}

func (c *apiClient) ListMessages(ctx context.Context, threadID int64) ([]store.Message, error) {
	url := c.base + "/v1/threads/" + strconv.FormatInt(threadID, 10) + "/messages"
	return get[[]store.Message](c, ctx, url)
}

// SendReply appends to a thread. The TUI never sets parent_id: a reply is a
// message in the thread, not a nested answer.
func (c *apiClient) SendReply(ctx context.Context, threadID int64, text string) error {
	body := map[string]any{"Name": "you", "Role": "user", "Content": text}
	resp, err := c.do(ctx, http.MethodPost,
		c.base+"/v1/threads/"+strconv.FormatInt(threadID, 10)+"/messages", jsonBody(body))
	if err != nil {
		return mutationTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readAPIError(resp)
	}
	var ack struct {
		Seq int64 `json:"seq"`
	}
	if err := decodeStrictJSON(resp.Body, &ack); err != nil {
		return mutationTransportError(err)
	}
	if ack.Seq <= 0 {
		return fmt.Errorf("DELIVERY_UNKNOWN: message acknowledgement without an assigned sequence")
	}
	return nil
}
```

Add `"strconv"` to the imports. `do` already substitutes `context.Background()` for a nil context, which is how the fetch commands pass `nil`.

- [ ] **Step 6: Rewrite `internal/tui/model.go`**

```go
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
	quitting       bool
	granularity    string
	reversed       bool
	detail         bool
	rows           []store.Row
	cursor         int
	scroll         int
	threadID       int64
	thread         []store.Message
	threadScroll   int
	status         string
	api            *apiClient
	compose        composeModel
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
// exactly the rest. Task 8 adds the split pane and the fullscreen thread.
func (m model) bodyView() string {
	return strings.Join([]string{
		m.titleLine(),
		renderRows(m.width, m.height-chromeH, m.rows, m.cursor, m.scroll),
		dimStyle.Render("↑↓ nav · g group · v sort · Enter read · r reply · q quit"),
		statusStyle.Width(m.width - 2).Render(termtext.Truncate(m.statusLine(), m.width-2, "…")),
	}, "\n")
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
	m.scroll = min(max(m.scroll, 0), m.cursor)
	if m.scroll > m.cursor-visible+1 {
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
// loaded. A channel row has no thread, so it fetches nothing.
func (m model) syncThread() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok || row.ThreadID == 0 || row.ThreadID == m.threadID {
		return nil
	}
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
	}
	return m, nil
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
```

`m.tick()` is added in Task 10; until then add a temporary `func (m model) tick() tea.Cmd { return nil }` so the package compiles, and delete it in Task 10.

- [ ] **Step 7: Replace `internal/tui/compose.go` with the stub this task needs**

Task 9 replaces this file for real. Delete `composeModel`, `composeState`, `composeMode`, `filterModel`, and every fetched-message type; leave:

```go
package tui

import tea "github.com/charmbracelet/bubbletea"

// composeModel is the reply box and nothing else. There was one mode per
// create path; the TUI no longer creates anything, so there is one mode.
type composeModel struct {
	active  bool
	width   int
	context string
	text    string
	cursor  int
	err     string
}

func (m *composeModel) resize(w int) {
	if w < 30 {
		m.width = 60
		return
	}
	m.width = w * 80 / 100
}

func (m *composeModel) open(context string) {
	m.active, m.context, m.text, m.cursor, m.err = true, context, "", 0, ""
}

func (m *composeModel) close() {
	m.active, m.context, m.text, m.cursor, m.err = false, "", "", 0, ""
}

func (m composeModel) view() string { return "" }

func (m *composeModel) update(msg tea.KeyMsg) tea.Cmd { return nil }
```

- [ ] **Step 8: Delete the dead code and the old tests**

```bash
git rm -q internal/tui/session.go internal/tui/model_test.go internal/tui/session_test.go \
  internal/tui/overlay_test.go internal/tui/inbox_layout_test.go internal/tui/simple_test.go \
  internal/tui/api_test.go internal/tui/sort_filter_test.go internal/tui/create_test.go \
  internal/tui/thread_table_test.go
```

- [ ] **Step 9: Run the gate**

Run: `go vet ./... && test -z "$(gofmt -l .)" && go test ./...`
Expected: PASS. Nothing may reference a deleted helper.

- [ ] **Step 10: Commit**

```bash
git add internal/tui
git commit -m "refactor(tui): one list of rows, one column width, no sessions

The model drops from forty-two fields to fifteen and from five views to one
list. All three text columns are twelve cells, so the row prefix is a single
constant instead of five widths and a drop ladder that shed NAME, CHANNEL,
THREAD, and TIME until the content column fit.

Rows come from the daemon at one granularity, so there is no representative
selection, no group-then-filter ordering, and no reply-count arithmetic on
the client. Row content is sanitized with termtext.SanitizeLine before it is
measured, which drops the OSC, DCS, APC, and C1 sequences the old CSI-only
regex let through.

Deletes the unreachable channels/threads/messages stack, both inbox layouts,
the preview toggle, the filter, and the whole session pane. The api client's
seven near-identical decoders collapse into one generic get."
```

---

### Task 8: The thread, in two placements

**Files:**
- Create: `internal/tui/thread.go`, `internal/tui/thread_test.go`
- Modify: `internal/tui/model.go` (`handleKey` gains `enter` and `esc`)

**Interfaces:**
- Consumes: `model` from Task 7.
- Produces: `renderThread(w, h int, title string, thread []store.Message) string`, `(*model).openThread() (tea.Model, tea.Cmd)`.

- [ ] **Step 1: Write the failing tests**

`internal/tui/thread_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

func threadFixture() []store.Message {
	return []store.Message{
		{Seq: 1, Name: "alice", AuthorType: "human", Content: "the original post", CreatedAt: "2026-09-23T10:00:00Z"},
		{Seq: 2, Name: "bob", AuthorType: "human", Content: "a reply", CreatedAt: "2026-09-23T10:05:00Z"},
		{Seq: 3, Name: "ci.bot", AuthorType: "agent", Content: "build passed", CreatedAt: "2026-09-23T10:10:00Z"},
	}
}

func TestRenderThreadShowsEveryMessage(t *testing.T) {
	got := plain(renderThread(60, 24, "eng › pr-review", threadFixture()))
	for _, want := range []string{"alice", "the original post", "bob", "a reply", "ci.bot", "build passed"} {
		if !strings.Contains(got, want) {
			t.Errorf("thread view is missing %q:\n%s", want, got)
		}
	}
}

func TestRenderThreadIsExactlyHeight(t *testing.T) {
	for _, h := range []int{1, 2, 5, 24} {
		if n := strings.Count(renderThread(60, h, "eng › pr-review", threadFixture()), "\n") + 1; n != h {
			t.Errorf("height %d produced %d lines", h, n)
		}
	}
}

func TestRenderThreadHeaderNamesTheThread(t *testing.T) {
	got := plain(renderThread(60, 24, "eng › pr-review", threadFixture()))
	if !strings.Contains(got, "eng › pr-review") {
		t.Errorf("header does not name the thread: %q", got)
	}
}

func TestRenderThreadEmptyState(t *testing.T) {
	got := plain(renderThread(60, 24, "", nil))
	if !strings.Contains(got, "no thread") {
		t.Errorf("empty state = %q", got)
	}
}

func TestRenderThreadNeverExceedsWidth(t *testing.T) {
	long := strings.Repeat("word ", 40)
	msgs := []store.Message{{Name: "alice", Content: long, CreatedAt: "2026-09-23T10:00:00Z"}}
	for _, w := range []int{20, 40, 74, 200} {
		for _, line := range strings.Split(renderThread(w, 24, "eng › pr-review", msgs), "\n") {
			if got := termtext.DisplayWidth(line); got > w {
				t.Fatalf("width %d produced a %d-cell line: %q", w, got, line)
			}
		}
	}
}

func TestRenderThreadSanitizesContent(t *testing.T) {
	msgs := []store.Message{{Name: "alice", Content: "safe\x1b]0;pwned\x07 text", CreatedAt: "2026-09-23T10:00:00Z"}}
	got := renderThread(60, 24, "eng › pr-review", msgs)
	if strings.Contains(got, "\x1b") {
		t.Fatal("an escape sequence reached the thread view")
	}
	if !strings.Contains(plain(got), "safe text") {
		t.Fatalf("sanitization must keep the surrounding text: %q", plain(got))
	}
}

func TestRenderThreadShowsTheTailWhenItOverflows(t *testing.T) {
	got := plain(renderThread(60, 6, "eng › pr-review", threadFixture()))
	if !strings.Contains(got, "build passed") {
		t.Errorf("a short pane must show the newest messages: %q", got)
	}
	if strings.Contains(got, "the original post") {
		t.Errorf("a short pane must drop the oldest messages: %q", got)
	}
}

func TestEnterOpensTheThreadAndEscReturns(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5, Channel: "eng", Thread: "pr-review"}}
	m.cursor = 0
	next, cmd := m.handleKey(key("enter"))
	mm := toModel(next)
	if !mm.detail {
		t.Fatal("Enter did not open the thread")
	}
	if mm.threadID != 5 {
		t.Fatalf("threadID = %d, want 5", mm.threadID)
	}
	if cmd == nil {
		t.Fatal("opening a thread must fetch it")
	}
	mm = toModel(mm.handleKey(key("esc")))
	if mm.detail {
		t.Fatal("Esc did not leave the thread")
	}
}

func TestEnterOnAChannelRowHasNoThread(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 0, Channel: "eng"}}
	m.cursor = 0
	mm := toModel(m.handleKey(key("enter")))
	if mm.threadID != 0 {
		t.Fatalf("a channel row must not load a thread, got %d", mm.threadID)
	}
	if mm.detail {
		t.Fatal("a channel row has no thread to open")
	}
	if mm.status == "" {
		t.Fatal("a channel row must say so")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tui/ -run 'TestRenderThread|TestEnter'`
Expected: FAIL — `renderThread` is undefined and `enter` is not handled.

- [ ] **Step 3: Write `internal/tui/thread.go`**

```go
package tui

import (
	"strings"

	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// A message is a clock, an author, and a body on its own lines. The body
// starts one cell past the clock, so the author is free to run further right
// on the header line without ever colliding with the body below it.
const (
	threadIndent  = 2
	threadClockW  = 5
	threadHeadGap = 1
	threadBodyAt  = threadIndent + threadClockW + threadHeadGap
	threadMinBody = 8
)

// renderThread draws a thread at any width: in the pane beside the list, or
// filling the terminal. The original post is not special-cased, so there is
// one uniform block per message and nothing to keep in sync.
func renderThread(w, h int, title string, thread []store.Message) string {
	if w < 1 || h < 1 {
		return ""
	}
	if len(thread) == 0 {
		return padLines(placeholder("no thread — press g"), w, h)
	}
	header := pad(sepStyle.Render(termtext.Truncate(termtext.SanitizeLine(title), w, "…")), w)
	rows := []string{header, pad(sepStyle.Render(strings.Repeat("─", w)), w)}
	if keep := h - 2; keep > 0 {
		body := threadLines(w, thread)
		if len(body) > keep {
			body = body[len(body)-keep:]
		}
		rows = append(rows, body...)
	}
	return padLines(strings.Join(rows, "\n"), w, h)
}

// threadLines is every message as a header line plus its wrapped content.
// Every line is truncated and padded to w, so a long author name or a wide
// grapheme cannot push a line past the pane.
func threadLines(w int, thread []store.Message) []string {
	body := max(w-threadBodyAt, threadMinBody)
	lines := make([]string, 0, len(thread)*3)
	for _, m := range thread {
		header := strings.Repeat(" ", threadIndent) +
			pad(formatClock(m.CreatedAt), threadClockW) +
			strings.Repeat(" ", threadHeadGap) +
			nameStyle(m.AuthorType).Render(termtext.Truncate(termtext.SanitizeLine(m.Name), ColW, ""))
		lines = append(lines, pad(threadHeaderStyle.Render(termtext.Truncate(header, w, "")), w))
		for _, line := range termtext.Wrap(termtext.SanitizeBlock(m.Content), body) {
			lines = append(lines, pad(
				threadBodyStyle.Render(strings.Repeat(" ", threadBodyAt)+termtext.Truncate(line, w-threadBodyAt, "")), w))
		}
	}
	return lines
}

func placeholder(s string) string { return placeholderStyle.Render(s) }

// padLines pads s to exactly h rows of w cells. A thread longer than the
// terminal is already truncated by the caller, so the last h rows win.
func padLines(s string, w, h int) string {
	rows := strings.Split(s, "\n")
	if len(rows) > h {
		rows = rows[len(rows)-h:]
	}
	for len(rows) < h {
		rows = append(rows, strings.Repeat(" ", w))
	}
	for i, r := range rows {
		rows[i] = pad(r, w)
	}
	return strings.Join(rows, "\n")
}
```

- [ ] **Step 4: Wire the split pane and `enter`/`esc` into `model.go`**

Replace `bodyView` with the version that has somewhere to put the thread:

```go
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
		body = strings.Join([]string{
			renderRows(ListW, h, m.rows, m.cursor, m.scroll),
			renderThread(m.width-ListW, h, m.threadTitle(), m.thread),
		}, "\n")
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
// list or fills the terminal.
func (m model) paneWidth() int {
	if m.split() && !m.detail {
		return m.width - ListW
	}
	return m.width
}
```

Add to the `switch msg.String()` in `handleKey`, and add the method:

```go
	case "enter":
		return m.openThread()
	case "esc":
		m.detail = false
		return m, nil
```

```go
// openThread reads the selected row's thread. In a split terminal it fills
// the terminal; in a stacked one it replaces the list. Same rule either way,
// which is what replaces the old detail view and its five state fields.
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
	m.threadScroll = 0
	return m, m.syncThread()
}
```

- [ ] **Step 5: Run the gate**

Run: `go test ./internal/tui/ && test -z "$(gofmt -l internal/tui)"`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui
git commit -m "feat(tui): one thread renderer in two placements

renderThread draws at any width, so the pane beside the list and the
fullscreen view are the same function, and Enter and Esc mean the same thing
in both geometries. That is what replaces the old detail view with its own
cursor, scroll offset, saved cursor, and three clamp functions.

Every message is one uniform block, so the original post is no longer
special-cased and threadSplit and buildThreadOPlines are gone. A short pane
shows the newest messages and drops the oldest."
```

---

### Task 9: Reply

**Files:**
- Rewrite: `internal/tui/compose.go`
- Create: `internal/tui/compose_test.go`
- Modify: `internal/tui/model.go` (`handleKey` gains `r`)

**Interfaces:**
- Consumes: `model` from Task 7, `handleComposeSend` from Task 7.
- Produces: `(*composeModel).open(context string)`, `(*composeModel).close()`, `(*composeModel).resize(w int)`, `(*composeModel).update(tea.KeyMsg) tea.Cmd`, `composeModel.view() string`, `(*model).openReply() (tea.Model, tea.Cmd)`.

- [ ] **Step 1: Write the failing tests**

`internal/tui/compose_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

func TestComposeTypesAndSends(t *testing.T) {
	var c composeModel
	c.resize(100)
	c.open("reply in eng › pr-review")
	for _, r := range "hi" {
		c.update(key(string(r)))
	}
	if c.text != "hi" {
		t.Fatalf("text = %q, want %q", c.text, "hi")
	}
	cmd := c.update(key("enter"))
	if cmd == nil {
		t.Fatal("Enter must produce a send command")
	}
	msg, ok := cmd().(composeSendMsg)
	if !ok {
		t.Fatal("the command must produce a composeSendMsg")
	}
	if msg.text != "hi" {
		t.Fatalf("sent text = %q", msg.text)
	}
}

func TestComposeEmptySendDoesNothing(t *testing.T) {
	var c composeModel
	c.open("reply")
	if cmd := c.update(key("enter")); cmd != nil {
		t.Fatal("an empty compose must not send")
	}
	if c.err == "" {
		t.Fatal("an empty compose must say cannot be empty")
	}
}

func TestComposeWhitespaceOnlySendDoesNothing(t *testing.T) {
	var c composeModel
	c.open("reply")
	for _, r := range "  " {
		c.update(key(string(r)))
	}
	if cmd := c.update(key("enter")); cmd != nil {
		t.Fatal("a whitespace-only compose must not send")
	}
}

func TestComposeEscCancels(t *testing.T) {
	var c composeModel
	c.open("reply")
	c.update(key("x"))
	c.update(key("esc"))
	if c.active || c.text != "" || c.context != "" {
		t.Fatalf("Esc left the compose active: %+v", c)
	}
}

func TestComposeBackspaceEdits(t *testing.T) {
	var c composeModel
	c.open("reply")
	for _, r := range "abc" {
		c.update(key(string(r)))
	}
	c.update(key("backspace"))
	if c.text != "ab" {
		t.Fatalf("text = %q, want %q", c.text, "ab")
	}
}

func TestComposeViewNamesTheThread(t *testing.T) {
	var c composeModel
	c.resize(100)
	c.open("reply in eng › pr-review")
	got := plain(c.view())
	if !strings.Contains(got, "pr-review") {
		t.Errorf("the compose view does not name the thread: %q", got)
	}
	if termtext.DisplayWidth(got) > 100 {
		t.Errorf("the compose view is %d cells wide", termtext.DisplayWidth(got))
	}
}

func TestOpenReplyOnAThreadRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 5, Channel: "eng", Thread: "pr-review"}}
	m.cursor = 0
	mm := toModel(m.openReply())
	if !mm.compose.active {
		t.Fatal("r did not open compose")
	}
	if !strings.Contains(mm.compose.context, "pr-review") {
		t.Fatalf("compose context = %q", mm.compose.context)
	}
	if !mm.detail {
		t.Fatal("replying must show the thread being replied to")
	}
}

func TestOpenReplyOnAChannelRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.rows = []store.Row{{ID: 1, ThreadID: 0, Channel: "eng"}}
	mm := toModel(m.openReply())
	if mm.compose.active {
		t.Fatal("a channel row has no thread to reply to")
	}
	if mm.status == "" {
		t.Fatal("a channel row must say so")
	}
}

func TestReplySendsToTheSelectedThread(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.threadID = 9
	m.compose.open("reply in eng › pr-review")
	mm := toModel(m.handleComposeSend(composeSendMsg{text: "hi"}))
	if mm.compose.active {
		t.Fatal("compose must close on send")
	}
}
```

Add these imports to `internal/tui/compose_test.go`:

```go
	"github.com/NoRaincheck/fluffle/internal/store"
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tui/ -run 'TestCompose|TestOpenReply'`
Expected: FAIL — the stub `open` does not exist and `openReply` is undefined.

- [ ] **Step 3: Rewrite `internal/tui/compose.go`**

```go
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NoRaincheck/fluffle/internal/tui/termtext"
)

// composeModel is the reply box and nothing else. There was one mode per
// create path; the TUI no longer creates anything, so there is one mode.
type composeModel struct {
	active  bool
	width   int
	context string
	text    string
	cursor  int
	err     string
}

func (m *composeModel) resize(w int) {
	if w < 30 {
		m.width = 60
		return
	}
	m.width = w * 80 / 100
}

func (m *composeModel) open(context string) {
	m.active, m.context, m.text, m.cursor, m.err = true, context, "", 0, ""
}

func (m *composeModel) close() {
	m.active, m.context, m.text, m.cursor, m.err = false, "", "", 0, ""
}

func (m *composeModel) update(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		m.close()
		return nil
	case tea.KeyEnter:
		if strings.TrimSpace(m.text) == "" {
			m.err = "cannot be empty"
			return nil
		}
		m.err = ""
		text := m.text
		return func() tea.Msg { return composeSendMsg{text: text} }
	case tea.KeyBackspace:
		if m.cursor > 0 {
			m.text = m.text[:m.cursor-1] + m.text[m.cursor:]
			m.cursor--
		}
	case tea.KeyLeft:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyRight:
		if m.cursor < len(m.text) {
			m.cursor++
		}
	case tea.KeyRunes, tea.KeySpace:
		runes := msg.Runes
		if len(runes) == 0 && msg.Type == tea.KeySpace {
			runes = []rune{' '}
		}
		for _, r := range runes {
			m.text = m.text[:m.cursor] + string(r) + m.text[m.cursor:]
			m.cursor++
		}
	}
	return nil
}

func (m composeModel) view() string {
	cursor := "▏ "
	if m.cursor < len(m.text) {
		cursor = "▏"
	}
	lines := []string{
		modalTitleStyle.Render(termtext.Truncate(m.context, m.width-6, "")),
		pad(termtext.Truncate(m.text+cursor, m.width-6, ""), m.width-6),
		m.errLine(),
		modalHintStyle.Render("Enter to send · Esc to cancel"),
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(modalBorder).
		Foreground(modalFg).
		Padding(0, 2).
		Width(m.width - 2).
		Render(strings.Join(lines, "\n"))
}

func (m composeModel) errLine() string {
	if m.err == "" {
		return ""
	}
	return modalErrorStyle.Render(m.err)
}
```

- [ ] **Step 4: Wire `r` into `handleKey`**

Add to the `switch msg.String()` in `handleKey`, and add the method:

```go
	case "r":
		return m.openReply()
```

```go
// openReply starts a reply to the selected row's thread. A channel row has no
// thread, so there is nothing to reply to.
func (m *model) openReply() (tea.Model, tea.Cmd) {
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
	m.threadScroll = 0
	m.compose.open("reply in " + row.Channel + " › " + row.Thread)
	return m, m.syncThread()
}
```

- [ ] **Step 5: Run the gate**

Run: `go test ./internal/tui/ && go vet ./... && test -z "$(gofmt -l .)" && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui
git commit -m "feat(tui): reply is the only compose mode

One mode, so composeMode, composeState, parentID, and the create flows go
with it. A reply appends to the thread and never sets parent_id, which is
what the old handleThreadReply already did in practice. A channel row has no
thread, so r answers no thread on this row rather than opening a box that
could not be sent. Replying shows the thread, so you can see what you are
answering."
```

---

### Task 10: `g`, `v`, and the tick

**Files:**
- Modify: `internal/tui/model.go`
- Create: `internal/tui/granularity_test.go`

**Interfaces:**
- Consumes: `model` from Task 7, `handleKey` from Tasks 8 and 9.
- Produces: `tickInterval`, `tickMsg`, `(*model).tick() tea.Cmd`, `(*model).refresh() tea.Cmd`, `(*model).nextGranularity() string`.

- [ ] **Step 1: Write the failing tests**

`internal/tui/granularity_test.go`:

```go
package tui

import (
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func TestGCyclesGranularity(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	m.threadID = 3
	want := []string{store.GranularityThread, store.GranularityChannel, store.GranularityMessage}
	for _, w := range want {
		next, cmd := m.handleKey(key("g"))
		mm := toModel(next)
		if mm.granularity != w {
			t.Fatalf("granularity = %q, want %q", mm.granularity, w)
		}
		if cmd == nil {
			t.Fatal("changing granularity must refetch")
		}
		if mm.threadID != 0 {
			t.Fatalf("changing granularity must drop the loaded thread, got %d", mm.threadID)
		}
	}
}

func TestVReversesSort(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.width, m.height = 120, 40
	mm := toModel(m.handleKey(key("v")))
	if !mm.reversed {
		t.Fatal("v did not reverse")
	}
	mm = toModel(mm.handleKey(key("v")))
	if mm.reversed {
		t.Fatal("v did not toggle back")
	}
}

func TestRefreshAlwaysRefetchesRows(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityThread
	m.rows = []store.Row{{ID: 1, ThreadID: 4}}
	m.threadID = 4
	if m.refresh() == nil {
		t.Fatal("a tick must refetch the rows")
	}
}

func TestSyncThreadSkipsAChannelRow(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityChannel
	m.rows = []store.Row{{ID: 1, ThreadID: 0}}
	m.cursor = 0
	if m.syncThread() != nil {
		t.Fatal("a channel row must not fetch a thread")
	}
}

func TestStaleRowsResponseIsDropped(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityThread
	m.rows = []store.Row{{ID: 42}}
	mm := toModel(m.Update(rowsFetchedMsg{
		granularity: store.GranularityMessage,
		rows:        []store.Row{{ID: 1}},
	}))
	if len(mm.rows) != 1 || mm.rows[0].ID != 42 {
		t.Fatal("a response for another granularity must be dropped")
	}
}

func TestStaleThreadResponseIsDropped(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.threadID = 5
	m.thread = []store.Message{{Seq: 1}}
	mm := toModel(m.Update(threadFetchedMsg{threadID: 6, messages: []store.Message{{Seq: 9}}}))
	if mm.thread[0].Seq != 1 {
		t.Fatal("a response for another thread must be dropped")
	}
}

func TestTickIsArmedByTheFirstRowsResponse(t *testing.T) {
	m := toModel(New("http://127.0.0.1:1"))
	m.granularity = store.GranularityMessage
	_, cmd := m.Update(rowsFetchedMsg{granularity: store.GranularityMessage})
	if cmd == nil {
		t.Fatal("the first rows response must arm a tick")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tui/ -run 'TestGCycles|TestVReverses|TestRefresh|TestSyncThread|TestStale|TestTickIsArmed'`
Expected: FAIL — `g` and `v` are unhandled and `refresh` is undefined.

- [ ] **Step 3: Implement**

Add to `internal/tui/model.go`:

```go
// tickInterval is how often the TUI re-reads its rows and the selected
// thread. One unconditional tick replaces a six-field poll whose release
// rules were a documented dead end: a session started in the thread you were
// already looking at was not discovered until you moved away and back.
const tickInterval = 2 * time.Second

type tickMsg struct{}

// tick arms the next refresh. It is scheduled from a response, never from a
// timer loop, so there is at most one tick in flight.
func (m model) tick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// refresh re-reads the rows and the selected thread. New messages append, so
// row and line indices are stable and no scroll is reset.
func (m model) refresh() tea.Cmd {
	return tea.Batch(m.fetchRows(), m.syncThread(), m.tick())
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
```

Add `case tickMsg:` to `Update`, immediately before `case tea.KeyMsg:`:

```go
	case tickMsg:
		return m, m.refresh()
```

Add to `handleKey`:

```go
	case "g":
		m.granularity = m.nextGranularity()
		m.cursor, m.scroll = 0, 0
		m.threadID = 0
		m.thread = nil
		return m, m.fetchRows()
	case "v":
		m.reversed = !m.reversed
		m.applyRows(m.rows)
		return m, nil
```

Add `"time"` to `model.go`'s imports.

- [ ] **Step 4: Run the gate**

Run: `go vet ./... && test -z "$(gofmt -l .)" && go test ./... && go tool sqlc diff`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui
git commit -m "feat(tui): one unconditional tick in place of the session poll

Every two seconds the TUI re-reads its rows and the selected thread, so an
agent's reply appears without a keypress. That is what the session pane was
buying, for two fields and no invariants: the old poll covered a live run's
lifetime rather than the thread, and a session started in the thread you were
already looking at was not discovered until you moved away and back.

A response is dropped when the field that selects it no longer matches, which
is the entire stale-response story. The tick is armed from a response, so at
most one is ever in flight. g and v complete the keymap."
```

---

### Task 11: Rewrite the docs

**Files:**
- Rewrite: `docs/tui-architecture.md`, `docs/tui-keybindings.md`, `docs/tui-extending.md`
- Modify: `docs/agent-sessions.md`, `README.md`, `VISION.md`

**Interfaces:**
- Consumes: the finished behaviour of Tasks 1 through 10.
- Produces: no code. Every documented key, route, and invariant must exist.

- [ ] **Step 1: Rewrite `docs/tui-architecture.md`**

Document: the one-model shape; the fifteen fields; the seven keys; the three granularities and what each row is; the `rowPrefixW = 61` geometry and why it is one constant; `split` versus the stacked fallback and the 71x24 floor; the one `renderThread` in two placements; the tick and the two stale-response rules; the vendored `screen` and `termtext` and their NOTICE. Delete every section about layouts, previews, sessions, polling, filters, and the channels/threads/messages stack. Keep the existing voice: dense, declarative, stating why a decision was made rather than only what it is.

- [ ] **Step 2: Rewrite `docs/tui-keybindings.md`**

One table of the seven keys plus `ctrl+c`, a table of the three granularities, the geometry rules, and the compose modal. Nothing else.

- [ ] **Step 3: Rewrite `docs/tui-extending.md`**

How to add a renderer or a granularity: the two render functions, the one `Row` type, and the rule that untrusted text goes through `termtext` before it is measured. Delete the sections on the layout toggles, the session pane, and the filter.

- [ ] **Step 4: Update `docs/agent-sessions.md`**

Delete the sections on the preview pane and the 500 ms poll. Keep the daemon-side contract: a leading `@mention` on a human message starts one session, the reply lands as an ordinary agent-authored message, and `flf agent session --id N` reads a transcript. Add one line: the TUI shows the agent's reply in the thread and does not show the run.

Update the mention-charset paragraph to the new rule: the charset is `[A-Za-z][A-Za-z.]*` with trailing dots trimmed, and a token a looser charset would have continued is not a name at all. Update the `agentcfg` charset sentence to `^[A-Za-z][A-Za-z.]{0,11}$`.

- [ ] **Step 5: Update `README.md` and `VISION.md`**

Re-slug every example: `auth-refactor` stays, `Schema migration` becomes `schema-migration`, `pr-review` stays, `ci-bot` becomes `ci.bot`. In `VISION.md`, add a fifth entry to Decisions Locked In covering the two name rules and the 12-byte bound, and replace the described TUI surface with the new keymap.

- [ ] **Step 6: Verify no doc names a deleted thing**

Run: `rg -n 'layout:compact|layout:full|preview pane|session pane|/v1/inbox|InboxMessage|filter modal|viewChannels' README.md VISION.md docs/ -g '!superpowers'`
Expected: no matches. Any match is a doc still describing deleted behavior.

- [ ] **Step 7: Commit**

```bash
git add README.md VISION.md docs/
git commit -m "docs: describe the rows TUI

The TUI docs are rewritten rather than edited, because most of what they
documented no longer exists: two layouts, a preview pane with a mutual
exclusion invariant, a session pane, a filter, and a create flow. The
agent-sessions doc keeps the daemon contract and drops the preview pane and
its polling notes. Examples are re-slugged throughout."
```

---

### Task 12: Verify the reduction and the gate

**Files:** none. This task produces the measurement the design promised.

- [ ] **Step 1: Measure**

```bash
echo "tui non-test (excludes vendored):"
find internal/tui -maxdepth 1 -name '*.go' -not -name '*_test.go' -exec wc -l {} + | tail -1
echo "tui test:"
find internal/tui -maxdepth 1 -name '*_test.go' -exec wc -l {} + | tail -1
echo "vendored:"
find internal/tui/screen internal/tui/termtext -name '*.go' -exec wc -l {} + | tail -1
echo "repo total (was 26081):"
find . -name '*.go' -not -path './.git/*' -exec wc -l {} + | tail -1
```

Expected: tui non-test well under 1200, tui test well under 800, vendored around 250, repo total below 26081.

- [ ] **Step 2: Run the full gate**

Run: `go vet ./... && test -z "$(gofmt -l .)" && go test ./... && go tool sqlc diff`
Expected: all clean.

- [ ] **Step 3: Smoke the real binary**

```bash
rm -rf ~/.fluffle
go build -o /tmp/flf ./cmd/flf
/tmp/flf init --orphaned
/tmp/flf channel create --name eng --orphaned
/tmp/flf thread new --channel eng --orphaned --title pr-review
/tmp/flf message send --thread 1 --text "looks great"
/tmp/flf message send --thread 1 --text "ship it"
/tmp/flf inbox
/tmp/flf thread new --channel eng --orphaned --title "has a space"
/tmp/flf daemon stop
```

Expected: `inbox` prints two rows naming `eng`, `pr-review`, and `you`; the spaced title is rejected with an `invalid` error. Then check by hand at 200x50 and at 80x24 that the list renders, `g` changes the group, `Enter` reads the thread, `r` replies, and a new message appears with no keypress.

- [ ] **Step 4: Push and report**

```bash
git push
gh pr comment 33 --body "$(cat <<'EOF'
## Result

- `internal/tui` non-test: 4153 -> MEASURED
- `internal/tui` test: 3637 -> MEASURED
- vendored `screen` + `termtext`: MEASURED
- repo total: 26081 -> MEASURED

`GET /v1/rows?g=message|thread|channel` replaces `GET /v1/inbox`. `flf inbox
--json` prints rows and no longer carries `seq`. Channel and thread names and
author names are now bounded 12-byte slugs, enforced in Go with no schema
change.
EOF
)"
```

Replace each `MEASURED` with the number from Step 1 before running.
