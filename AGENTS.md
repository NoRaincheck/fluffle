# AGENTS.md

Rules and conventions for AI agents working on this project.

## Project Overview

Fluffle (`flf`) is a local-first, TUI-first communication platform for developer teams and local AI agents. It is a lightweight, Go-based system backed by SQLite, with a local daemon serving HTTP + WebSocket to TUI and CLI clients. See `README.md` for the full surface and `VISION.md` for the project manifesto.

## Architecture

- **Go 1.26**, pure-Go SQLite (`modernc.org/sqlite`), stdlib HTTP
- **Three layers:** `internal/store` (SQLite), `internal/apiserver` (HTTP daemon), `cmd/flf` (CLI)
- **Supporting packages:** `internal/jsonl` (ACP-compatible line codec), `internal/repo` (Git directory resolution), `internal/client` (daemon auto-spawn + health probe)
- **No TUI, no webapp, no FTS, no embeddings, no federation, no auth tokens** — these are out of scope for the current MVP

## Code Conventions

- **No comments** unless explicitly requested. Code should be self-documenting.
- **Go formatting:** `gofmt -l` must be empty for all changed files before committing.
- **TDD:** failing test (RED) → implementation (GREEN) → commit. Never commit without tests passing.
- **Error handling:** use typed errors (`ErrNotFound`, `ErrConflict`) in the store layer; translate to HTTP status codes + `{"code","message"}` envelopes in the API layer; translate to exit codes 0/1/2 in the CLI.
- **Exit codes:** 0 = success, 1 = client error, 2 = daemon error. Never deviate.
- **Agent identity:** `X-Fluffle-Agent` header identifies agents. The daemon re-derives `author_type` from this header for anti-spoofing. Never trust `author_type` from client request bodies.
- **Append-only for agents:** agents may only `append` messages or `add` reactions. They may never create channels, threads, or delete data.
- **No hidden agent memory:** the thread history (JSONL) is the only context. Do not add vector stores, embedding databases, or opaque long-term memory.

## Testing

- **Unit tests:** one `*_test.go` per package, in the same directory. Use `t.TempDir()` for temporary files.
- **Integration tests:** live daemon smoke tests in `cmd/flf/main_test.go` where the CLI needs real HTTP transport.
- **Run:** `go test -count=1 ./...` before every commit. `go vet ./...` must be clean.
- **Coverage:** aim for real behavior verification, not mock coverage. Edge cases matter more than line count.

## Git Workflow

- **Branches:** feature branches off `main`, named `docs/<feature>` for design/spec work, `feat/<feature>` for implementation.
- **Commit messages:** conventional style — `feat(store): ...`, `fix(api): ...`, `refactor: ...`, `docs: ...`.
- **SDD (Subagent-Driven Development):** use the SDD skill for multi-task work. Each task is reviewed before the next begins. Fix rounds are scoped and bounded (one fix wave per task).
- **No merge conflicts with own PR:** push to your own feature branch; the PR is the integration point. Do not merge your own PR without human sign-off.

## Domain Rules (Immutable)

1. **Repo-anchored by default:** every channel/thread must tie to a local Git repo path. Orphaned channels are the exception, not the norm.
2. **SQLite-first:** single database file, no connection pooling beyond `SetMaxOpenConns(1)`. WAL mode is not yet implemented.
3. **Localhost-only:** the daemon binds `127.0.0.1:0`. No remote access, no tokens, no authentication beyond localhost binding.
4. **JSONL is the portable format:** agent threads are structured JSONL with keys `seq`, `role`, `author`, `author_type`, `content`, `timestamp`, `metadata`. Export/import preserves these fields (except `seq` and `metadata` — `seq` is daemon-assigned, `metadata` has no DB column in the MVP).
5. **YAGNI:** do not add features that are not explicitly in the plan or spec. If it is not in the design doc, it does not exist.

## Common Pitfalls

- **`:memory:` SQLite:** always use `SetMaxOpenConns(1)` with in-memory databases, or use a file path. Multiple connections to `:memory:` create separate databases.
- **`os.FindProcess` on Unix:** always succeeds; does not validate PID existence. Do not use it to check if a process is alive.
- **`net.Listen("tcp", ...)` always returns `*TCPAddr`:** safe to type-assert, but document why.
- **HTTP `Content-Type`:** set `application/json` on all responses, including success. Do not rely on case-insensitive JSON matching across packages.
- **Import/append must parse the whole file before the first POST:** no partial writes on parse failure.

## Review Expectations

- Every PR gets two reviews: spec compliance (does it match the plan?) and task quality (tx correctness, no overbuilding, tests verify real behavior).
- Review findings are triaged: Critical (must fix), Important (should fix), Minor (nice to have). Only Critical and Important block merge.
- Push back when a finding is technically incorrect, breaks existing functionality, or violates YAGNI. Cite code, not opinion.
