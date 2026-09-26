package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/NoRaincheck/fluffle/internal/client"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type agentListItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Reply       string `json:"reply"`
	Source      string `json:"source"`
}

type agentListPayload struct {
	Agents []agentListItem `json:"agents"`
}

type sessionPayload struct {
	Session store.Session        `json:"session"`
	Events  []store.SessionEvent `json:"events"`
}

func validateAgents(payload *agentListPayload) apiResponseCheck {
	// The reply mode is not whitelisted: agentcfg already validates it, and a mode
	// this CLI has not heard of is forward-compatible, not a broken response.
	return func() error {
		for i, item := range payload.Agents {
			if item.Name == "" || item.Command == "" {
				return fmt.Errorf("agent %d has no name or command", i)
			}
			if item.Source == "" {
				return fmt.Errorf("agent %q has no source", item.Name)
			}
		}
		return nil
	}
}

func validateSession(payload *sessionPayload) apiResponseCheck {
	return func() error {
		sess := payload.Session
		if sess.ID <= 0 {
			return errors.New("session response has no id")
		}
		if sess.ThreadID <= 0 || sess.TriggerMessageID <= 0 {
			return fmt.Errorf("session %d has invalid references %d/%d", sess.ID, sess.ThreadID, sess.TriggerMessageID)
		}
		if sess.AgentName == "" || sess.Command == "" {
			return fmt.Errorf("session %d has an empty agent name or command", sess.ID)
		}
		switch sess.Status {
		case store.SessionQueued, store.SessionRunning, store.SessionSucceeded, store.SessionFailed, store.SessionCanceled:
		default:
			return fmt.Errorf("session %d has unknown status %q", sess.ID, sess.Status)
		}
		for i, event := range payload.Events {
			if event.SessionID != sess.ID || event.Seq <= 0 {
				return fmt.Errorf("event %d has invalid reference %d/%d", i, event.SessionID, event.Seq)
			}
			if event.Type == "" {
				return fmt.Errorf("event %d has no type", i)
			}
		}
		return nil
	}
}

func agentListCmd(args []string) int {
	fs := newFlagSet("agent list")
	repoPath := fs.String("repo", "", "repo path whose .flf.toml is resolved (default: global config only)")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	repo := ""
	if *repoPath != "" {
		abs, err := filepath.Abs(*repoPath)
		if err != nil {
			return fail("BAD_ARGS", err.Error())
		}
		if _, err := os.Stat(abs); err != nil {
			return fail("NOT_A_GIT_REPO", fmt.Sprintf("%s does not exist", *repoPath))
		}
		repo = abs
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	var payload agentListPayload
	u := base + "/v1/agents?repo=" + url.QueryEscape(repo)
	if code := apiGet(u, "", &payload, validateAgents(&payload)); code != 0 {
		return code
	}
	if payload.Agents == nil {
		payload.Agents = []agentListItem{}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(payload)
		return 0
	}
	for _, item := range payload.Agents {
		description := item.Description
		if description == "" {
			description = "-"
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", item.Name, item.Command, item.Reply, item.Source, description)
	}
	return 0
}

func agentSessionCmd(args []string) int {
	fs := newFlagSet("agent session")
	id := fs.Int64("id", 0, "session id")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *id <= 0 {
		return fail("BAD_ARGS", "usage: flf agent session --id N [--json]; --id must be a positive integer")
	}
	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	var payload sessionPayload
	u := base + "/v1/sessions/" + strconv.FormatInt(*id, 10)
	if code := apiGet(u, "", &payload, validateSession(&payload)); code != 0 {
		return code
	}
	if payload.Events == nil {
		payload.Events = []store.SessionEvent{}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(payload)
		return 0
	}
	sess := payload.Session
	status := sess.Status
	if sess.StartedAt != nil && sess.FinishedAt != nil {
		status = fmt.Sprintf("%s (%s -> %s)", sess.Status, *sess.StartedAt, *sess.FinishedAt)
	}
	fmt.Printf("session %d  agent=%s  thread=%d  trigger=%d  status=%s  reply=%s\n",
		sess.ID, sess.AgentName, sess.ThreadID, sess.TriggerMessageID, status, sess.ReplyMode)
	fmt.Printf("command: %s\n", sess.Command)
	if sess.ExitCode != nil {
		fmt.Printf("exit: %d\n", *sess.ExitCode)
	}
	if sess.Cwd != nil {
		fmt.Printf("cwd: %s\n", *sess.Cwd)
	} else {
		fmt.Println("cwd: none")
	}
	if sess.ReplyMessageID != nil {
		fmt.Printf("reply: message %d\n", *sess.ReplyMessageID)
	}
	if sess.Error != nil {
		fmt.Printf("error: %s\n", *sess.Error)
	}
	for _, event := range payload.Events {
		fmt.Printf("%d\t%s\t%s\n", event.Seq, event.Type, event.Content)
	}
	return 0
}
