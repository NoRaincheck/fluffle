package apiserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/store"
)

const (
	maxBatchBodyBytes = 1 << 20
	maxBatchEvents    = 1000
)

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type batchRequest struct {
	Events []jsonl.Line `json:"events"`
	Import bool         `json:"import"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errBody{Code: code, Message: msg})
}

func isAgent(r *http.Request) bool { return r.Header.Get("X-Fluffle-Agent") != "" }

var (
	shutdownMu sync.Mutex
	shutdownFn func()
)

func SetShutdown(fn func()) {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	shutdownFn = fn
}

func shutdownHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, 405, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "shutting_down"})
		shutdownMu.Lock()
		fn := shutdownFn
		shutdownMu.Unlock()
		if fn != nil {
			go fn()
		}
	}
}

type jsonResponseWriter struct {
	http.ResponseWriter
	converted   bool
	wroteHeader bool
}

func (w *jsonResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if w.Header().Get("Content-Type") == "application/json" {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.Header().Del("Content-Length")
	writeErr(w.ResponseWriter, status, "CHANNEL_NOT_FOUND", "unknown route")
	w.converted = true
}

func (w *jsonResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.converted {
		return len(data), nil
	}
	return w.ResponseWriter.Write(data)
}

func jsonMuxHandler(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(&jsonResponseWriter{ResponseWriter: w}, r)
	})
}

func liveAuthorType(r *http.Request) string {
	if isAgent(r) {
		return "agent"
	}
	return "human"
}

func listThreadMessages(s *store.Store, threadID int64, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	query := r.URL.Query()
	afterValues, hasAfter := query["after_seq"]
	_, hasLast := query["last"]
	if hasAfter && hasLast {
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", "after_seq and last are mutually exclusive")
		return
	}

	var (
		messages []store.Message
		err      error
	)
	if hasAfter {
		if len(afterValues) != 1 {
			writeErr(w, http.StatusBadRequest, "BAD_JSONL", "after_seq must be a non-negative integer")
			return
		}
		afterSeq, parseErr := strconv.ParseInt(afterValues[0], 10, 64)
		if parseErr != nil || afterSeq < 0 {
			writeErr(w, http.StatusBadRequest, "BAD_JSONL", "after_seq must be a non-negative integer")
			return
		}
		messages, err = s.ListMessagesAfter(threadID, afterSeq)
	} else {
		last, _ := strconv.Atoi(query.Get("last"))
		messages, err = s.ListMessages(threadID, last)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", err.Error())
		return
	}
	if messages == nil {
		messages = []store.Message{}
	}
	writeJSON(w, http.StatusOK, messages)
}

func appendThreadMessage(s *store.Store, threadID int64, w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", err.Error())
		return
	}
	var body struct {
		Name, Role, Content, AgentID, CreatedAt string
		ParentID, ParentSeq                     int64
	}
	for key, value := range raw {
		var target any
		switch key {
		case "Name", "name", "Author", "author":
			target = &body.Name
		case "Role", "role":
			target = &body.Role
		case "Content", "content":
			target = &body.Content
		case "AgentID", "agent_id":
			target = &body.AgentID
		case "CreatedAt", "created_at":
			target = &body.CreatedAt
		case "ParentID", "parent_id", "parentId":
			target = &body.ParentID
		case "ParentSeq", "parent_seq", "parentSeq":
			target = &body.ParentSeq
		default:
			continue
		}
		if err := json.Unmarshal(value, target); err != nil {
			writeErr(w, http.StatusBadRequest, "BAD_JSONL", err.Error())
			return
		}
	}

	name := body.Name
	if name == "" {
		name = body.AgentID
	}
	if name == "" {
		name = "unknown"
	}
	authorType := liveAuthorType(r)
	var (
		seq int64
		err error
	)
	switch {
	case body.ParentSeq != 0:
		seq, _, err = s.AppendMessageByParentSeq(threadID, body.ParentSeq, name, authorType, body.Role, body.Content, body.CreatedAt)
	case body.CreatedAt != "":
		seq, err = s.AppendMessageAtWithParent(threadID, name, authorType, body.Role, body.Content, body.CreatedAt, body.ParentID)
	case body.ParentID != 0:
		seq, err = s.AppendMessageWithParent(threadID, name, authorType, body.Role, body.Content, body.ParentID)
	default:
		seq, err = s.AppendMessage(threadID, name, authorType, body.Role, body.Content)
	}
	if err != nil {
		writeThreadMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq})
}

func listThreadReactions(s *store.Store, threadID int64, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	reactions, err := s.ListReactions(threadID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", err.Error())
		return
	}
	if reactions == nil {
		reactions = []store.Reaction{}
	}
	writeJSON(w, http.StatusOK, reactions)
}

func appendThreadReaction(s *store.Store, threadID, messageSeq int64, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var body struct {
		Emoji, Name, AgentID string
		AgentIDSnake         string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", err.Error())
		return
	}
	name := body.Name
	if name == "" {
		name = body.AgentID
	}
	if name == "" {
		name = body.AgentIDSnake
	}
	if name == "" {
		name = "unknown"
	}
	if err := s.AddReactionBySeq(threadID, messageSeq, body.Emoji, name, liveAuthorType(r)); err != nil {
		writeThreadMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func appendThreadEvents(s *store.Store, threadID int64, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBatchBodyBytes)
	decoder := json.NewDecoder(r.Body)
	var body batchRequest
	if err := decoder.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", err.Error())
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", err.Error())
		return
	}
	if len(body.Events) == 0 {
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", "events must not be empty")
		return
	}
	if len(body.Events) > maxBatchEvents {
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", "too many events")
		return
	}

	agentRequest := isAgent(r)
	events := make([]store.AppendEvent, len(body.Events))
	for i, line := range body.Events {
		authorType := "human"
		if agentRequest {
			authorType = "agent"
		}
		if body.Import && !agentRequest {
			authorType = line.AuthorType
			if authorType == "" {
				authorType = "human"
			}
		}
		events[i] = store.AppendEvent{
			Type:       line.Type,
			SourceSeq:  line.Seq,
			ParentSeq:  line.ParentSeq,
			MessageSeq: line.MessageSeq,
			Name:       line.Name,
			AuthorType: authorType,
			Role:       line.Role,
			Content:    line.Content,
			Emoji:      line.Emoji,
			CreatedAt:  line.Timestamp,
		}
	}
	results, err := s.AppendBatch(threadID, events)
	if err != nil {
		writeThreadMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func writeThreadMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "THREAD_NOT_FOUND", "thread or event target not found")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "BAD_JSONL", "duplicate reaction")
	case isClientMutationError(err):
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", err.Error())
	}
}

func isClientMutationError(err error) bool {
	if strings.HasPrefix(err.Error(), "parsing time ") {
		return true
	}
	switch err.Error() {
	case "name and content required",
		"bad author_type",
		"bad role",
		"parent_seq must be non-negative",
		"parent message not in this thread",
		"emoji and name required",
		"source_seq must be non-negative",
		"message_seq must be positive",
		"unknown event type",
		"duplicate source_seq",
		"unsorted source_seq":
		return true
	default:
		return false
	}
}

func NewHandler(s *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/v1/inbox", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		msgs, err := s.ListInbox(limit)
		if err != nil {
			writeErr(w, 500, "DAEMON_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, msgs)
	})
	mux.HandleFunc("/v1/channels", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			repo := r.URL.Query().Get("repo")
			inc := r.URL.Query().Get("include-orphaned") == "1"
			list, err := s.ListChannels(repo, inc)
			if err != nil {
				writeErr(w, 500, "DAEMON_ERROR", err.Error())
				return
			}
			if list == nil {
				list = []store.Channel{}
			}
			writeJSON(w, http.StatusOK, list)
		case "POST":
			if isAgent(r) {
				writeErr(w, 403, "AGENT_FORBIDDEN", "agents cannot create channels")
				return
			}
			var body struct {
				Name, RepoAbsPath, RepoRemote, RepoHeadSHA, RepoHeadBranch string
				Orphaned                                                   bool `json:"orphaned"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeErr(w, 400, "BAD_JSONL", err.Error())
				return
			}
			id, err := s.CreateChannel(body.Name, body.RepoAbsPath, body.RepoRemote, body.RepoHeadSHA, body.RepoHeadBranch, body.Orphaned)
			if err == store.ErrConflict {
				writeErr(w, 409, "CHANNEL_EXISTS", "channel exists")
				return
			}
			if err != nil {
				writeErr(w, 400, "NOT_A_GIT_REPO", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": id})
		default:
			writeErr(w, 405, "METHOD_NOT_ALLOWED", "method not allowed")
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
				writeErr(w, 500, "DAEMON_ERROR", err.Error())
				return
			}
			if list == nil {
				list = []store.Thread{}
			}
			writeJSON(w, http.StatusOK, list)
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
				writeErr(w, 400, "BAD_JSONL", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": tid})
		default:
			writeErr(w, 405, "METHOD_NOT_ALLOWED", "method not allowed")
		}
	})
	mux.HandleFunc("/v1/threads/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/threads/")
		parts := strings.Split(rest, "/")
		if len(parts) < 2 {
			writeErr(w, http.StatusNotFound, "THREAD_NOT_FOUND", "unknown route")
			return
		}
		threadID, _ := strconv.ParseInt(parts[0], 10, 64)
		switch {
		case len(parts) == 2 && parts[1] == "messages":
			switch r.Method {
			case http.MethodGet:
				listThreadMessages(s, threadID, w, r)
			case http.MethodPost:
				appendThreadMessage(s, threadID, w, r)
			default:
				writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			}
		case len(parts) == 2 && parts[1] == "reactions":
			listThreadReactions(s, threadID, w, r)
		case len(parts) == 2 && parts[1] == "events":
			appendThreadEvents(s, threadID, w, r)
		case len(parts) == 4 && parts[1] == "messages" && parts[3] == "reactions":
			messageSeq, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil || messageSeq <= 0 {
				writeErr(w, http.StatusBadRequest, "BAD_JSONL", "message sequence must be a positive integer")
				return
			}
			appendThreadReaction(s, threadID, messageSeq, w, r)
		default:
			writeErr(w, http.StatusNotFound, "THREAD_NOT_FOUND", "unknown route")
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
			Emoji, Name, AgentID string
			AgentIDSnake         string `json:"agent_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "BAD_JSONL", err.Error())
			return
		}
		authorType := liveAuthorType(r)
		name := body.Name
		if name == "" {
			name = body.AgentID
		}
		if name == "" {
			name = body.AgentIDSnake
		}
		if name == "" {
			name = "unknown"
		}
		if err := s.AddReaction(id, body.Emoji, name, authorType); err == store.ErrConflict {
			writeErr(w, 409, "BAD_JSONL", "duplicate reaction")
			return
		} else if err != nil {
			writeErr(w, 400, "BAD_JSONL", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/shutdown", shutdownHandler(s))
	return jsonMuxHandler(mux)
}
