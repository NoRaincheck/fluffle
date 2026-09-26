package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"strings"
	"syscall"
	"time"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/apiserver"
	"github.com/NoRaincheck/fluffle/internal/client"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/repo"
	"github.com/NoRaincheck/fluffle/internal/runner"
	"github.com/NoRaincheck/fluffle/internal/session"
	"github.com/NoRaincheck/fluffle/internal/store"
	"github.com/NoRaincheck/fluffle/internal/tui"
)

func main() { os.Exit(run(os.Args[1:])) }

const rootUsage = "usage: flf <daemon|init|inbox|channel|thread|message|react|agent|tui>\n  Channel: repo-anchored or --orphaned room (e.g. general)\n  Thread: titled conversation inside a channel\n  Message: chat line inside a thread (use --thread ID)"

func run(args []string) int {
	if len(args) == 0 {
		return fail("BAD_ARGS", rootUsage)
	}
	switch args[0] {
	case "daemon":
		return daemonCmd(args[1:])
	case "init":
		return initCmd(args[1:])
	case "inbox":
		return inboxCmd(args[1:])
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

type apiResponseCheck func() error

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

func apiGet(u, agentID string, out any, check apiResponseCheck) int {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	if agentID != "" {
		req.Header.Set("X-Fluffle-Agent", agentID)
	}
	resp, err := client.NewHTTPClient().Do(req)
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return printAPIError(resp)
	}
	if out == nil {
		return 0
	}
	if err := decodeStrictJSON(resp.Body, out); err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	if check != nil {
		if err := check(); err != nil {
			return fail("DAEMON_ERROR", err.Error())
		}
	}
	return 0
}

func decodeStrictJSON(r io.Reader, out any) error {
	decoder := json.NewDecoder(r)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("null JSON response")
	}
	return json.Unmarshal(raw, out)
}

func apiPost(u, agentID string, payload any, out any, check apiResponseCheck) int {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fail("BAD_REQUEST", err.Error())
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return fail("BAD_REQUEST", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	if agentID != "" {
		req.Header.Set("X-Fluffle-Agent", agentID)
	}
	resp, err := client.NewHTTPClient().Do(req)
	if err != nil {
		if client.IsPreDispatchError(err) {
			return fail("DAEMON_DOWN", err.Error())
		}
		return fail("DELIVERY_UNKNOWN", fmt.Sprintf("read the thread before retrying: %v", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return printAPIError(resp)
	}
	if out == nil {
		return 0
	}
	if err := decodeStrictJSON(resp.Body, out); err != nil {
		return fail("DELIVERY_UNKNOWN", fmt.Sprintf("read the thread before retrying: %v", err))
	}
	if check != nil {
		if err := check(); err != nil {
			return fail("DELIVERY_UNKNOWN", fmt.Sprintf("read the thread before retrying: %v", err))
		}
	}
	return 0
}

func validateList[T any](list *[]T, check func(T) error) apiResponseCheck {
	return func() error {
		for i, item := range *list {
			if err := check(item); err != nil {
				return fmt.Errorf("entry %d: %w", i, err)
			}
		}
		return nil
	}
}

func validateChannels(list *[]store.Channel) apiResponseCheck {
	return validateList(list, func(channel store.Channel) error {
		if channel.ID <= 0 {
			return fmt.Errorf("channel has invalid id %d", channel.ID)
		}
		if strings.TrimSpace(channel.Name) == "" {
			return errors.New("channel has an empty name")
		}
		return nil
	})
}

func validateThreads(list *[]store.Thread) apiResponseCheck {
	return validateList(list, func(thread store.Thread) error {
		if thread.ID <= 0 || thread.ChannelID <= 0 {
			return fmt.Errorf("thread has invalid identity %d/%d", thread.ID, thread.ChannelID)
		}
		if strings.TrimSpace(thread.Title) == "" {
			return errors.New("thread has an empty title")
		}
		return nil
	})
}

func validateMessages(list *[]store.Message) apiResponseCheck {
	return validateList(list, func(message store.Message) error {
		if message.ID <= 0 || message.ThreadID <= 0 || message.Seq <= 0 {
			return fmt.Errorf("message has invalid reference %d/%d/%d", message.ID, message.ThreadID, message.Seq)
		}
		if strings.TrimSpace(message.Name) == "" || strings.TrimSpace(message.Content) == "" {
			return errors.New("message has an empty name or content")
		}
		if message.AuthorType != "human" && message.AuthorType != "agent" {
			return fmt.Errorf("message has unknown author_type %q", message.AuthorType)
		}
		return nil
	})
}

func validateReactions(list *[]store.Reaction) apiResponseCheck {
	return validateList(list, func(reaction store.Reaction) error {
		if reaction.ID <= 0 || reaction.MessageID <= 0 || reaction.MessageSeq <= 0 {
			return fmt.Errorf("reaction has invalid reference %d/%d/%d", reaction.ID, reaction.MessageID, reaction.MessageSeq)
		}
		if strings.TrimSpace(reaction.Emoji) == "" || strings.TrimSpace(reaction.Name) == "" {
			return errors.New("reaction has an empty emoji or name")
		}
		return nil
	})
}

func validateInbox(list *[]store.InboxMessage) apiResponseCheck {
	return validateList(list, func(entry store.InboxMessage) error {
		if entry.ID <= 0 || entry.ThreadID <= 0 || entry.Seq <= 0 || entry.ChannelID <= 0 {
			return fmt.Errorf("inbox entry has invalid reference %d/%d/%d/%d", entry.ID, entry.ThreadID, entry.Seq, entry.ChannelID)
		}
		if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Content) == "" {
			return errors.New("inbox entry has an empty name or content")
		}
		if strings.TrimSpace(entry.ChannelName) == "" || strings.TrimSpace(entry.ThreadTitle) == "" {
			return errors.New("inbox entry has an empty channel name or thread title")
		}
		return nil
	})
}

func validateCreatedID(id *int64) apiResponseCheck {
	return func() error {
		if *id <= 0 {
			return errors.New("daemon acknowledged creation without an id")
		}
		return nil
	}
}

func validateAssignedSeq(seq *int64) apiResponseCheck {
	return func() error {
		if *seq <= 0 {
			return errors.New("daemon acknowledged a message without an assigned sequence")
		}
		return nil
	}
}

type reactionAck struct {
	OK *bool `json:"ok"`
}

func validateReactionAck(ack *reactionAck) apiResponseCheck {
	return func() error {
		if ack.OK == nil {
			return errors.New("reaction response did not acknowledge the write")
		}
		if !*ack.OK {
			return errors.New("reaction response reported failure")
		}
		return nil
	}
}

func validateBatchResults(events []jsonl.Line, results *batchResults) apiResponseCheck {
	return func() error {
		if len(*results) != len(events) {
			return fmt.Errorf("batch returned %d results for %d events", len(*results), len(events))
		}
		for i, result := range *results {
			if result.MessageID <= 0 {
				return fmt.Errorf("batch result %d has no message id", i)
			}
			if events[i].Type == "reaction" {
				if result.ReactionID <= 0 {
					return fmt.Errorf("batch result %d has no reaction id", i)
				}
				continue
			}
			if result.Seq <= 0 {
				return fmt.Errorf("batch result %d has no assigned sequence", i)
			}
		}
		return nil
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
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

func inboxCmd(args []string) int {
	fs := newFlagSet("inbox")
	limit := fs.Int("limit", 100, "maximum messages")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	var messages []store.InboxMessage
	u := base + "/v1/inbox?limit=" + strconv.Itoa(*limit)
	if code := apiGet(u, "", &messages, validateInbox(&messages)); code != 0 {
		return code
	}
	if messages == nil {
		messages = []store.InboxMessage{}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(messages)
		return 0
	}
	for _, message := range messages {
		fmt.Printf("%s/%d %s/%d seq %d %s: %s\n", message.ChannelName, message.ChannelID, message.ThreadTitle, message.ThreadID, message.Seq, message.Name, message.Content)
	}
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
	if code := apiGet(u, "", &list, validateChannels(&list)); code != 0 {
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
		return fail("BAD_ARGS", "usage: flf channel create --name N [--repo PATH | --orphaned] [--json]")
	}
	if *orphaned && *repoPath != "" {
		return fail("BAD_ARGS", "use --orphaned or --repo, not both")
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
	if code := apiPost(base+"/v1/channels", "", body, &out, validateCreatedID(&out.ID)); code != 0 {
		return code
	}
	if *jsonOut {
		var list []store.Channel
		u := base + "/v1/channels?include-orphaned=1"
		if code := apiGet(u, "", &list, validateChannels(&list)); code != 0 {
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
	if code := apiGet(u, "", &list, validateChannels(&list)); code != 0 {
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
	if code := apiGet(base+"/v1/channels/"+strconv.FormatInt(id, 10)+"/threads", "", &list, validateThreads(&list)); code != 0 {
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
	if code := apiPost(base+"/v1/channels/"+strconv.FormatInt(id, 10)+"/threads", "", body, &out, validateCreatedID(&out.ID)); code != 0 {
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

func threadMessagesURL(base string, threadID int64, last int, afterSeq int64) string {
	u := base + "/v1/threads/" + strconv.FormatInt(threadID, 10) + "/messages"
	query := url.Values{}
	if afterSeq >= 0 {
		query.Set("after_seq", strconv.FormatInt(afterSeq, 10))
	} else if last > 0 {
		query.Set("last", strconv.Itoa(last))
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

func dumpThreadMessages(base string, threadID int64, last int, afterSeq int64, agentID string) int {
	var msgs []store.Message
	if code := apiGet(threadMessagesURL(base, threadID, last, afterSeq), agentID, &msgs, validateMessages(&msgs)); code != 0 {
		return code
	}
	seqByID := make(map[int64]int64, len(msgs))
	for _, message := range msgs {
		seqByID[message.ID] = message.Seq
	}
	if afterSeq > 0 || last > 0 {
		var allMessages []store.Message
		if code := apiGet(threadMessagesURL(base, threadID, 0, -1), agentID, &allMessages, validateMessages(&allMessages)); code != 0 {
			return code
		}
		for _, message := range allMessages {
			seqByID[message.ID] = message.Seq
		}
	}
	lines := make([]jsonl.Line, 0, len(msgs))
	for _, message := range msgs {
		lines = append(lines, jsonl.Line{
			Type:       "message",
			Seq:        message.Seq,
			ParentSeq:  seqByID[message.ParentIDValue()],
			Role:       message.Role,
			Name:       message.Name,
			AuthorType: message.AuthorType,
			Content:    message.Content,
			Timestamp:  message.CreatedAt,
			Metadata:   map[string]any{},
		})
	}
	var reactions []store.Reaction
	if code := apiGet(base+"/v1/threads/"+strconv.FormatInt(threadID, 10)+"/reactions", agentID, &reactions, validateReactions(&reactions)); code != 0 {
		return code
	}
	for _, reaction := range reactions {
		messageSeq := reaction.MessageSeq
		if messageSeq == 0 {
			messageSeq = seqByID[reaction.MessageID]
		}
		lines = append(lines, jsonl.Line{
			Type:       "reaction",
			MessageSeq: messageSeq,
			Name:       reaction.Name,
			AuthorType: reaction.AuthorType,
			Emoji:      reaction.Emoji,
			Timestamp:  reaction.CreatedAt,
			Metadata:   map[string]any{},
		})
	}
	os.Stdout.Write(jsonl.EncodeLines(lines))
	return 0
}

func parseJSONLFile(path string) ([]jsonl.Line, int) {
	var (
		raw []byte
		err error
	)
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
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
	return postJSONLBatch(base, threadID, lines, agentID, false)
}

type batchResults []store.AppendResult

func (results *batchResults) UnmarshalJSON(data []byte) error {
	var rawResults []json.RawMessage
	if err := json.Unmarshal(data, &rawResults); err != nil {
		return err
	}
	if rawResults == nil {
		return fmt.Errorf("batch results must be an array")
	}
	parsed := make([]store.AppendResult, 0, len(rawResults))
	for i, raw := range rawResults {
		var wire struct {
			SourceSeq  *int64 `json:"SourceSeq"`
			Seq        *int64 `json:"Seq"`
			MessageID  *int64 `json:"MessageID"`
			ReactionID *int64 `json:"ReactionID"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return fmt.Errorf("batch result %d: %w", i, err)
		}
		if wire.SourceSeq == nil || wire.Seq == nil || wire.MessageID == nil || wire.ReactionID == nil {
			return fmt.Errorf("batch result %d missing required field", i)
		}
		parsed = append(parsed, store.AppendResult{
			SourceSeq:  *wire.SourceSeq,
			Seq:        *wire.Seq,
			MessageID:  *wire.MessageID,
			ReactionID: *wire.ReactionID,
		})
	}
	*results = parsed
	return nil
}

func postJSONLBatch(base string, threadID int64, lines []jsonl.Line, agentID string, importEvents bool) int {
	u := base + "/v1/threads/" + strconv.FormatInt(threadID, 10) + "/events"
	body := struct {
		Events []jsonl.Line `json:"events"`
		Import bool         `json:"import"`
	}{Events: lines, Import: importEvents}
	var out batchResults
	return apiPost(u, agentID, body, &out, validateBatchResults(lines, &out))
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
	afterSeq := fs.Int64("after-seq", -1, "return messages after this sequence")
	agentID := fs.String("agent-id", "", "agent id")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *threadID == 0 {
		return fail("BAD_ARGS", "usage: flf agent read --thread ID [--last N | --after-seq N] [--agent-id ID] [--json]")
	}
	if flagWasSet(fs, "after-seq") && *afterSeq < 0 {
		return fail("BAD_ARGS", "after-seq must be a non-negative integer")
	}
	if flagWasSet(fs, "after-seq") && flagWasSet(fs, "last") {
		return fail("BAD_ARGS", "after-seq and last are mutually exclusive")
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	if *jsonOut {
		return dumpThreadMessages(base, *threadID, *last, *afterSeq, *agentID)
	}
	var msgs []store.Message
	if code := apiGet(threadMessagesURL(base, *threadID, *last, *afterSeq), *agentID, &msgs, validateMessages(&msgs)); code != 0 {
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
	return dumpThreadMessages(base, *threadID, 0, -1, "")
}

func ensureImportChannel(base, name, repoPath string, orphaned bool) (int64, int) {
	if orphaned {
		var list []store.Channel
		if code := apiGet(base+"/v1/channels?include-orphaned=1", "", &list, validateChannels(&list)); code != 0 {
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
		if code := apiPost(base+"/v1/channels", "", body, &out, validateCreatedID(&out.ID)); code != 0 {
			return 0, code
		}
		return out.ID, 0
	}
	abs, err := repo.Canonicalize(repoPath)
	if err != nil {
		return 0, fail("NOT_A_GIT_REPO", repoPath)
	}
	var list []store.Channel
	if code := apiGet(base+"/v1/channels?repo="+url.QueryEscape(abs), "", &list, validateChannels(&list)); code != 0 {
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
	if code := apiPost(base+"/v1/channels", "", body, &out, validateCreatedID(&out.ID)); code != 0 {
		return 0, code
	}
	return out.ID, 0
}

func threadImportCmd(args []string) int {
	fs := newFlagSet("thread import")
	file := fs.String("file", "", "JSONL file to import")
	threadID := fs.Int64("thread", 0, "existing destination thread id")
	channel := fs.String("channel", "", "channel name")
	repoPath := fs.String("repo", "", "repo path")
	orphaned := fs.Bool("orphaned", false, "import into an orphaned channel")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if flagWasSet(fs, "thread") && *threadID <= 0 {
		return fail("BAD_ARGS", "thread must be a positive integer")
	}
	if *file == "" {
		return fail("BAD_ARGS", "thread import requires --file F")
	}
	if *threadID == 0 && *channel == "" {
		return fail("BAD_ARGS", "thread import requires --thread ID or --channel NAME")
	}
	if *threadID > 0 && (*channel != "" || *repoPath != "" || *orphaned) {
		return fail("BAD_ARGS", "thread cannot be combined with channel import options")
	}
	if *threadID == 0 && *orphaned && *repoPath != "" {
		return fail("BAD_ARGS", "use --orphaned or --repo, not both")
	}
	lines, code := parseJSONLFile(*file)
	if code != 0 {
		return code
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	if *threadID > 0 {
		if code := postJSONLBatch(base, *threadID, lines, "", true); code != 0 {
			return code
		}
		fmt.Printf("thread %d\n", *threadID)
		return 0
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
	if code := apiPost(base+"/v1/channels/"+strconv.FormatInt(chID, 10)+"/threads", "", body, &out, validateCreatedID(&out.ID)); code != 0 {
		return code
	}
	if code := postJSONLBatch(base, out.ID, lines, "", true); code != 0 {
		return code
	}
	fmt.Printf("thread %d\n", out.ID)
	return 0
}

func messageCmd(args []string) int {
	if len(args) == 0 || args[0] != "send" {
		return fail("BAD_ARGS", "usage: flf message send --thread ID (--text T | --text -) [--reply-to ID | --reply-to-seq N] [--as NAME] [--agent-id ID] [--created-at TS] [--json]")
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
	text := fs.String("text", "", "message text or - for stdin")
	as := fs.String("as", "", "name (default $USER)")
	agentID := fs.String("agent-id", "", "agent id")
	replyTo := fs.Int64("reply-to", 0, "parent message id for threaded reply")
	replyToSeq := fs.Int64("reply-to-seq", -1, "parent message sequence for threaded reply")
	createdAt := fs.String("created-at", "", "message timestamp (RFC3339, e.g. 2025-01-15T10:30:00Z)")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *threadID == 0 {
		return fail("BAD_ARGS", "usage: flf message send --thread ID (--text T | --text -) [--reply-to ID | --reply-to-seq N] [--as NAME] [--agent-id ID] [--created-at TS] [--json]")
	}
	if flagWasSet(fs, "reply-to-seq") && *replyToSeq <= 0 {
		return fail("BAD_ARGS", "reply-to-seq must be a positive integer")
	}
	if flagWasSet(fs, "reply-to") && *replyToSeq > 0 {
		return fail("BAD_ARGS", "reply-to and reply-to-seq are mutually exclusive")
	}
	textValue := *text
	if textValue == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fail("FILE_READ", err.Error())
		}
		textValue = string(raw)
	}
	if textValue == "" {
		return fail("BAD_ARGS", "usage: flf message send --thread ID (--text T | --text -) [--reply-to ID | --reply-to-seq N] [--as NAME] [--agent-id ID] [--created-at TS] [--json]")
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	name := defaultName(*as)
	body := map[string]any{"Name": name, "Role": "user", "Content": textValue, "AgentID": *agentID}
	if *replyTo != 0 {
		body["ParentID"] = *replyTo
	} else if *replyToSeq > 0 {
		body["parent_seq"] = *replyToSeq
	}
	if *createdAt != "" {
		body["CreatedAt"] = *createdAt
	}
	var out struct {
		Seq int64 `json:"seq"`
	}
	u := base + "/v1/threads/" + strconv.FormatInt(*threadID, 10) + "/messages"
	if code := apiPost(u, *agentID, body, &out, validateAssignedSeq(&out.Seq)); code != 0 {
		return code
	}
	if *jsonOut {
		output := map[string]any{"seq": out.Seq, "thread_id": *threadID, "name": name, "content": textValue, "parent_id": *replyTo}
		if *replyToSeq > 0 {
			output["parent_seq"] = *replyToSeq
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(output)
		return 0
	}
	fmt.Printf("seq %d\n", out.Seq)
	return 0
}

func reactCmd(args []string) int {
	if len(args) == 0 || args[0] != "add" {
		return fail("BAD_ARGS", "usage: flf react add (--message ID | --thread ID --message-seq N) --emoji E [--as NAME] [--agent-id ID]")
	}
	return reactAddCmd(args[1:])
}

func reactAddCmd(args []string) int {
	fs := newFlagSet("react add")
	threadID := fs.Int64("thread", 0, "thread id")
	messageID := fs.Int64("message", 0, "message id")
	messageSeq := fs.Int64("message-seq", -1, "message sequence")
	emoji := fs.String("emoji", "", "emoji")
	as := fs.String("as", "", "name (default $USER)")
	agentID := fs.String("agent-id", "", "agent id")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if flagWasSet(fs, "message-seq") && *messageSeq <= 0 {
		return fail("BAD_ARGS", "message-seq must be a positive integer")
	}
	if *messageSeq > 0 && *threadID == 0 {
		return fail("BAD_ARGS", "--thread is required with --message-seq")
	}
	if *messageSeq > 0 && flagWasSet(fs, "message") {
		return fail("BAD_ARGS", "message and message-seq are mutually exclusive")
	}
	if *emoji == "" || (*messageID == 0 && *messageSeq <= 0) {
		return fail("BAD_ARGS", "usage: flf react add (--message ID | --thread ID --message-seq N) --emoji E [--as NAME] [--agent-id ID]")
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	name := defaultName(*as)
	body := map[string]any{"Emoji": *emoji, "Name": name, "AgentID": *agentID}
	var out reactionAck
	var u string
	if *messageSeq > 0 {
		u = base + "/v1/threads/" + strconv.FormatInt(*threadID, 10) + "/messages/" + strconv.FormatInt(*messageSeq, 10) + "/reactions"
	} else {
		u = base + "/v1/messages/" + strconv.FormatInt(*messageID, 10) + "/reactions"
	}
	if code := apiPost(u, *agentID, body, &out, validateReactionAck(&out)); code != 0 {
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

const (
	daemonStopRequestTimeout = 2 * time.Second
	daemonStopExitTimeout    = 5 * time.Second
	daemonStopPollInterval   = 25 * time.Millisecond
)

func daemonStop() int { return daemonStopWithin(daemonStopExitTimeout) }

func daemonStopWithin(exitTimeout time.Duration) int {
	home := client.FluffleHome()
	runtimeFile := filepath.Join(home, "daemon.json")
	b, err := os.ReadFile(runtimeFile)
	if err != nil {
		return fail("DAEMON_DOWN", "not running")
	}
	var df struct {
		PID  int `json:"pid"`
		Port int `json:"port"`
	}
	if err := json.Unmarshal(b, &df); err != nil {
		return fail("DAEMON_DOWN", "bad daemon.json")
	}
	if df.PID <= 0 {
		return fail("DAEMON_DOWN", "bad daemon.json")
	}
	proc, err := os.FindProcess(df.PID)
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	if !requestDaemonShutdown(df.Port) || !waitForProcessExit(df.PID, exitTimeout) {
		notice := fmt.Sprintf("daemon %d did not shut down gracefully; killing it", df.PID)
		if err := proc.Kill(); err != nil {
			os.Remove(runtimeFile)
			return fail("DAEMON_DOWN", notice+"; kill failed: "+err.Error())
		}
		fmt.Fprintln(os.Stderr, "flf: "+notice)
	}
	os.Remove(runtimeFile)
	fmt.Println("daemon stopped")
	return 0
}

// requestDaemonShutdown asks the daemon to run its own shutdown path, which is
// the only route that reaches the session barrier. The request is a
// human-initiated control operation, so it deliberately carries no
// X-Fluffle-Agent header; the route rejects agent callers with 403.
func requestDaemonShutdown(port int) bool {
	if port <= 0 {
		return false
	}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: daemonStopRequestTimeout}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// processAlive reports whether a pid still exists. os.FindProcess always
// succeeds on Unix, so the only usable probe is signal 0.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// waitForProcessExit blocks until the pid is gone, the deadline passes, or the
// pid is already dead on arrival. A killed-but-unreaped child still answers
// signal 0, so the daemon's own reaping is what makes this return.
func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !processAlive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(daemonStopPollInterval)
	}
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
	agents := agentcfg.NewLoader(filepath.Join(home, "config.toml"))
	sessions := session.NewManager(s, agents, runner.NewExec())
	if err := sessions.Reconcile(); err != nil {
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
	srv := &http.Server{Handler: apiserver.NewHandlerWithDeps(s, apiserver.Deps{
		Starter:  sessions,
		Agents:   agents,
		Canceler: sessions,
	})}
	apiserver.SetShutdown(func() {
		sessions.ShutdownAndWait()
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
	childExit := make(chan error, 1)
	go func() { childExit <- cmd.Wait() }()

	port, err := awaitDaemonPort(home, 50, 100*time.Millisecond, childExit, cmd.Process.Pid)
	if err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	fmt.Println("daemon started on 127.0.0.1:" + strconv.Itoa(port))
	return 0
}

func awaitDaemonPort(home string, attempts int, delay time.Duration, childExit <-chan error, childPID int) (int, error) {
	for i := 0; i < attempts; i++ {
		if data, err := os.ReadFile(filepath.Join(home, "daemon.json")); err == nil {
			var df struct {
				Port int `json:"port"`
				PID  int `json:"pid"`
			}
			if json.Unmarshal(data, &df) == nil && df.Port > 0 && childPID == df.PID {
				return df.Port, nil
			}
		}
		select {
		case err := <-childExit:
			if err == nil {
				err = errors.New("child exited before publishing its port")
			}
			return 0, fmt.Errorf("daemon child failed: %w", err)
		case <-time.After(delay):
		}
	}
	return 0, errors.New("daemon did not become ready with a published port")
}
