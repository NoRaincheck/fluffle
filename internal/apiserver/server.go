package apiserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(errBody{Code: code, Message: msg})
}

func isAgent(r *http.Request) bool { return r.Header.Get("X-Fluffle-Agent") != "" }

func NewHandler(s *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/v1/channels", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			repo := r.URL.Query().Get("repo")
			inc := r.URL.Query().Get("include-orphaned") == "1"
			list, err := s.ListChannels(repo, inc)
			if err != nil {
				writeErr(w, 500, "DAEMON_DOWN", err.Error())
				return
			}
			json.NewEncoder(w).Encode(list)
		case "POST":
			if isAgent(r) {
				writeErr(w, 403, "AGENT_FORBIDDEN", "agents cannot create channels")
				return
			}
			var body struct {
				Name, RepoAbsPath, RepoRemote, RepoHeadSHA string
				Orphaned                                   bool `json:"orphaned"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeErr(w, 400, "BAD_JSONL", err.Error())
				return
			}
			id, err := s.CreateChannel(body.Name, body.RepoAbsPath, body.RepoRemote, body.RepoHeadSHA, body.Orphaned)
			if err == store.ErrConflict {
				writeErr(w, 409, "CHANNEL_NOT_FOUND", "channel exists")
				return
			}
			if err != nil {
				writeErr(w, 400, "NOT_A_GIT_REPO", err.Error())
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": id})
		default:
			writeErr(w, 405, "DAEMON_DOWN", "method not allowed")
		}
	})
	mux.HandleFunc("/v1/channels/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/channels/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[1] != "threads" {
			writeErr(w, 404, "CHANNEL_NOT_FOUND", "unknown route")
			return
		}
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		switch r.Method {
		case "GET":
			list, err := s.ListThreads(id)
			if err != nil {
				writeErr(w, 500, "DAEMON_DOWN", err.Error())
				return
			}
			json.NewEncoder(w).Encode(list)
		case "POST":
			if isAgent(r) {
				writeErr(w, 403, "AGENT_FORBIDDEN", "agents cannot create threads")
				return
			}
			var body struct {
				Title string `json:"title"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeErr(w, 400, "BAD_JSONL", err.Error())
				return
			}
			tid, err := s.CreateThread(id, body.Title)
			if err == store.ErrNotFound {
				writeErr(w, 404, "CHANNEL_NOT_FOUND", "no such channel")
				return
			}
			if err != nil {
				writeErr(w, 400, "DAEMON_DOWN", err.Error())
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": tid})
		default:
			writeErr(w, 405, "DAEMON_DOWN", "method not allowed")
		}
	})
	mux.HandleFunc("/v1/threads/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/threads/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[1] != "messages" {
			writeErr(w, 404, "THREAD_NOT_FOUND", "unknown route")
			return
		}
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		switch r.Method {
		case "GET":
			last, _ := strconv.Atoi(r.URL.Query().Get("last"))
			msgs, err := s.ListMessages(id, last)
			if err != nil {
				writeErr(w, 500, "DAEMON_DOWN", err.Error())
				return
			}
			json.NewEncoder(w).Encode(msgs)
		case "POST":
			var body struct {
				Author, Role, Content, AgentID, CreatedAt string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeErr(w, 400, "BAD_JSONL", err.Error())
				return
			}
			authorType := "human"
			if body.AgentID != "" || isAgent(r) {
				authorType = "agent"
			}
			author := body.Author
			if author == "" {
				author = body.AgentID
			}
			if author == "" {
				author = "unknown"
			}
			var seq int64
			var err error
			if body.CreatedAt != "" {
				seq, err = s.AppendMessageAt(id, author, authorType, body.Role, body.Content, body.CreatedAt)
			} else {
				seq, err = s.AppendMessage(id, author, authorType, body.Role, body.Content)
			}
			if err == store.ErrNotFound {
				writeErr(w, 404, "THREAD_NOT_FOUND", "no such thread")
				return
			}
			if err != nil {
				writeErr(w, 400, "BAD_JSONL", err.Error())
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"seq": seq})
		default:
			writeErr(w, 405, "DAEMON_DOWN", "method not allowed")
		}
	})
	mux.HandleFunc("/v1/messages/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/messages/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[1] != "reactions" || r.Method != "POST" {
			writeErr(w, 404, "THREAD_NOT_FOUND", "unknown route")
			return
		}
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		var body struct {
			Emoji, Author, AgentID string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "BAD_JSONL", err.Error())
			return
		}
		authorType := "human"
		if body.AgentID != "" || isAgent(r) {
			authorType = "agent"
		}
		author := body.Author
		if author == "" {
			author = body.AgentID
		}
		if author == "" {
			author = "unknown"
		}
		if err := s.AddReaction(id, body.Emoji, author, authorType); err == store.ErrConflict {
			writeErr(w, 409, "DAEMON_DOWN", "duplicate reaction")
			return
		} else if err != nil {
			writeErr(w, 400, "BAD_JSONL", err.Error())
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	return mux
}
