package apiserver

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/mentions"
	"github.com/NoRaincheck/fluffle/internal/session"
	"github.com/NoRaincheck/fluffle/internal/store"
)

const repoConfigFile = ".flf.toml"

type SessionStarter interface {
	Start(threadID, triggerMessageID int64, names []string)
}

type SessionCanceler interface {
	Cancel(id int64) error
}

type Deps struct {
	Starter  SessionStarter
	Agents   *agentcfg.Loader
	Canceler SessionCanceler
}

type agentListItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Reply       string `json:"reply"`
	Source      string `json:"source"`
}

func (d Deps) startSessionForMessage(threadID, messageID int64, content string) {
	if d.Starter == nil {
		return
	}
	names, _ := mentions.Parse(content)
	if len(names) == 0 {
		return
	}
	d.Starter.Start(threadID, messageID, names)
}

func (d Deps) startSessionsForBatch(s *store.Store, threadID int64, events []store.AppendEvent, results []store.AppendResult) {
	if d.Starter == nil {
		return
	}
	for i, r := range results {
		if r.Seq <= 0 || r.MessageID == 0 || i >= len(events) {
			continue
		}
		if names, _ := mentions.Parse(events[i].Content); len(names) == 0 {
			continue
		}
		msg, err := s.MessageByID(r.MessageID)
		if err != nil || msg.AuthorType == "agent" {
			continue
		}
		d.startSessionForMessage(threadID, r.MessageID, msg.Content)
	}
}

func registerSessionRoutes(mux *http.ServeMux, s *store.Store, d Deps) {
	mux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
		parts := strings.Split(rest, "/")
		id, valid := parsePositiveRouteID(parts[0])
		if !valid {
			writeErr(w, http.StatusBadRequest, "BAD_JSONL", "session id must be a positive integer")
			return
		}
		switch {
		case len(parts) == 1:
			if r.Method != http.MethodGet {
				writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
				return
			}
			serveSession(s, id, w)
		case len(parts) == 2 && parts[1] == "cancel":
			if r.Method != http.MethodPost {
				writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
				return
			}
			serveCancel(s, d, id, w, r)
		default:
			writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", "unknown route")
		}
	})
	mux.HandleFunc("/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		serveAgentList(d, w, r)
	})
}

func serveSession(s *store.Store, id int64, w http.ResponseWriter) {
	sess, err := s.GetSession(id)
	if err != nil {
		writeSessionError(w, err)
		return
	}
	events, err := s.ListSessionEvents(id)
	if err != nil {
		writeSessionError(w, err)
		return
	}
	if events == nil {
		events = []store.SessionEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess, "events": events})
}

func serveCancel(s *store.Store, d Deps, id int64, w http.ResponseWriter, r *http.Request) {
	if isAgent(r) {
		writeErr(w, http.StatusForbidden, "AGENT_FORBIDDEN", "agents cannot cancel sessions")
		return
	}
	if _, err := s.GetSession(id); err != nil {
		writeSessionError(w, err)
		return
	}
	if d.Canceler == nil {
		writeErr(w, http.StatusServiceUnavailable, "DAEMON_ERROR", "agent execution is not available")
		return
	}
	if err := d.Canceler.Cancel(id); err != nil {
		if errors.Is(err, session.ErrTerminal) {
			writeErr(w, http.StatusConflict, "SESSION_FINISHED", "session already finished")
			return
		}
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func serveAgentList(d Deps, w http.ResponseWriter, r *http.Request) {
	if d.Agents == nil {
		writeJSON(w, http.StatusOK, map[string]any{"agents": []agentListItem{}})
		return
	}
	repo := r.URL.Query().Get("repo")
	if info, err := os.Lstat(filepath.Join(repo, repoConfigFile)); err == nil && info.Mode()&os.ModeSymlink != 0 {
		slog.Error("refusing symlinked agent config", "repo", repo)
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", "agent configuration could not be loaded")
		return
	}
	set, err := d.Agents.Resolve(repo)
	if err != nil {
		slog.Error("agent configuration could not be loaded", "repo", repo, "err", err)
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", "agent configuration could not be loaded")
		return
	}
	entries := set.Entries()
	out := make([]agentListItem, 0, len(entries))
	for _, e := range entries {
		out = append(out, agentListItem{
			Name:        e.Name,
			Description: e.Description,
			Command:     e.Command,
			Reply:       e.Reply,
			Source:      e.Source,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func writeSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", "no such session")
	default:
		slog.Error("session request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", "session request failed")
	}
}
