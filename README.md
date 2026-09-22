# fluffle (`flf`)

A local-first, TUI-first communication hub for developer teams and local AI agents, strictly anchored to Git repositories.

Fluffle provides clean, terminal-native chat for human-to-human and human-to-agent collaboration. Rather than replacing issue trackers or code review tools, it acts as lightweight, repo-anchored communication glue that hooks into and complements existing tools like `kata` and `roborev`.

## Core Tenets

- **TUI-first, webapp-second** — the primary interface is a terminal UI; the webapp is a secondary viewer.
- **Repo-anchored by default** — every channel and thread ties to a local Git repository path.
- **Strict agent boundaries** — agents are append-only (messages + reactions); only humans create channels/threads or delete data.
- **Portable agent state** — threads are structured, append-only JSONL streams that are trivial to export, share, and resume.
- **No hidden agent memory** — the thread history is the context. What is in the JSONL is what the agent receives.
- **SQLite-first, local daemon** — a single SQLite database backed by a local client-server daemon.

## Architecture

```
flf CLI ──┬── TUI (primary, keyboard-driven)
          ├── daemon (local HTTP + SQLite server)
          │     ├── /v1/health
          │     ├── /v1/channels
          │     ├── /v1/channels/{id}/threads
          │     ├── /v1/threads/{id}/messages
          │     └── /v1/threads/{id}/messages/{seq}/reactions
          └── agent CLI (flf agent read/append, flf thread export/import)
```

- **Language:** Go 1.26
- **Data layer:** SQLite (local file, `modernc.org/sqlite` pure-Go)
- **Primary client:** TUI (Terminal User Interface)
- **Secondary client:** Lightweight webapp (HTTP + WebSocket)
- **Agent interface:** `flf` CLI — external agent frameworks read thread context and append responses via JSONL

## Quick Start

```bash
# Build
go build ./cmd/flf

# Start the local daemon
./flf daemon start --background

# Create a repo-anchored channel
./flf channel create "auth-refactor" --repo ./my-project

# List threads in a channel
./flf thread list --channel "auth-refactor" --repo ./my-project

# Send a message
./flf message send --thread 1 --text "Reviewing the migration script now."

# Agent reads thread context and appends a response
./flf agent read --thread 1 --last 5 --agent-id my-agent
# ... process output, write response.jsonl ...
./flf agent append --thread 1 --file response.jsonl --agent-id my-agent

# Stop the daemon
./flf daemon stop
```

## CLI Reference

### Daemon & Initialization

| Command | Description |
|---|---|
| `flf daemon start [--background]` | Start the local SQLite-backed server |
| `flf daemon stop` | Stop the running daemon |
| `flf daemon status` | Show daemon status |
| `flf init [--repo DIR]` | Initialize fluffle context in a Git repository |

### Channel & Thread Management

| Command | Description |
|---|---|
| `flf channel list [--repo PATH] [--orphaned]` | List channels, optionally filtered by repo |
| `flf channel create NAME [--repo PATH \| --orphaned]` | Create a channel |
| `flf thread list --channel NAME --repo PATH` | List threads in a channel |
| `flf thread new --channel NAME --repo PATH --title T` | Create a new thread |

### Messaging & Reactions

| Command | Description |
|---|---|
| `flf message send --thread ID --text T [--as NAME] [--agent-id ID]` | Send a message |
| `flf react add --message SEQ --emoji E [--agent-id ID]` | Add an emoji reaction |

### Agent Interaction

| Command | Description |
|---|---|
| `flf agent read --thread ID [--last N] [--agent-id ID]` | Output thread messages as JSONL to stdout |
| `flf agent append --thread ID --file F [--agent-id ID]` | Append lines from a JSONL file |

### Session Handoff

| Command | Description |
|---|---|
| `flf thread export --thread ID --format jsonl` | Export a thread as JSONL |
| `flf thread import --file F --channel NAME (--repo PATH \| --orphaned)` | Import a JSONL file as a new thread |

## Error Codes

| Code | HTTP | Exit | When |
|---|---|---|---|
| `DAEMON_DOWN` | 500/transport | 2 | Daemon unreachable or internal error |
| `NOT_A_GIT_REPO` | — | 1 | Path is not a Git repository |
| `AGENT_FORBIDDEN` | 403 | 1 | Agent attempted to create a channel or thread |
| `BAD_JSONL` | 400/409 | 1 | Parse failure, or channel/thread/reaction conflict |
| `THREAD_NOT_FOUND` | 404 | 1 | Thread does not exist |
| `CHANNEL_NOT_FOUND` | 404 | 1 | Channel does not exist |
| `FILE_READ` | — | 1 | Local file unreadable (agent append/import) |

## Project State

This project is in early MVP. The core backend (store, daemon, CLI, agent handoff) is implemented and tested. Remaining work:

- TUI client (primary interface)
- Webapp client (secondary viewer)
- Metadata column migration (currently accepted but dropped on import)
- Full permission-matrix test coverage

## License

MIT
