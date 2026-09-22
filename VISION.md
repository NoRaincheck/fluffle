
# 📄 Project Manifesto: Fluffle (`flf`)

**Tagline:** A local-first, TUI-first communication hub for developers and local AI agents, strictly anchored to Git repositories.

## 🎯 Core Vision
Fluffle is a lightweight, Golang-based communication platform designed for developer teams working alongside local AI agents. It provides a clean, terminal-native interface for human-to-human and human-to-agent collaboration. Rather than reinventing issue tracking or code review, Fluffle acts as the lightweight, repo-anchored communication glue that hooks into and complements existing tools like `kata` and `roborev`.

---

## 🏛️ Core Tenets (The Immutable Rules)

1. **TUI-First, Webapp-Second**: The primary, feature-rich user experience is a Terminal User Interface (e.g., built with Bubble Tea). The webapp is a secondary, lightweight client-server viewer for convenience or remote checking, not the primary driver of the product.
2. **Repo-Anchored by Default**: Every channel and thread must be tied to a local Git repository path. This ensures `kata` and `roborev` can operate sanely on the exact same context. Conversations explicitly decoupled from a repo are relegated to isolated, persistent "orphaned" scratchpad channels.
3. **Strict Agent Boundaries**:
   - Agents **cannot** create channels or threads.
   - Agents are **append-only**. They may only add messages or emoji reactions to existing threads.
   - Only humans possess destructive permissions (e.g., deleting sessions, archiving threads, modifying metadata).
4. **Portable Agent State (JSONL)**: Agent threads are treated as structured, append-only JSONL streams. This makes agent sessions trivial to export, share, and resume by another human in their own ACP (Agent Client Protocol) compatible environment.
5. **No Hidden Agent Memory**: Fluffle does not implement vector databases, embedding stores, or opaque long-term agent memory. The thread history *is* the context. What is in the JSONL is what the agent receives.
6. **SQLite-First & Local Daemon**: The system operates on a local client-server architecture backed by a single SQLite database. The `flf` CLI manages the local daemon, and clients (TUI or Web) connect to it.

---

## 🔌 Integration Philosophy: "Glue, Not a Replacement"
Fluffle deliberately avoids building heavy, specialized workflows. It defers to existing tools:
- **Code Review**: Fluffle does not build a PR review UI. Instead, a Fluffle thread can link to, or be spawned by, a `roborev` CLI command.
- **Task/Issue Tracking**: Fluffle does not build a Kanban board. Channels can mirror, trigger, or link to `kata` daemon workflows.
- **Division of Labor**: Fluffle provides the chat, context-sharing, and agent handoff. `kata` and `roborev` provide the specialized execution and deep Git integration.

---

## 🏗️ Architectural Posture

- **Language**: Golang.
- **Data Layer**: SQLite (local file, accessed via a local daemon).
- **Primary Client**: TUI (Terminal User Interface). Rich, keyboard-driven, responsive (similar to `kata`'s terminal layout).
- **Secondary Client**: Lightweight Webapp (HTTP + WebSocket) for browser-based viewing when the terminal is unavailable.
- **Agent Interface**: Agents interact primarily via the `flf` CLI. An external agent framework (or local LLM runner) executes CLI commands to read thread context and append responses/reactions.

---

## 🤝 Key Workflows

1. **Human-to-Human Sync**: A developer opens the TUI, navigates to a repo-anchored channel, and discusses a refactor with a colleague in a clean, distraction-free thread layout.
2. **Human-to-Agent Pairing**: A developer invokes an agent within a thread (`flf agent invoke`). The agent reads the thread's JSONL history, processes it via a local model, and appends its response and/or emoji reactions via the CLI.
3. **Seamless Session Handoff**: Developer A exports an active agent thread (`flf thread export --format jsonl`). Developer B imports this file into their local Fluffle instance, instantly resuming the agent session using their own local ACP setup and models, with zero loss of context.

---

## 💻 CLI (`flf`) Surface Sketch
The CLI is the control plane. It manages the daemon, provides TUI entry, and serves as the API for external agents.

```bash
# Daemon & Initialization
$ flf daemon start          # Start the local SQLite-backed server
$ flf init                  # Initialize fluffle context in a git repository

# TUI Entry (Primary Interface)
$ flf tui                   # Launch the main terminal user interface

# Channel & Thread Management (Human)
$ flf channel list --repo ./my-project
$ flf thread new --channel "auth-refactor" --title "Schema migration"

# Messaging & Reactions
$ flf message send --thread 42 --text "Reviewing the migration script now."
$ flf react add --message 105 --emoji "👀"  # Available to both humans and agents

# Agent Interaction (Append-only, CLI-triggered)
# External agent frameworks call this to read context and append responses
$ flf agent read --thread 42 --last 5       # Outputs last 5 messages as JSONL
$ flf agent append --thread 42 --file response.jsonl 

# Session Handoff (ACP Compatibility)
$ flf thread export --thread 42 --format jsonl > session.jsonl
$ flf thread import --file session.jsonl --channel "local-context"
```

---

## 🔍 Precision Check: Edge Cases to Define Next
To maintain this level of clarity during implementation, the following decisions should be locked in early:

1. **Daemon Lifecycle**: Does `flf tui` automatically spawn and manage the background daemon, or must the user run `flf daemon start` separately? (Auto-spawning is more user-friendly for a local-first tool).
2. **Agent Authentication**: How does an external agent framework authenticate with the `flf` daemon to prove it is allowed to append to a thread? (e.g., Localhost-only restriction, or a simple pre-shared token?).
3. **ACP JSONL Schema**: Define the exact, minimal JSONL schema (e.g., `{ "role": "user"|"assistant"|"system", "content": "...", "timestamp": "...", "metadata": {...} }`) to guarantee true portability.
4. **Orphaned Channel Lifecycle**: Are orphaned channels permanently persistent, or do they have a time-to-live (TTL) to prevent SQLite bloat?

