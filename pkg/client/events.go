package client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Event struct {
	ID        string            `json:"id"`
	MessageID string            `json:"message_id"`
	Sequence  int64             `json:"sequence"`
	Type      string            `json:"type"`
	Details   map[string]string `json:"details"`
	CreatedAt time.Time         `json:"created_at"`
}
type EventPage struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor"`
}
type MessagePage struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"next_cursor"`
}

func (c *Client) Events(ctx context.Context, id, cursor string, limit int) (out EventPage, e error) {
	if limit < 1 || limit > 1000 {
		return out, fmt.Errorf("event limit must be 1..1000")
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}, "cursor": {cursor}}
	e = c.call(ctx, "GET", "/v1/messages/"+url.PathEscape(id)+"/events?"+q.Encode(), "", nil, &out)
	return
}
func (c *Client) List(ctx context.Context, before string, limit int) (out MessagePage, e error) {
	if limit < 1 || limit > 100 {
		return out, fmt.Errorf("message limit must be 1..100")
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}, "before": {before}}
	e = c.call(ctx, "GET", "/v1/messages?"+q.Encode(), "", nil, &out)
	return
}
func VerifyWebhook(secret []byte, timestamp, signature string, payload []byte, now time.Time, tolerance time.Duration) error {
	if len(secret) < 16 || tolerance <= 0 {
		return fmt.Errorf("invalid verification parameters")
	}
	unix, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil {
		return fmt.Errorf("invalid timestamp")
	}
	sent := time.Unix(unix, 0)
	if sent.Before(now.Add(-tolerance)) || sent.After(now.Add(tolerance)) {
		return fmt.Errorf("expired webhook timestamp")
	}
	if !strings.HasPrefix(signature, "v1=") {
		return fmt.Errorf("unsupported signature")
	}
	got, e := hex.DecodeString(strings.TrimPrefix(signature, "v1="))
	if e != nil || len(got) != sha256.Size {
		return fmt.Errorf("invalid signature")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
