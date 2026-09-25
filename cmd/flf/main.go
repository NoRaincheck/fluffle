package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/NoRaincheck/fluffle/internal/apiserver"
	"github.com/NoRaincheck/fluffle/internal/client"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/repo"
	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui"
)

func main() { os.Exit(run(os.Args[1:])) }

const rootUsage = "usage: flf <daemon|init|channel|thread|message|react|agent|tui>\n  Channel: repo-anchored or --orphaned room (e.g. general)\n  Thread: titled conversation inside a channel\n  Message: chat line inside a thread (use --thread ID)"

func run(args []string) int {
	if len(args) == 0 {
		return fail("BAD_ARGS", rootUsage)
	}
	switch args[0] {
	case "daemon":
		return daemonCmd(args[1:])
	case "init":
		return initCmd(args[1:])
	case "channel":
		return channelCmd(args[1:])
	case "thread":
		return threadCmd(args[1:])
	case "message":
		return messageCmd(args[1:])
	case "react":
		return reactCmd(args[1:])
	case "agent":
		return agentCmd(args[1:])
	case "tui":
		result := tui.Run()
		if !result.OK {
			return fail(result.Code, result.Message)
		}
		return 0
	default:
		return fail("BAD_ARGS", rootUsage)
	}
}

type apiErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func printAPIError(resp *http.Response) int {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fail("DAEMON_ERROR", fmt.Sprintf("read error response: %v", err))
	}
	var eb apiErrBody
	if err := json.Unmarshal(body, &eb); err != nil || eb.Code == "" {
		return fail("DAEMON_ERROR", fmt.Sprintf("status %d: invalid error response", resp.StatusCode))
	}
	return fail(eb.Code, eb.Message)
}

func apiGet(u, agentID string, out any) int {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	if agentID != "" {
		req.Header.Set("X-Fluffle-Agent", agentID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return printAPIError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fail("DAEMON_ERROR", err.Error())
		}
	}
	return 0
}

func apiPost(u, agentID string, payload any, out any) int {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fail("BAD_REQUEST", err.Error())
	}
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return fail("BAD_REQUEST", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	if agentID != "" {
		req.Header.Set("X-Fluffle-Agent", agentID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail("DELIVERY_UNKNOWN", fmt.Sprintf("read the thread before retrying: %v", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return printAPIError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fail("DELIVERY_UNKNOWN", fmt.Sprintf("read the thread before retrying: %v", err))
		}
	}
	return 0
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func initCmd(args []string) int {
	fs := newFlagSet("init")
	repoFlag := fs.String("repo", "", "repo directory (default: cwd)")
	orphaned := fs.Bool("orphaned", false, "allow initializing outside a git repo")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	dir := *repoFlag
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fail("NOT_A_GIT_REPO", err.Error())
		}
		dir = cwd
	}
	abs, err := repo.Canonicalize(dir)
	if err != nil {
		return fail("NOT_A_GIT_REPO", fmt.Sprintf("%s is not a git repo (suggest --orphaned)", dir))
	}
	_, _, isGit := repo.InspectGitDir(abs)
	if !isGit && !*orphaned {
		return fail("NOT_A_GIT_REPO", fmt.Sprintf("%s is not a git repo (suggest --orphaned)", abs))
	}
	fmt.Printf("initialized %s\n", abs)
	return 0
}

func channelCmd(args []string) int {
	if len(args) == 0 {
		return fail("BAD_ARGS", "usage: flf channel <list|create>\n  Channel: repo-anchored room or --orphaned (no git)")
	}
	switch args[0] {
	case "list":
		return channelListCmd(args[1:])
	case "create":
		return channelCreateCmd(args[1:])
	default:
		return fail("BAD_ARGS", "usage: flf channel <list|create>")
	}
}

func channelListCmd(args []string) int {
	fs := newFlagSet("channel list")
	repoPath := fs.String("repo", "", "repo path (default: cwd)")
	includeOrphaned := fs.Bool("include-orphaned", false, "include orphaned channels")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	var abs string
	if *repoPath != "" {
		var err error
		abs, err = repo.Canonicalize(*repoPath)
		if err != nil {
			return fail("NOT_A_GIT_REPO", *repoPath)
		}
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	inc := "0"
	if *includeOrphaned {
		inc = "1"
	}
	var list []store.Channel
	u := base + "/v1/channels?include-orphaned=" + inc
	if abs != "" {
		u += "&repo=" + url.QueryEscape(abs)
	}
	if code := apiGet(u, "", &list); code != 0 {
		return code
	}
	if list == nil {
		list = []store.Channel{}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(list)
		return 0
	}
	for _, c := range list {
		fmt.Printf("%d %s\n", c.ID, c.Name)
	}
	return 0
}

func channelCreateCmd(args []string) int {
	fs := newFlagSet("channel create")
	name := fs.String("name", "", "channel name")
	repoPath := fs.String("repo", "", "repo path (default: cwd)")
	orphaned := fs.Bool("orphaned", false, "create orphaned channel")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *name == "" {
		return fail("BAD_ARGS", "usage: flf channel create --name N (--repo PATH | --orphaned)")
	}
	if *orphaned && *repoPath != "" {
		return fail("BAD_ARGS", "usage: flf channel create --name N (--repo PATH | --orphaned)")
	}
	var body map[string]any
	if *orphaned {
		body = map[string]any{"Name": *name, "Orphaned": true}
	} else {
		abs := *repoPath
		if abs == "" {
			var err error
			abs, err = os.Getwd()
			if err != nil {
				return fail("CWD_ERROR", err.Error())
			}
		}
		abs, err := repo.Canonicalize(abs)
		if err != nil {
			return fail("NOT_A_GIT_REPO", fmt.Sprintf("%s (suggest --orphaned)", abs))
		}
		remote, head, isGit := repo.InspectGitDir(abs)
		if !isGit {
			return fail("NOT_A_GIT_REPO", fmt.Sprintf("%s (suggest --orphaned)", abs))
		}
		body = map[string]any{"Name": *name, "RepoAbsPath": abs, "RepoRemote": remote, "RepoHeadSHA": head, "Orphaned": false}
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if code := apiPost(base+"/v1/channels", "", body, &out); code != 0 {
		return code
	}
	if *jsonOut {
		var list []store.Channel
		u := base + "/v1/channels?include-orphaned=1"
		if code := apiGet(u, "", &list); code != 0 {
			return code
		}
		for _, c := range list {
			if c.ID == out.ID {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				enc.Encode(c)
				return 0
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return 0
	}
	fmt.Printf("channel %d\n", out.ID)
	return 0
}

func threadCmd(args []string) int {
	if len(args) == 0 {
		return fail("BAD_ARGS", "usage: flf thread <list|new|export|import>")
	}
	switch args[0] {
	case "list":
		return threadListCmd(args[1:])
	case "new":
		return threadNewCmd(args[1:])
	case "export":
		return threadExportCmd(args[1:])
	case "import":
		return threadImportCmd(args[1:])
	default:
		return fail("BAD_ARGS", "usage: flf thread <list|new|export|import>")
	}
}

func resolveChannelID(base, abs, name string, orphaned bool) (int64, int) {
	var list []store.Channel
	var u string
	if orphaned {
		u = base + "/v1/channels?include-orphaned=1"
	} else {
		u = base + "/v1/channels?repo=" + url.QueryEscape(abs)
	}
	if code := apiGet(u, "", &list); code != 0 {
		return 0, code
	}
	for _, c := range list {
		if c.Name == name && c.IsOrphaned == orphaned {
			return c.ID, 0
		}
	}
	return 0, fail("CHANNEL_NOT_FOUND", name)
}

func resolveChannelIDLegacy(base, abs, name string) (int64, int) {
	return resolveChannelID(base, abs, name, false)
}

func threadListCmd(args []string) int {
	fs := newFlagSet("thread list")
	channel := fs.String("channel", "", "channel name")
	repoPath := fs.String("repo", "", "repo path (default: cwd)")
	orphaned := fs.Bool("orphaned", false, "list threads in orphaned channel")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *channel == "" {
		return fail("BAD_ARGS", "usage: flf thread list --channel NAME [--repo PATH | --orphaned] [--json]")
	}
	if *orphaned && *repoPath != "" {
		return fail("BAD_ARGS", "usage: flf thread list --channel NAME [--repo PATH | --orphaned]")
	}
	var abs string
	if !*orphaned {
		abs = *repoPath
		if abs == "" {
			var err error
			abs, err = os.Getwd()
			if err != nil {
				return fail("CWD_ERROR", err.Error())
			}
		}
		var err error
		abs, err = repo.Canonicalize(abs)
		if err != nil {
			return fail("NOT_A_GIT_REPO", abs)
		}
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	id, code := resolveChannelID(base, abs, *channel, *orphaned)
	if code != 0 {
		return code
	}
	var list []store.Thread
	if code := apiGet(base+"/v1/channels/"+strconv.FormatInt(id, 10)+"/threads", "", &list); code != 0 {
		return code
	}
	if list == nil {
		list = []store.Thread{}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(list)
		return 0
	}
	for _, th := range list {
		fmt.Printf("%d %s\n", th.ID, th.Title)
	}
	return 0
}

func threadNewCmd(args []string) int {
	fs := newFlagSet("thread new")
	channel := fs.String("channel", "", "channel name")
	repoPath := fs.String("repo", "", "repo path (default: cwd)")
	orphaned := fs.Bool("orphaned", false, "create thread in orphaned channel")
	title := fs.String("title", "", "thread title")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *channel == "" || *title == "" {
		return fail("BAD_ARGS", "usage: flf thread new --channel NAME --title T [--repo PATH | --orphaned] [--json]")
	}
	if *orphaned && *repoPath != "" {
		return fail("BAD_ARGS", "usage: flf thread new --channel NAME --title T [--repo PATH | --orphaned]")
	}
	var abs string
	if !*orphaned {
		abs = *repoPath
		if abs == "" {
			var err error
			abs, err = os.Getwd()
			if err != nil {
				return fail("CWD_ERROR", err.Error())
			}
		}
		var err error
		abs, err = repo.Canonicalize(abs)
		if err != nil {
			return fail("NOT_A_GIT_REPO", abs)
		}
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	id, code := resolveChannelID(base, abs, *channel, *orphaned)
	if code != 0 {
		return code
	}
	var out struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{"Title": *title}
	if code := apiPost(base+"/v1/channels/"+strconv.FormatInt(id, 10)+"/threads", "", body, &out); code != 0 {
		return code
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"id": out.ID, "title": *title, "channel_id": id, "channel": *channel})
		return 0
	}
	fmt.Printf("thread %d\n", out.ID)
	return 0
}

func dumpThreadMessages(base string, threadID int64, last int, agentID string) int {
	u := base + "/v1/threads/" + strconv.FormatInt(threadID, 10) + "/messages"
	if last > 0 {
		u += "?last=" + strconv.Itoa(last)
	}
	var msgs []store.Message
	if code := apiGet(u, agentID, &msgs); code != 0 {
		return code
	}
	seqByID := make(map[int64]int64, len(msgs))
	for _, m := range msgs {
		seqByID[m.ID] = m.Seq
	}
	lines := make([]jsonl.Line, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, jsonl.Line{
			Seq:        m.Seq,
			ParentSeq:  seqByID[m.ParentIDValue()],
			Role:       m.Role,
			Name:       m.Name,
			AuthorType: m.AuthorType,
			Content:    m.Content,
			Timestamp:  m.CreatedAt,
			Metadata:   map[string]any{},
		})
	}
	os.Stdout.Write(jsonl.EncodeLines(lines))
	return 0
}

func parseJSONLFile(path string) ([]jsonl.Line, int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fail("FILE_READ", err.Error())
	}
	lines, err := jsonl.ParseLines(raw)
	if err != nil {
		return nil, fail("BAD_JSONL", err.Error())
	}
	return lines, 0
}

func postJSONLLines(base string, threadID int64, lines []jsonl.Line, agentID string) int {
	u := base + "/v1/threads/" + strconv.FormatInt(threadID, 10) + "/messages"
	for _, l := range lines {
		role := l.Role
		if role == "" {
			role = "user"
		}
		body := map[string]any{"Name": l.Name, "Role": role, "Content": l.Content, "AgentID": agentID, "CreatedAt": l.Timestamp}
		var out struct {
			Seq int64 `json:"seq"`
		}
		if code := apiPost(u, agentID, body, &out); code != 0 {
			return code
		}
	}
	return 0
}

func agentCmd(args []string) int {
	if len(args) == 0 {
		return fail("BAD_ARGS", "usage: flf agent <read|append>")
	}
	switch args[0] {
	case "read":
		return agentReadCmd(args[1:])
	case "append":
		return agentAppendCmd(args[1:])
	default:
		return fail("BAD_ARGS", "usage: flf agent <read|append>")
	}
}

func agentReadCmd(args []string) int {
	fs := newFlagSet("agent read")
	threadID := fs.Int64("thread", 0, "thread id")
	last := fs.Int("last", 0, "last N messages (0 = all)")
	agentID := fs.String("agent-id", "", "agent id")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *threadID == 0 {
		return fail("BAD_ARGS", "usage: flf agent read --thread ID [--last N] [--agent-id ID]")
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	if *jsonOut {
		return dumpThreadMessages(base, *threadID, *last, *agentID)
	}
	var msgs []store.Message
	u := base + "/v1/threads/" + strconv.FormatInt(*threadID, 10) + "/messages"
	if *last > 0 {
		u += "?last=" + strconv.Itoa(*last)
	}
	if code := apiGet(u, *agentID, &msgs); code != 0 {
		return code
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(msgs)
	return 0
}

func agentAppendCmd(args []string) int {
	fs := newFlagSet("agent append")
	threadID := fs.Int64("thread", 0, "thread id")
	file := fs.String("file", "", "JSONL file to append")
	agentID := fs.String("agent-id", "", "agent id")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *threadID == 0 || *file == "" {
		return fail("BAD_ARGS", "usage: flf agent append --thread ID --file F [--agent-id ID]")
	}
	lines, code := parseJSONLFile(*file)
	if code != 0 {
		return code
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	return postJSONLLines(base, *threadID, lines, *agentID)
}

func threadExportCmd(args []string) int {
	fs := newFlagSet("thread export")
	threadID := fs.Int64("thread", 0, "thread id")
	format := fs.String("format", "jsonl", "export format (only jsonl)")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *threadID == 0 {
		return fail("BAD_ARGS", "usage: flf thread export --thread ID --format jsonl")
	}
	if *format != "jsonl" {
		return fail("BAD_JSONL", "only jsonl supported")
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	return dumpThreadMessages(base, *threadID, 0, "")
}

func ensureImportChannel(base, name, repoPath string, orphaned bool) (int64, int) {
	if orphaned {
		var list []store.Channel
		if code := apiGet(base+"/v1/channels?include-orphaned=1", "", &list); code != 0 {
			return 0, code
		}
		for _, c := range list {
			if c.Name == name && c.IsOrphaned {
				return c.ID, 0
			}
		}
		var out struct {
			ID int64 `json:"id"`
		}
		body := map[string]any{"Name": name, "Orphaned": true}
		if code := apiPost(base+"/v1/channels", "", body, &out); code != 0 {
			return 0, code
		}
		return out.ID, 0
	}
	abs, err := repo.Canonicalize(repoPath)
	if err != nil {
		return 0, fail("NOT_A_GIT_REPO", repoPath)
	}
	var list []store.Channel
	if code := apiGet(base+"/v1/channels?repo="+url.QueryEscape(abs), "", &list); code != 0 {
		return 0, code
	}
	for _, c := range list {
		if c.Name == name {
			return c.ID, 0
		}
	}
	remote, head, isGit := repo.InspectGitDir(abs)
	if !isGit {
		return 0, fail("NOT_A_GIT_REPO", fmt.Sprintf("%s is not a git repo (suggest --orphaned)", abs))
	}
	var out struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{"Name": name, "RepoAbsPath": abs, "RepoRemote": remote, "RepoHeadSHA": head, "Orphaned": false}
	if code := apiPost(base+"/v1/channels", "", body, &out); code != 0 {
		return 0, code
	}
	return out.ID, 0
}

func threadImportCmd(args []string) int {
	fs := newFlagSet("thread import")
	file := fs.String("file", "", "JSONL file to import")
	channel := fs.String("channel", "", "channel name")
	repoPath := fs.String("repo", "", "repo path")
	orphaned := fs.Bool("orphaned", false, "import into an orphaned channel")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *file == "" || *channel == "" || (*repoPath == "" && !*orphaned) {
		return fail("BAD_ARGS", "usage: flf thread import --file F --channel NAME (--repo PATH | --orphaned)")
	}
	lines, code := parseJSONLFile(*file)
	if code != 0 {
		return code
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	chID, code := ensureImportChannel(base, *channel, *repoPath, *orphaned)
	if code != 0 {
		return code
	}
	var out struct {
		ID int64 `json:"id"`
	}
	title := "import " + filepath.Base(*file)
	body := map[string]any{"Title": title}
	if code := apiPost(base+"/v1/channels/"+strconv.FormatInt(chID, 10)+"/threads", "", body, &out); code != 0 {
		return code
	}
	if code := postJSONLLines(base, out.ID, lines, ""); code != 0 {
		return code
	}
	fmt.Printf("thread %d\n", out.ID)
	return 0
}

func messageCmd(args []string) int {
	if len(args) == 0 || args[0] != "send" {
		return fail("BAD_ARGS", "usage: flf message send --thread ID --text T [--as NAME] [--agent-id ID]")
	}
	return messageSendCmd(args[1:])
}

func defaultName(as string) string {
	if as != "" {
		return as
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}

func messageSendCmd(args []string) int {
	fs := newFlagSet("message send")
	threadID := fs.Int64("thread", 0, "thread id")
	text := fs.String("text", "", "message text")
	as := fs.String("as", "", "name (default $USER)")
	agentID := fs.String("agent-id", "", "agent id")
	replyTo := fs.Int64("reply-to", 0, "parent message id for threaded reply")
	createdAt := fs.String("created-at", "", "message timestamp (RFC3339, e.g. 2025-01-15T10:30:00Z)")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *threadID == 0 || *text == "" {
		return fail("BAD_ARGS", "usage: flf message send --thread ID --text T [--as NAME] [--agent-id ID] [--reply-to ID] [--created-at TS] [--json]")
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	name := defaultName(*as)
	body := map[string]any{"Name": name, "Role": "user", "Content": *text, "AgentID": *agentID}
	if *replyTo != 0 {
		body["ParentID"] = *replyTo
	}
	if *createdAt != "" {
		body["CreatedAt"] = *createdAt
	}
	var out struct {
		Seq int64 `json:"seq"`
	}
	u := base + "/v1/threads/" + strconv.FormatInt(*threadID, 10) + "/messages"
	if code := apiPost(u, *agentID, body, &out); code != 0 {
		return code
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"seq": out.Seq, "thread_id": *threadID, "name": name, "content": *text, "parent_id": *replyTo})
		return 0
	}
	fmt.Printf("seq %d\n", out.Seq)
	return 0
}

func reactCmd(args []string) int {
	if len(args) == 0 || args[0] != "add" {
		return fail("BAD_ARGS", "usage: flf react add --message ID --emoji E [--as NAME] [--agent-id ID]")
	}
	return reactAddCmd(args[1:])
}

func reactAddCmd(args []string) int {
	fs := newFlagSet("react add")
	messageID := fs.Int64("message", 0, "message id")
	emoji := fs.String("emoji", "", "emoji")
	as := fs.String("as", "", "name (default $USER)")
	agentID := fs.String("agent-id", "", "agent id")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *messageID == 0 || *emoji == "" {
		return fail("BAD_ARGS", "usage: flf react add --message ID --emoji E [--as NAME] [--agent-id ID]")
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	name := defaultName(*as)
	body := map[string]any{"Emoji": *emoji, "Name": name, "AgentID": *agentID}
	var out map[string]any
	u := base + "/v1/messages/" + strconv.FormatInt(*messageID, 10) + "/reactions"
	if code := apiPost(u, *agentID, body, &out); code != 0 {
		return code
	}
	fmt.Println("ok")
	return 0
}

func daemonCmd(args []string) int {
	fs := newFlagSet("daemon")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if len(args) == 0 {
		return fail("BAD_ARGS", "usage: flf daemon <start|stop|status> [--json]")
	}
	switch args[0] {
	case "status":
		base, err := client.DaemonBaseURL()
		if err != nil {
			return fail("DAEMON_DOWN", "daemon down")
		}
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.Encode(map[string]any{"status": "up", "url": base})
			return 0
		}
		fmt.Println("daemon up at", base)
		return 0
	case "start":
		background := len(args) > 1 && args[1] == "--background"
		return daemonStart(background)
	case "stop":
		return daemonStop()
	default:
		return fail("BAD_ARGS", "usage: flf daemon <start|stop|status> [--json]")
	}
}

func daemonStop() int {
	b, err := os.ReadFile(filepath.Join(client.FluffleHome(), "daemon.json"))
	if err != nil {
		return fail("DAEMON_DOWN", "not running")
	}
	var df struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(b, &df); err != nil {
		return fail("DAEMON_DOWN", "bad daemon.json")
	}
	proc, err := os.FindProcess(df.PID)
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	if err := proc.Kill(); err != nil {
		os.Remove(filepath.Join(client.FluffleHome(), "daemon.json"))
		return fail("DAEMON_DOWN", err.Error())
	}
	os.Remove(filepath.Join(client.FluffleHome(), "daemon.json"))
	fmt.Println("daemon stopped")
	return 0
}

func daemonStart(background bool) int {
	if background {
		return daemonStartBackground()
	}
	home := client.FluffleHome()
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	dbPath := filepath.Join(home, "fluffle.db")
	s, err := store.Open(dbPath)
	if err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	port := ln.Addr().(*net.TCPAddr).Port
	payload, err := json.Marshal(map[string]any{"port": port, "pid": os.Getpid(), "started_at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	if err := os.WriteFile(filepath.Join(home, "daemon.json"), payload, 0o644); err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	fmt.Println("fluffle daemon on 127.0.0.1:" + strconv.Itoa(port))
	srv := &http.Server{Handler: apiserver.NewHandler(s)}
	apiserver.SetShutdown(func() {
		srv.Close()
	})
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fail("DAEMON_ERROR", err.Error())
	}
	return 0
}

func daemonStartBackground() int {
	home := client.FluffleHome()
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}

	// Fork the daemon into a detached subprocess.
	// Re-exec ourselves with --foreground so the child runs the blocking
	// server loop while the parent exits immediately.
	bin, err := os.Executable()
	if err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	cmd := exec.Command(bin, "daemon", "start")
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	// Wait briefly for the child to write daemon.json so we can report the port.
	for i := 0; i < 50; i++ {
		if data, err := os.ReadFile(filepath.Join(home, "daemon.json")); err == nil {
			var df struct {
				Port int `json:"port"`
			}
			if json.Unmarshal(data, &df) == nil && df.Port > 0 {
				fmt.Println("daemon started on 127.0.0.1:" + strconv.Itoa(df.Port))
				return 0
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("daemon started (port unknown)")
	return 0
}
