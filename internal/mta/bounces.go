package mta

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
	smtp "github.com/emersion/go-smtp"
)

func bounceMAC(attempt string, key []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("bounce:" + attempt))
	return hex.EncodeToString(h.Sum(nil)[:10])
}
func BounceAddress(attempt, hostname string, key []byte) string {
	return "b+" + strings.ReplaceAll(attempt, "-", "") + "+" + bounceMAC(attempt, key) + "@" + hostname
}
func VerifyBounce(address, hostname string, key []byte) (string, bool) {
	p := strings.Split(strings.ToLower(address), "@")
	if len(p) != 2 || p[1] != hostname {
		return "", false
	}
	fields := strings.Split(p[0], "+")
	if len(fields) != 3 || fields[0] != "b" || len(fields[1]) != 32 {
		return "", false
	}
	s := fields[1]
	id := s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	return id, domain.ValidID(id) && hmac.Equal([]byte(fields[2]), []byte(bounceMAC(id, key)))
}

type DSN struct{ Recipient, Action, Status string }

func ParseDSN(raw []byte) ([]DSN, error) {
	msg, e := mail.ReadMessage(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	typ, params, e := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if e != nil || typ != "multipart/report" || params["report-type"] != "delivery-status" {
		return nil, fmt.Errorf("expected delivery-status report")
	}
	r := multipart.NewReader(msg.Body, params["boundary"])
	var result []DSN
	for {
		part, e := r.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		pt, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if pt != "message/delivery-status" {
			continue
		}
		body, e := io.ReadAll(io.LimitReader(part, 64*1024))
		if e != nil {
			return nil, e
		}
		body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		blocks := strings.Split(string(body), "\n\n")
		for _, block := range blocks {
			h, e := textproto.NewReader(bufReader(block + "\n\n")).ReadMIMEHeader()
			if e != nil {
				continue
			}
			final := h.Get("Final-Recipient")
			p := strings.SplitN(final, ";", 2)
			if len(p) != 2 || strings.TrimSpace(strings.ToLower(p[0])) != "rfc822" {
				continue
			}
			addr, e := domain.Address(strings.TrimSpace(p[1]))
			if e != nil {
				continue
			}
			action := h.Get("Action")
			status := h.Get("Status")
			if (action == "failed" && strings.HasPrefix(status, "5.")) || (action == "delayed" && strings.HasPrefix(status, "4.")) {
				result = append(result, DSN{addr, action, status})
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no supported recipient DSN")
	}
	return result, nil
}

type bounceBackend struct {
	store    *store.Store
	hostname string
	key      []byte
}
type bounceSession struct {
	backend *bounceBackend
	attempt string
}

func NewBounceServer(s *store.Store, addr, hostname string, key []byte) *smtp.Server {
	b := &bounceBackend{s, hostname, key}
	server := smtp.NewServer(b)
	server.Addr = addr
	server.Domain = hostname
	server.MaxMessageBytes = 1024 * 1024
	server.MaxRecipients = 1
	server.ReadTimeout = 15 * time.Second
	server.WriteTimeout = 15 * time.Second
	return server
}
func (b *bounceBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &bounceSession{backend: b}, nil
}
func (s *bounceSession) Reset()        { s.attempt = "" }
func (s *bounceSession) Logout() error { return nil }
func (s *bounceSession) Mail(from string, _ *smtp.MailOptions) error {
	if from != "" {
		return &smtp.SMTPError{Code: 550, Message: "null sender required for DSN"}
	}
	return nil
}
func (s *bounceSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	id, ok := VerifyBounce(to, s.backend.hostname, s.backend.key)
	if !ok {
		return &smtp.SMTPError{Code: 550, Message: "invalid bounce token"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, e := s.backend.store.AttemptRecipient(ctx, id); e != nil {
		return &smtp.SMTPError{Code: 450, Message: "bounce lookup unavailable"}
	}
	s.attempt = id
	return nil
}
func (s *bounceSession) Data(r io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(r, 1024*1024+1))
	if e != nil {
		return e
	}
	items, e := ParseDSN(raw)
	if e != nil {
		return &smtp.SMTPError{Code: 550, Message: "invalid DSN"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	expected, e := s.backend.store.AttemptRecipient(ctx, s.attempt)
	if e != nil {
		return &smtp.SMTPError{Code: 451, Message: "temporary lookup failure"}
	}
	for _, d := range items {
		if d.Recipient == expected {
			if e = s.backend.store.ApplyDSN(ctx, s.attempt, d.Recipient, d.Action, d.Status); e != nil {
				return &smtp.SMTPError{Code: 451, Message: "temporary persistence failure"}
			}
			return nil
		}
	}
	return &smtp.SMTPError{Code: 550, Message: "DSN recipient mismatch"}
}
