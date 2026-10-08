package submission

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"time"

	"github.com/Elmar006/selfmail/internal/application"
	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
)

type backend struct{ service *application.Service }
type session struct {
	service       *application.Service
	principal     domain.Principal
	authenticated bool
	authUntil     time.Time
	from          string
	to            []string
}

func New(service *application.Service, addr, hostname, cert, key string, allowPlain bool) (*smtp.Server, error) {
	s := smtp.NewServer(&backend{service})
	s.Addr = addr
	s.Domain = hostname
	s.MaxMessageBytes = 5 * 1024 * 1024
	s.MaxRecipients = 100
	s.MaxLineLength = 1000
	s.AllowInsecureAuth = allowPlain
	s.ReadTimeout = 30 * time.Second
	s.WriteTimeout = 30 * time.Second
	if cert != "" && key != "" {
		pair, e := tls.LoadX509KeyPair(cert, key)
		if e != nil {
			return nil, e
		}
		s.TLSConfig = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	} else if !allowPlain {
		return nil, errors.New("SMTP_TLS_CERT and SMTP_TLS_KEY required for submission")
	}
	return s, nil
}
func (b *backend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &session{service: b.service}, nil
}
func (s *session) AuthMechanisms() []string { return []string{"PLAIN"} }
func (s *session) Auth(mech string) (sasl.Server, error) {
	if mech != "PLAIN" {
		return nil, &smtp.SMTPError{Code: 504, Message: "unsupported authentication mechanism"}
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p, e := s.service.Authenticate(ctx, password)
		if e != nil || p.Paused || !p.Can("messages:write") || (username != p.TenantID && username != "apikey") || (identity != "" && identity != username) {
			return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "authentication failed"}
		}
		s.principal = p
		s.authenticated = true
		s.authUntil = time.Now().Add(30 * time.Minute)
		return nil
	}), nil
}
func (s *session) Reset() { s.from = ""; s.to = nil }
func (s *session) Logout() error {
	s.principal = domain.Principal{}
	s.authenticated = false
	return nil
}
func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if !s.authenticated {
		return &smtp.SMTPError{Code: 530, Message: "authentication required"}
	}
	a, e := domain.Address(from)
	if e != nil {
		return &smtp.SMTPError{Code: 553, Message: "invalid sender"}
	}
	s.from = a
	s.to = nil
	return nil
}
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	a, e := domain.Address(to)
	if e != nil {
		return &smtp.SMTPError{Code: 553, Message: "invalid recipient"}
	}
	for _, v := range s.to {
		if v == a {
			return nil
		}
	}
	s.to = append(s.to, a)
	return nil
}
func (s *session) Data(r io.Reader) error {
	if !s.authenticated || time.Now().After(s.authUntil) {
		return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "authentication expired"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctx, release, e := s.service.Budget.Acquire(ctx)
	if e != nil {
		return &smtp.SMTPError{Code: 451, Message: "submission busy; retry later"}
	}
	defer release()
	raw, e := io.ReadAll(io.LimitReader(r, 5*1024*1024+1))
	if e != nil {
		return e
	}
	if len(raw) > 5*1024*1024 {
		return &smtp.SMTPError{Code: 552, Message: "message too large"}
	}
	// SMTP has no idempotency contract: only exact retransmissions with the same raw
	// MIME, envelope and recipient set share this key. Applications should set Message-ID.
	req := domain.SendRequest{From: s.from, To: append([]string(nil), s.to...), Raw: raw, Priority: "normal"}
	key := "smtp:" + security.Digest(string(raw)+"\x00"+s.from+"\x00"+joinSorted(s.to))
	_, e = s.service.Send(ctx, s.principal, key, req)
	if e == nil {
		return nil
	}
	if errors.Is(e, domain.ErrUnavailable) {
		return &smtp.SMTPError{Code: 451, Message: "temporary failure; retry later"}
	}
	if application.IsValidation(e) || errors.Is(e, domain.ErrForbidden) || errors.Is(e, domain.ErrSuppressed) {
		return &smtp.SMTPError{Code: 554, Message: "message rejected by project policy"}
	}
	return &smtp.SMTPError{Code: 451, Message: "temporary persistence failure"}
}
