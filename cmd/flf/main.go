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
	"path/filepath"
	"strconv"
	"time"

	"github.com/NoRaincheck/fluffle/internal/apiserver"
	"github.com/NoRaincheck/fluffle/internal/client"
	"github.com/NoRaincheck/fluffle/internal/repo"
	"github.com/NoRaincheck/fluffle/internal/store"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: flf <daemon|init|channel|thread|message|react|tui>")
		return 1
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
	case "tui":
		fmt.Fprintln(os.Stderr, "backend-only milestone: tui deferred")
		return 1
	default:
		fmt.Fprintln(os.Stderr, "unknown command (backend slice implements daemon + tui stub; rest in Task 7-8)")
		return 1
	}
}

type apiErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func printAPIError(resp *http.Response) int {
	body, _ := io.ReadAll(resp.Body)
	var eb apiErrBody
	if err := json.Unmarshal(body, &eb); err != nil || eb.Code == "" {
		fmt.Fprintf(os.Stderr, "DAEMON_DOWN: status %d\n", resp.StatusCode)
		return 1
	}
	fmt.Fprintf(os.Stderr, "%s: %s\n", eb.Code, eb.Message)
	return 1
}

func apiGet(u, agentID string, out any) int {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	if agentID != "" {
		req.Header.Set("X-Fluffle-Agent", agentID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return printAPIError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
			return 2
		}
	}
	return 0
}

func apiPost(u, agentID string, payload any, out any) int {
	raw, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	req.Header.Set("Content-Type", "application/json")
	if agentID != "" {
		req.Header.Set("X-Fluffle-Agent", agentID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return printAPIError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
			return 2
		}
	}
	return 0
}

func initCmd(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "repo directory (default: cwd)")
	orphaned := fs.Bool("orphaned", false, "allow initializing outside a git repo")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	dir := *repoFlag
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "NOT_A_GIT_REPO:", err)
			return 1
		}
		dir = cwd
	}
	abs, err := repo.Canonicalize(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NOT_A_GIT_REPO: %s is not a git repo (suggest --orphaned)\n", dir)
		return 1
	}
	_, _, isGit := repo.InspectGitDir(abs)
	if !isGit && !*orphaned {
		fmt.Fprintf(os.Stderr, "NOT_A_GIT_REPO: %s is not a git repo (suggest --orphaned)\n", abs)
		return 1
	}
	fmt.Printf("initialized %s\n", abs)
	return 0
}

func channelCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: flf channel <list|create>")
		return 1
	}
	switch args[0] {
	case "list":
		return channelListCmd(args[1:])
	case "create":
		return channelCreateCmd(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: flf channel <list|create>")
		return 1
	}
}

func channelListCmd(args []string) int {
	fs := flag.NewFlagSet("channel list", flag.ContinueOnError)
	repoPath := fs.String("repo", "", "repo path")
	includeOrphaned := fs.Bool("include-orphaned", false, "include orphaned channels")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *repoPath == "" {
		fmt.Fprintln(os.Stderr, "usage: flf channel list --repo PATH [--include-orphaned]")
		return 1
	}
	abs, err := repo.Canonicalize(*repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NOT_A_GIT_REPO: %s\n", *repoPath)
		return 1
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	inc := "0"
	if *includeOrphaned {
		inc = "1"
	}
	var list []store.Channel
	u := base + "/v1/channels?repo=" + url.QueryEscape(abs) + "&include-orphaned=" + inc
	if code := apiGet(u, "", &list); code != 0 {
		return code
	}
	for _, c := range list {
		fmt.Printf("%d %s\n", c.ID, c.Name)
	}
	return 0
}

func channelCreateCmd(args []string) int {
	fs := flag.NewFlagSet("channel create", flag.ContinueOnError)
	name := fs.String("name", "", "channel name")
	repoPath := fs.String("repo", "", "repo path")
	orphaned := fs.Bool("orphaned", false, "create orphaned channel")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "usage: flf channel create --name N (--repo PATH | --orphaned)")
		return 1
	}
	var body map[string]any
	if *orphaned {
		body = map[string]any{"Name": *name, "Orphaned": true}
	} else {
		if *repoPath == "" {
			fmt.Fprintln(os.Stderr, "usage: flf channel create --name N (--repo PATH | --orphaned)")
			return 1
		}
		abs, err := repo.Canonicalize(*repoPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "NOT_A_GIT_REPO: %s is not a git repo (suggest --orphaned)\n", *repoPath)
			return 1
		}
		remote, head, isGit := repo.InspectGitDir(abs)
		if !isGit {
			fmt.Fprintf(os.Stderr, "NOT_A_GIT_REPO: %s is not a git repo (suggest --orphaned)\n", abs)
			return 1
		}
		body = map[string]any{"Name": *name, "RepoAbsPath": abs, "RepoRemote": remote, "RepoHeadSHA": head, "Orphaned": false}
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if code := apiPost(base+"/v1/channels", "", body, &out); code != 0 {
		return code
	}
	fmt.Printf("channel %d\n", out.ID)
	return 0
}

func threadCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: flf thread <list|new>")
		return 1
	}
	switch args[0] {
	case "list":
		return threadListCmd(args[1:])
	case "new":
		return threadNewCmd(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: flf thread <list|new>")
		return 1
	}
}

func resolveChannelID(base, abs, name string) (int64, int) {
	var list []store.Channel
	u := base + "/v1/channels?repo=" + url.QueryEscape(abs)
	if code := apiGet(u, "", &list); code != 0 {
		return 0, code
	}
	for _, c := range list {
		if c.Name == name {
			return c.ID, 0
		}
	}
	fmt.Fprintf(os.Stderr, "CHANNEL_NOT_FOUND: %s\n", name)
	return 0, 1
}

func threadListCmd(args []string) int {
	fs := flag.NewFlagSet("thread list", flag.ContinueOnError)
	channel := fs.String("channel", "", "channel name")
	repoPath := fs.String("repo", "", "repo path")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *channel == "" || *repoPath == "" {
		fmt.Fprintln(os.Stderr, "usage: flf thread list --channel NAME --repo PATH")
		return 1
	}
	abs, err := repo.Canonicalize(*repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NOT_A_GIT_REPO: %s\n", *repoPath)
		return 1
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	id, code := resolveChannelID(base, abs, *channel)
	if code != 0 {
		return code
	}
	var list []store.Thread
	if code := apiGet(base+"/v1/channels/"+strconv.FormatInt(id, 10)+"/threads", "", &list); code != 0 {
		return code
	}
	for _, th := range list {
		fmt.Printf("%d %s\n", th.ID, th.Title)
	}
	return 0
}

func threadNewCmd(args []string) int {
	fs := flag.NewFlagSet("thread new", flag.ContinueOnError)
	channel := fs.String("channel", "", "channel name")
	repoPath := fs.String("repo", "", "repo path")
	title := fs.String("title", "", "thread title")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *channel == "" || *repoPath == "" || *title == "" {
		fmt.Fprintln(os.Stderr, "usage: flf thread new --channel NAME --repo PATH --title T")
		return 1
	}
	abs, err := repo.Canonicalize(*repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NOT_A_GIT_REPO: %s\n", *repoPath)
		return 1
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	id, code := resolveChannelID(base, abs, *channel)
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
	fmt.Printf("thread %d\n", out.ID)
	return 0
}

func messageCmd(args []string) int {
	if len(args) == 0 || args[0] != "send" {
		fmt.Fprintln(os.Stderr, "usage: flf message send --thread ID --text T [--as NAME] [--agent-id ID]")
		return 1
	}
	return messageSendCmd(args[1:])
}

func defaultAuthor(as string) string {
	if as != "" {
		return as
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}

func messageSendCmd(args []string) int {
	fs := flag.NewFlagSet("message send", flag.ContinueOnError)
	threadID := fs.Int64("thread", 0, "thread id")
	text := fs.String("text", "", "message text")
	as := fs.String("as", "", "author name (default $USER)")
	agentID := fs.String("agent-id", "", "agent id")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *threadID == 0 || *text == "" {
		fmt.Fprintln(os.Stderr, "usage: flf message send --thread ID --text T [--as NAME] [--agent-id ID]")
		return 1
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	author := defaultAuthor(*as)
	body := map[string]any{"Author": author, "Role": "user", "Content": *text, "AgentID": *agentID}
	var out struct {
		Seq int64 `json:"seq"`
	}
	u := base + "/v1/threads/" + strconv.FormatInt(*threadID, 10) + "/messages"
	if code := apiPost(u, *agentID, body, &out); code != 0 {
		return code
	}
	fmt.Printf("seq %d\n", out.Seq)
	return 0
}

func reactCmd(args []string) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprintln(os.Stderr, "usage: flf react add --message ID --emoji E [--as NAME] [--agent-id ID]")
		return 1
	}
	return reactAddCmd(args[1:])
}

func reactAddCmd(args []string) int {
	fs := flag.NewFlagSet("react add", flag.ContinueOnError)
	messageID := fs.Int64("message", 0, "message id")
	emoji := fs.String("emoji", "", "emoji")
	as := fs.String("as", "", "author name (default $USER)")
	agentID := fs.String("agent-id", "", "agent id")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *messageID == 0 || *emoji == "" {
		fmt.Fprintln(os.Stderr, "usage: flf react add --message ID --emoji E [--as NAME] [--agent-id ID]")
		return 1
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	author := defaultAuthor(*as)
	body := map[string]any{"Emoji": *emoji, "Author": author, "AgentID": *agentID}
	var out map[string]any
	u := base + "/v1/messages/" + strconv.FormatInt(*messageID, 10) + "/reactions"
	if code := apiPost(u, *agentID, body, &out); code != 0 {
		return code
	}
	fmt.Println("ok")
	return 0
}

func daemonCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: flf daemon <start|stop|status>")
		return 1
	}
	switch args[0] {
	case "status":
		base, err := client.DaemonBaseURL()
		if err != nil {
			fmt.Fprintln(os.Stderr, "daemon down")
			return 2
		}
		fmt.Println("daemon up at", base)
		return 0
	case "start":
		background := len(args) > 1 && args[1] == "--background"
		return daemonStart(background)
	case "stop":
		return daemonStop()
	}
	return 1
}

func daemonStop() int {
	b, err := os.ReadFile(filepath.Join(client.FluffleHome(), "daemon.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN: not running")
		return 2
	}
	var df struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(b, &df); err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN: bad daemon.json")
		return 2
	}
	proc, err := os.FindProcess(df.PID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	if err := proc.Kill(); err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		os.Remove(filepath.Join(client.FluffleHome(), "daemon.json"))
		return 2
	}
	os.Remove(filepath.Join(client.FluffleHome(), "daemon.json"))
	fmt.Println("daemon stopped")
	return 0
}

func daemonStart(background bool) int {
	home := client.FluffleHome()
	if err := os.MkdirAll(home, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	dbPath := filepath.Join(home, "fluffle.db")
	s, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	port := ln.Addr().(*net.TCPAddr).Port
	payload, err := json.Marshal(map[string]any{"port": port, "pid": os.Getpid(), "started_at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	if err := os.WriteFile(filepath.Join(home, "daemon.json"), payload, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	srv := &http.Server{Handler: apiserver.NewHandler(s)}
	if !background {
		fmt.Println("fluffle daemon on 127.0.0.1:" + strconv.Itoa(port))
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	return 0
}
