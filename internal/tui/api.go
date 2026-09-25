package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return &apiClient{base: base, http: client.NewHTTPClient()}
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
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var channels []store.Channel
	if err := decodeStrictJSON(resp.Body, &channels); err != nil {
		return nil, fmt.Errorf("DAEMON_ERROR: %w", err)
	}
	if channels == nil {
		channels = []store.Channel{}
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
	return c.doJSON(ctx, c.base+"/v1/channels", http.MethodPost, body)
}

func (c *apiClient) ListThreads(ctx context.Context, channelID int64) ([]store.Thread, error) {
	url := c.base + "/v1/channels/" + fmt.Sprintf("%d", channelID) + "/threads"
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var threads []store.Thread
	if err := decodeStrictJSON(resp.Body, &threads); err != nil {
		return nil, fmt.Errorf("DAEMON_ERROR: %w", err)
	}
	if threads == nil {
		threads = []store.Thread{}
	}
	return threads, nil
}

func (c *apiClient) CreateThread(ctx context.Context, channelID int64, title string) (int64, error) {
	return c.doJSON(ctx, c.base+"/v1/channels/"+fmt.Sprintf("%d", channelID)+"/threads", http.MethodPost, map[string]any{"Title": title})
}

func (c *apiClient) ListMessages(ctx context.Context, threadID int64) ([]store.Message, error) {
	url := c.base + "/v1/threads/" + fmt.Sprintf("%d", threadID) + "/messages"
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var msgs []store.Message
	if err := decodeStrictJSON(resp.Body, &msgs); err != nil {
		return nil, fmt.Errorf("DAEMON_ERROR: %w", err)
	}
	if msgs == nil {
		msgs = []store.Message{}
	}
	return msgs, nil
}

func (c *apiClient) ListInbox(ctx context.Context, limit int) ([]store.InboxMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	url := fmt.Sprintf("%s/v1/inbox?limit=%d", c.base, limit)
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var msgs []store.InboxMessage
	if err := decodeStrictJSON(resp.Body, &msgs); err != nil {
		return nil, fmt.Errorf("DAEMON_ERROR: %w", err)
	}
	if msgs == nil {
		msgs = []store.InboxMessage{}
	}
	return msgs, nil
}

func (c *apiClient) SendMessage(ctx context.Context, threadID, parentID int64, text string) error {
	body := map[string]any{"Name": "you", "Role": "user", "Content": text, "ParentID": parentID}
	resp, err := c.do(ctx, http.MethodPost, c.base+"/v1/threads/"+fmt.Sprintf("%d", threadID)+"/messages", jsonBody(body))
	if err != nil {
		return fmt.Errorf("DELIVERY_UNKNOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readAPIError(resp)
	}
	var ack struct {
		Seq int64 `json:"seq"`
	}
	if err := decodeStrictJSON(resp.Body, &ack); err != nil {
		return fmt.Errorf("DELIVERY_UNKNOWN: %w", err)
	}
	if ack.Seq <= 0 {
		return fmt.Errorf("DELIVERY_UNKNOWN: message acknowledgement without an assigned sequence")
	}
	return nil
}

func (c *apiClient) AddReaction(ctx context.Context, messageID int64, emoji string) error {
	body := map[string]any{"Emoji": emoji, "Name": "you"}
	resp, err := c.do(ctx, http.MethodPost, c.base+"/v1/messages/"+fmt.Sprintf("%d", messageID)+"/reactions", jsonBody(body))
	if err != nil {
		return fmt.Errorf("DELIVERY_UNKNOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readAPIError(resp)
	}
	var ack struct {
		OK *bool `json:"ok"`
	}
	if err := decodeStrictJSON(resp.Body, &ack); err != nil {
		return fmt.Errorf("DELIVERY_UNKNOWN: %w", err)
	}
	if ack.OK == nil || !*ack.OK {
		return fmt.Errorf("DELIVERY_UNKNOWN: reaction response did not acknowledge the write")
	}
	return nil
}

func (c *apiClient) do(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *apiClient) doJSON(ctx context.Context, url, method string, body any) (int64, error) {
	resp, err := c.do(ctx, method, url, jsonBody(body))
	if err != nil {
		return 0, fmt.Errorf("DELIVERY_UNKNOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, readAPIError(resp)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := decodeStrictJSON(resp.Body, &out); err != nil {
		return 0, fmt.Errorf("DELIVERY_UNKNOWN: %w", err)
	}
	if out.ID <= 0 {
		return 0, fmt.Errorf("DELIVERY_UNKNOWN: creation acknowledged without an id")
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
	if err := json.NewDecoder(resp.Body).Decode(&eb); err != nil || eb.Code == "" {
		return fmt.Errorf("DAEMON_ERROR: status %d without a readable error envelope", resp.StatusCode)
	}
	return fmt.Errorf("%s: %s", eb.Code, eb.Message)
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
			return errors.New("multiple JSON values")
		}
		return err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null JSON response")
	}
	return json.Unmarshal(raw, out)
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
