// Package client integrates Selfmail with any Go project. HTTP idempotency is
// explicit; the SDK never generates a new key for a retry of the same operation.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"`
}
type SendRequest struct {
	From        string            `json:"from"`
	FromName    string            `json:"from_name,omitempty"`
	To          []string          `json:"to"`
	ReplyTo     string            `json:"reply_to,omitempty"`
	Subject     string            `json:"subject"`
	Text        string            `json:"text,omitempty"`
	HTML        string            `json:"html,omitempty"`
	Template    string            `json:"template,omitempty"`
	Variables   map[string]any    `json:"variables,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	Priority    string            `json:"priority,omitempty"`
	SendAt      time.Time         `json:"send_at,omitempty"`
	TTLSeconds  int               `json:"ttl_seconds,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}
type SendResult struct {
	BatchID    string   `json:"batch_id"`
	MessageIDs []string `json:"message_ids"`
	Replayed   bool     `json:"replayed"`
}
type Message struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Recipient string `json:"recipient"`
	LastError string `json:"last_error"`
	QueueID   string `json:"queue_id"`
}
type APIError struct {
	Status     int
	Message    string
	RetryAfter string
}

func (e *APIError) Error() string { return fmt.Sprintf("selfmail HTTP %d: %s", e.Status, e.Message) }

type Client struct {
	BaseURL, APIKey string
	HTTP            *http.Client
}

func New(baseURL, key string) (*Client, error) {
	u, e := url.Parse(baseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid Selfmail URL")
	}
	return &Client{strings.TrimSuffix(baseURL, "/"), key, &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Send(ctx context.Context, key string, input SendRequest) (SendResult, error) {
	var out SendResult
	if key == "" {
		return out, fmt.Errorf("idempotency key is required")
	}
	e := c.call(ctx, "POST", "/v1/messages", key, input, &out)
	return out, e
}
func (c *Client) Status(ctx context.Context, id string) (Message, error) {
	var out Message
	e := c.call(ctx, "GET", "/v1/messages/"+url.PathEscape(id), "", nil, &out)
	return out, e
}
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.call(ctx, "POST", "/v1/messages/"+url.PathEscape(id)+"/cancel", "", nil, nil)
}
func (c *Client) call(ctx context.Context, method, path, key string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(raw)
	}
	r, e := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if e != nil {
		return e
	}
	r.Header.Set("Authorization", "Bearer "+c.APIKey)
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	res, e := c.HTTP.Do(r)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&problem)
		return &APIError{res.StatusCode, problem.Error, res.Header.Get("Retry-After")}
	}
	if output == nil {
		return nil
	}
	// A documented 1000-event page can exceed 1 MiB when diagnostic fields are
	// present. Keep responses bounded without truncating ordinary full pages.
	return json.NewDecoder(io.LimitReader(res.Body, 8*1024*1024)).Decode(output)
}
