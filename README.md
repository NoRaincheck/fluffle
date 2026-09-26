# fluffle (`flf`)

A local-first, TUI-first communication hub for developer teams and local AI agents.

**Glossary:** Channel = repo-anchored or `--orphaned` room · Thread = titled conversation in a channel · Message = chat line in a thread (reply via `--reply-to` or `--reply-to-seq`) · Reaction = emoji attached to a message · Inbox = unified view of recent messages across all channels · **Agent session** = one local agent run started by an `@mention`, with its prompt, output, and result. Two unrelated things were once both called a *session*, so: an **agent session** is a subprocess run recorded in the `agent_sessions` table and is never part of an export, whereas the portable thing an export carries is the **thread** itself. Only the `session.jsonl` filename in the examples below still uses the older name.

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
./flf agent append --thread 1 --file response.jsonl --agent-id my-agent

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

# inbox — unified view of recent messages across channels
./flf inbox --limit 50 --json

# init — prepare a repo for fluffle
./flf init --repo .

# TUI — minimal 3-view stack: Channels → Threads → Messages
./flf tui
#  Channels: ↑↓/j/k nav · Enter open · n new thread · q quit
#  Threads:  ↑↓ nav · Enter open · n new thread · Esc back
#  Messages: ↑↓ nav · c post · r reply · Esc back

./flf daemon stop
```

Docs: `VISION.md` for scope and the `kata`/`roborev` division of labor · `docs/backend.md` for the daemon, API, and CLI reference · `docs/agent-sessions.md` for why local agent runs are shaped the way they are, and what is out of scope · `docs/tui-architecture.md` and `docs/tui-keybindings.md` for the TUI.

Manual QA: `scripts/seed-tui.sh` seeds orphan and repo-anchored channels, threads, replies, and reactions across ~14 days of timestamps, then `./flf tui`. It needs `FLUFFLE_HOME` and `HOME_TMP` set to a scratch dir, with the daemon already running.
