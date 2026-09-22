package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/NoRaincheck/fluffle/internal/client"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type apiClient struct {
	base string
	http *http.Client
}

func NewAPIClient(base string) *apiClient {
	return &apiClient{base: base, http: &http.Client{}}
}

func (c *apiClient) EnsureDaemon() error {
	base, err := client.EnsureDaemon()
	if err != nil {
		return fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	c.base = base
	return nil
}

func (c *apiClient) ListChannels(ctx context.Context, repo, filter string) ([]store.Channel, error) {
	url := c.base + "/v1/channels?include-orphaned=1"
	if repo != "" {
		url += "&repo=" + repo
	}
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var channels []store.Channel
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	if filter != "" {
		channels = filterChannels(channels, filter)
	}
	return channels, nil
}

func (c *apiClient) CreateChannel(ctx context.Context, name, repo, branch string, orphaned bool) (int64, error) {
	body := map[string]any{"Name": name, "Orphaned": orphaned}
	if !orphaned {
		body["RepoAbsPath"] = repo
		body["RepoHeadBranch"] = branch
	}
	return c.doJSON(c.base+"/v1/channels", "POST", body)
}

func (c *apiClient) ListThreads(ctx context.Context, channelID int64) ([]store.Thread, error) {
	url := c.base + "/v1/channels/" + fmt.Sprintf("%d", channelID) + "/threads"
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var threads []store.Thread
	if err := json.NewDecoder(resp.Body).Decode(&threads); err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	return threads, nil
}

func (c *apiClient) CreateThread(ctx context.Context, channelID int64, title string) (int64, error) {
	return c.doJSON(c.base+"/v1/channels/"+fmt.Sprintf("%d", channelID)+"/threads", "POST", map[string]any{"Title": title})
}

func (c *apiClient) ListMessages(ctx context.Context, threadID int64) ([]store.Message, error) {
	url := c.base + "/v1/threads/" + fmt.Sprintf("%d", threadID) + "/messages"
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var msgs []store.Message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	return msgs, nil
}

func (c *apiClient) SendMessage(ctx context.Context, threadID, parentID int64, text string) error {
	body := map[string]any{"Author": "you", "Role": "user", "Content": text, "ParentID": parentID}
	resp, err := c.http.Post(c.base+"/v1/threads/"+fmt.Sprintf("%d", threadID)+"/messages", "application/json", jsonBody(body))
	if err != nil {
		return fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readAPIError(resp)
	}
	return nil
}

func (c *apiClient) AddReaction(ctx context.Context, messageID int64, emoji string) error {
	body := map[string]any{"Emoji": emoji, "Author": "you"}
	resp, err := c.http.Post(c.base+"/v1/messages/"+fmt.Sprintf("%d", messageID)+"/reactions", "application/json", jsonBody(body))
	if err != nil {
		return fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readAPIError(resp)
	}
	return nil
}

func (c *apiClient) doJSON(url, method string, body any) (int64, error) {
	resp, err := c.http.Post(url, "application/json", jsonBody(body))
	if err != nil {
		return 0, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, readAPIError(resp)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	return out.ID, nil
}

func jsonBody(v any) *bytes.Reader {
	raw, _ := json.Marshal(v)
	return bytes.NewReader(raw)
}

func readAPIError(resp *http.Response) error {
	var eb struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&eb); err != nil {
		return fmt.Errorf("DAEMON_DOWN: status %d", resp.StatusCode)
	}
	return fmt.Errorf("%s: %s", eb.Code, eb.Message)
}

func filterChannels(channels []store.Channel, filter string) []store.Channel {
	if filter == "" {
		return channels
	}
	filter = strings.ToLower(filter)
	out := make([]store.Channel, 0, len(channels))
	for _, c := range channels {
		if strings.Contains(strings.ToLower(c.RepoAbsPath), filter) ||
			strings.Contains(strings.ToLower(c.Name), filter) {
			out = append(out, c)
		}
	}
	return out
}
