package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrGone        = errors.New("metadata expired")
	ErrConflict    = errors.New("conflict")
	ErrForbidden   = errors.New("forbidden")
	ErrSuppressed  = errors.New("recipient suppressed")
	ErrUnavailable = errors.New("temporarily unavailable")
)

type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string  { return e.Field + ": " + e.Message }
func Invalid(field, message string) error { return &ValidationError{field, message} }

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

var uuidRE = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func ValidID(s string) bool { return uuidRE.MatchString(s) }
func Token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type Principal struct {
	KeyID      string   `json:"-"`
	TenantID   string   `json:"tenant_id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	Rate       int      `json:"rate"`
	DailyLimit int      `json:"daily_limit"`
	Paused     bool     `json:"paused"`
}

func (p Principal) Can(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
}

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
	Raw         []byte            `json:"-"`
}
type SendResult struct {
	BatchID    string   `json:"batch_id"`
	MessageIDs []string `json:"message_ids"`
	Replayed   bool     `json:"replayed"`
}
type Message struct {
	KeyDigest    string      `json:"-"`
	Fingerprint  string      `json:"-"`
	NodeID       string      `json:"-"`
	ID           string      `json:"id"`
	TenantID     string      `json:"tenant_id"`
	BatchID      string      `json:"batch_id"`
	From         string      `json:"from"`
	Recipient    string      `json:"recipient"`
	Priority     string      `json:"priority"`
	Status       string      `json:"status"`
	AttemptCount int         `json:"attempt_count"`
	LastError    string      `json:"last_error,omitempty"`
	QueueID      string      `json:"queue_id,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	Payload      SendRequest `json:"-"`
	AttemptID    string      `json:"-"`
}
type Event struct {
	Sequence  int64             `json:"sequence"`
	ID        string            `json:"id"`
	MessageID string            `json:"message_id"`
	Type      string            `json:"type"`
	Details   map[string]string `json:"details,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}
type Template struct {
	Name    string `json:"name"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
	Version int    `json:"version"`
}
type Domain struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Selector     string `json:"selector"`
	Token        string `json:"verification_token,omitempty"`
	PublicKey    string `json:"dkim_public_key"`
	Verified     bool   `json:"verified"`
	EncryptedKey []byte `json:"-"`
}
type Webhook struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}
type Job struct {
	MessageID string `json:"message_id"`
	TenantID  string `json:"tenant_id"`
}

// Dispatch contains a small queue reference, never a message body or credential.
type Dispatch struct {
	Priority string
	Job      Job
}

func Address(input string) (string, error) {
	if len(input) > 320 || strings.ContainsAny(input, "\r\n\x00") {
		return "", Invalid("address", "invalid address")
	}
	a, e := mail.ParseAddress(input)
	if e != nil {
		return "", Invalid("address", "invalid address")
	}
	parts := strings.Split(a.Address, "@")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[0]) > 64 || !ValidDomain(strings.ToLower(parts[1])) {
		return "", Invalid("address", "ASCII address and fully qualified domain required")
	}
	// SMTPUTF8 envelope addresses are intentionally not supported in v1.
	for _, c := range a.Address {
		if c < 33 || c > 126 {
			return "", Invalid("address", "SMTPUTF8 envelope is unsupported")
		}
	}
	return parts[0] + "@" + strings.ToLower(parts[1]), nil
}

var domainRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidDomain(s string) bool {
	if len(s) > 253 || s != strings.ToLower(s) || strings.HasSuffix(s, ".") {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !domainRE.MatchString(l) {
			return false
		}
	}
	return true
}
func SenderDomain(addr string) string { return addr[strings.LastIndex(addr, "@")+1:] }
func Validate(r *SendRequest) error {
	var err error
	if parsed, e := mail.ParseAddress(r.From); e == nil && r.FromName == "" {
		r.FromName = parsed.Name
	}
	if len(r.FromName) > 200 || strings.ContainsAny(r.FromName, "\r\n\x00") {
		return Invalid("from_name", "invalid display name")
	}
	r.From, err = Address(r.From)
	if err != nil {
		return Invalid("from", err.Error())
	}
	if len(r.To) < 1 || len(r.To) > 100 {
		return Invalid("to", "requires 1 to 100 recipients")
	}
	seen := map[string]bool{}
	for i, a := range r.To {
		a, err = Address(a)
		if err != nil {
			return Invalid("to", err.Error())
		}
		if seen[a] {
			return Invalid("to", "duplicate recipient")
		}
		seen[a] = true
		r.To[i] = a
	}
	if r.ReplyTo != "" {
		r.ReplyTo, err = Address(r.ReplyTo)
		if err != nil {
			return err
		}
	}
	if r.Priority == "" {
		r.Priority = "normal"
	}
	if r.Priority != "normal" && r.Priority != "critical" && r.Priority != "bulk" {
		return Invalid("priority", "must be critical, normal or bulk")
	}
	if len(r.Raw) == 0 && r.Template == "" && r.Text == "" && r.HTML == "" {
		return Invalid("body", "text or html required")
	}
	if strings.ContainsAny(r.Subject, "\r\n\x00") || len(r.Subject) > 998 {
		return Invalid("subject", "invalid subject")
	}
	if !r.SendAt.IsZero() && r.SendAt.After(time.Now().Add(30*24*time.Hour)) {
		return Invalid("send_at", "maximum scheduling horizon is 30 days")
	}
	if r.TTLSeconds == 0 {
		r.TTLSeconds = 86400
	}
	if r.TTLSeconds < 30 || r.TTLSeconds > 86400 {
		return Invalid("ttl_seconds", "must be 30 to 86400")
	}
	if len(r.Attachments) > 10 {
		return Invalid("attachments", "maximum 10")
	}
	total := len(r.Text) + len(r.HTML) + len(r.Raw)
	for _, a := range r.Attachments {
		if a.Filename == "" || len(a.Filename) > 255 || strings.ContainsAny(a.Filename, "\r\n\x00/\\") {
			return Invalid("attachment.filename", "invalid filename")
		}
		if len(a.ContentType) > 255 || strings.ContainsAny(a.ContentType, "\r\n\x00") {
			return Invalid("attachment.content_type", "invalid content type")
		}
		if a.ContentType != "" {
			if _, _, e := mime.ParseMediaType(a.ContentType); e != nil {
				return Invalid("attachment.content_type", "invalid media type")
			}
		}
		total += len(a.Data)
	}
	if total > 5*1024*1024 {
		return Invalid("body", "maximum decoded message size is 5 MiB")
	}
	if len(r.Metadata) > 20 {
		return Invalid("metadata", "maximum 20 keys")
	}
	for k, v := range r.Metadata {
		if len(k) > 64 || len(v) > 256 {
			return Invalid("metadata", fmt.Sprintf("oversized field %q", k))
		}
	}
	return nil
}
