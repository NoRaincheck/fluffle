package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

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

// get performs a GET and decodes a JSON array into T. A body of 4xx or 5xx
// surfaces as the envelope's code and message, a null or undecodable body as
// DAEMON_ERROR. A decodable array always yields a non-nil slice, so a caller
// never has to handle nil; a daemon that would serialise null must normalise it
// on its own side, where the empty result is produced.
func get[T any](c *apiClient, ctx context.Context, url string) ([]T, error) {
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var out []T
	if err := decodeStrictJSON(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("DAEMON_ERROR: %w", err)
	}
	return out, nil
}

func (c *apiClient) ListRows(ctx context.Context, granularity string, limit int) ([]store.Row, error) {
	if limit <= 0 {
		limit = 200
	}
	url := fmt.Sprintf("%s/v1/rows?g=%s&limit=%d", c.base, granularity, limit)
	return get[store.Row](c, ctx, url)
}

func (c *apiClient) ListMessages(ctx context.Context, threadID int64) ([]store.Message, error) {
	url := c.base + "/v1/threads/" + strconv.FormatInt(threadID, 10) + "/messages"
	return get[store.Message](c, ctx, url)
}

// SendReply appends to a thread. The TUI never sets parent_id: a reply is a
// message in the thread, not a nested answer.
func (c *apiClient) SendReply(ctx context.Context, threadID int64, text string) error {
	body := map[string]any{"Name": "you", "Role": "user", "Content": text}
	resp, err := c.do(ctx, http.MethodPost,
		c.base+"/v1/threads/"+strconv.FormatInt(threadID, 10)+"/messages", jsonBody(body))
	if err != nil {
		return mutationTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readAPIError(resp)
	}
	var ack struct {
		Seq int64 `json:"seq"`
	}
	if err := decodeStrictJSON(resp.Body, &ack); err != nil {
		return mutationTransportError(err)
	}
	if ack.Seq <= 0 {
		return fmt.Errorf("DELIVERY_UNKNOWN: message acknowledgement without an assigned sequence")
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

func mutationTransportError(err error) error {
	if client.IsPreDispatchError(err) {
		return fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	return fmt.Errorf("DELIVERY_UNKNOWN: %w", err)
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
