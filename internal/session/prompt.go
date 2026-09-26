package session

import (
	"fmt"
	"strings"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type promptInput struct {
	Agent         agentcfg.Entry
	Thread        store.ThreadContext
	History       []jsonl.Line
	Trigger       store.Message
	NeedsReplying bool
}

func buildPrompt(in promptInput) (string, error) {
	var b strings.Builder
	if sys := strings.TrimSpace(in.Agent.SystemPrompt); sys != "" {
		b.WriteString(sys)
		b.WriteString("\n\n")
	}
	repo := "none"
	if in.Thread.RepoAbsPath != nil && *in.Thread.RepoAbsPath != "" {
		repo = *in.Thread.RepoAbsPath
	}
	fmt.Fprintf(&b, "## Thread\n\n%s > %s\nrepository: %s\n\n", in.Thread.ChannelName, in.Thread.ThreadTitle, repo)

	b.WriteString("## History\n\n")
	if len(in.History) == 0 {
		b.WriteString("(empty)\n")
	}
	for _, l := range in.History {
		encoded, err := jsonl.MarshalLine(l)
		if err != nil {
			return "", err
		}
		b.WriteString(encoded)
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "\n## Request\n\n#%d\n%s\n", in.Trigger.Seq, in.Trigger.Content)

	if in.NeedsReplying {
		fmt.Fprintf(&b, `
## Replying

You are agent %q in fluffle thread %d.
Post your reply with:

    flf message send --thread %d --reply-to-seq %d --text "<your reply>" --agent-id %s
`, in.Agent.Name, in.Thread.ThreadID, in.Thread.ThreadID, in.Trigger.Seq, in.Agent.Name)
		if in.Agent.Reply == "auto" {
			b.WriteString("\nIf you do not post, whatever you write to stdout is posted for you.\n")
		}
	}
	return b.String(), nil
}

func deliverPrompt(args []string, prompt string) ([]string, string) {
	hasPlaceholder := false
	out := make([]string, len(args))
	for i, a := range args {
		if strings.Contains(a, "{prompt}") {
			hasPlaceholder = true
			out[i] = strings.ReplaceAll(a, "{prompt}", prompt)
			continue
		}
		out[i] = a
	}
	if hasPlaceholder {
		return out, ""
	}
	return out, prompt
}

func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
