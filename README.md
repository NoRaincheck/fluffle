# fluffle (`flf`)

A local-first, TUI-first communication hub for developer teams and local AI agents.

**Glossary:** Channel = repo-anchored or `--orphaned` room · Thread = titled conversation in a channel · Message = chat line in a thread (reply via `--reply-to` or `--reply-to-seq`) · Reaction = emoji attached to a message · Row = one line of the TUI's cross-channel feed, where one row is a message, a thread, or a channel depending on the granularity the daemon answers · **Agent session** = one local agent run started by an `@mention`, with its prompt, output, and result. Two unrelated things were once both called a *session*, so: an **agent session** is a subprocess run recorded in the `agent_sessions` table and is never part of an export, whereas the portable thing an export carries is the **thread** itself. Only the `session.jsonl` filename in the examples below still uses the older name.

```bash
go build ./cmd/flf
./flf daemon start --background

# orphan channel (no git needed) + thread + messages — all --json testable
./flf channel create --orphaned --name general --json
./flf thread new --channel general --orphaned --title "hello" --json   # → {"id":1}
./flf message send --thread 1 --text "first!" --as alice --json       # → {"seq":1}
./flf message send --thread 1 --text "reply" --reply-to 1 --as bob --json
./flf message send --thread 1 --text "seq reply" --reply-to-seq 1 --as carol --json
./flf react add --thread 1 --message-seq 1 --emoji "👀" --as alice

# agent read — portable JSONL with thread-local sequences + reactions
./flf agent read --thread 1 --last 5 --json   # last 5 messages as JSONL
./flf agent read --thread 1 --after-seq 3     # cursor-based reads
./flf agent append --thread 1 --file response.jsonl --agent-id my.agent

# repo-anchored channel/thread
./flf channel create --name refactor --repo . --json
./flf thread new --channel refactor --repo . --title "review" --json
./flf thread list --channel refactor --repo . --json

# agent sessions — a human @mention starts one; config in .flf.toml or ~/.fluffle/config.toml
# (agent configs are re-read on mtime change, so no daemon restart is needed)
printf '[[agents]]\nname="reviewer"\ncommand="claude"\nargs=["-p","{prompt}"]\n' > ~/.fluffle/config.toml
./flf agent list --json
./flf message send --thread 1 --text "@reviewer what changed in the auth refactor?" --as alice
./flf agent session --id 1 --json      # the full run: prompt, stdout, exit

# thread export / import — portable JSONL handoff (a "session" here is the exported thread, not an agent run)
./flf thread export --thread 1 --format jsonl > session.jsonl
./flf thread import --file session.jsonl --channel refactor

# inbox — the cross-channel feed; one row is a message, a thread, or a channel
# depending on the granularity the daemon answers (this asks for messages)
./flf inbox --limit 50 --json

# init — prepare a repo for fluffle
./flf init --repo .

# TUI — a feed of rows beside the thread under the cursor; g cycles what one row
# is (message → thread → channel), and the list takes the whole terminal below
# 110 columns, where Enter still reads the thread
./flf tui
#  List:    ↑↓/j/k nav · g group · v sort · Enter read · Esc back · r reply · q quit
#  Compose: type · Enter send · Esc cancel
#  Needs 71x24. Reply appends to the thread; creating, reacting, and reading an
#  agent run are CLI commands.

./flf daemon stop
```

Docs: `VISION.md` for scope and the `kata`/`roborev` division of labor · `docs/backend.md` for the daemon, API, and CLI reference · `docs/agent-sessions.md` for why local agent runs are shaped the way they are, and what is out of scope · `docs/tui-architecture.md`, `docs/tui-keybindings.md`, and `docs/tui-extending.md` for the TUI.

## Development

Tasks run through [`justfile`](justfile); there is no `Makefile`. Install it once with `brew install just`. Bare `just` runs the gate — `go vet`, then `gofmt -l`, then `go test ./...` — and `just --list` shows the recipes.

| Command | When |
|---------|------|
| `just test` | Before committing. |
| `just generate` | After **any** change under `internal/store/queries/` or `internal/store/migrations/`. |
| `just diff` | Before committing. Must be clean, and `just test` does not run it for you. |
| `just vet` | After changing a query file. |

The SQL layer is owned by [sqlc](https://sqlc.dev), pinned as a Go tool in `go.mod`, so there is nothing to install and no version drift. The schema lives in `internal/store/migrations/` as versioned, goose-formatted files, every statement lives in `internal/store/queries/` as an annotated `.sql` file, and `internal/db/` is **generated and committed** — never hand-edited. To change the schema:

1. Add `internal/store/migrations/00002_add_message_edited_at.sql` with the `-- +goose Up` statements and a `-- +goose Down` that undoes them. The numeric prefix is zero-padded — `00002_`, `00010_`, never `10_` — because sqlc reads the directory in lexicographic order, so an unpadded `10_` would sort before `9_`.
2. `just generate` — until the generator runs, nothing sees the new column.
3. `just test`, then commit the migration and the regenerated `internal/db/` **in the same commit**.

A query change is the same three beats against the matching file in `internal/store/queries/`, plus `just vet`; the generated methods are listed in `internal/db/querier.go`. Never edit a migration that has already been applied — write a new numbered one, or the schema on disk silently diverges from the migration set. The full rules, including why the `no-pragma` vet rule can never actually fire, are in [docs/backend.md](docs/backend.md#sql-layer).

Neither `just test` nor `just diff` starts the daemon, so before calling a change done, build the binary and smoke-test it against a scratch database file — [Verifying a change](docs/backend.md#verifying-a-change) has the recipe and the JSON spot-checks, which matter because a wire-contract regression is invisible to the unit suite.

Manual QA: `./scripts/seed-tui.sh` builds a scratch install under `.tui-seed/`, seeds orphan and repo-anchored channels, threads, replies, reactions, and agent sessions across a 14-day timestamp spread, fires one live `@mention`, verifies the result, and execs the TUI. No environment setup, no daemon to start by hand. `--no-tui` stops after seeding; `SEED_AGENT_SLEEP` tunes how long the live agent runs.

Thread data lives in `scripts/fixtures/*.jsonl`, where `@T-14d` style tokens are resolved against the clock at seed time. Agent sessions are staged by `scripts/seed-sessions.go` through `internal/store`, which needs the daemon stopped — the store has no WAL and no `busy_timeout`. `queued` and `running` sessions are deliberately never staged: the daemon reconciles both to `canceled` on startup, so only the live `@mention` can show those states.
