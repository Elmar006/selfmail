package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/mailmsg"
	"github.com/Elmar006/selfmail/internal/security"
)

type Repository interface {
	Authenticate(context.Context, string) (domain.Principal, error)
	Replay(context.Context, string, string, string) (domain.SendResult, bool, error)
	Enqueue(context.Context, domain.Principal, string, string, domain.SendRequest, bool) (domain.SendResult, error)
	GetMessage(context.Context, string, string) (domain.Message, error)
	ListMessages(context.Context, string, string, int) ([]domain.Message, error)
	Events(context.Context, string, string) ([]domain.Event, error)
	Cancel(context.Context, string, string) error
	Suppress(context.Context, string, string, string, bool) error
	AddDomain(context.Context, string, domain.Domain) error
	GetDomain(context.Context, string, string) (domain.Domain, error)
	VerifyDomain(context.Context, string, string) error
	PutTemplate(context.Context, string, domain.Template) (int, error)
	GetTemplate(context.Context, string, string) (domain.Template, error)
	AddWebhook(context.Context, string, domain.Webhook, []byte) error
	DeleteWebhook(context.Context, string, string) error
}
type RateLimiter interface {
	Ingress(context.Context, string, int) (time.Duration, error)
}
type TXTResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
}
type Service struct {
	Repo            Repository
	Limiter         RateLimiter
	Vault           *security.Vault
	Resolver        TXTResolver
	AllowUnverified bool
	WireLimit       int
	Budget          *WorkBudget
}

func (s *Service) Authenticate(ctx context.Context, key string) (domain.Principal, error) {
	return s.Repo.Authenticate(ctx, key)
}
func (s *Service) Send(ctx context.Context, p domain.Principal, key string, r domain.SendRequest) (domain.SendResult, error) {
	ctx, release, e := s.Budget.Acquire(ctx)
	if e != nil {
		return domain.SendResult{}, e
	}
	defer release()
	if repo, ok := s.Repo.(interface {
		RefreshPrincipal(context.Context, domain.Principal) (domain.Principal, error)
	}); ok {
		var e error
		p, e = repo.RefreshPrincipal(ctx, p)
		if e != nil {
			return domain.SendResult{}, e
		}
	}
	if !p.Can("messages:write") || p.Paused {
		return domain.SendResult{}, domain.ErrForbidden
	}
	if key == "" || len(key) > 200 || strings.ContainsAny(key, "\r\n\x00") {
		return domain.SendResult{}, domain.Invalid("idempotency_key", "required, maximum 200 UTF-8 bytes")
	}
	if e := domain.Validate(&r); e != nil {
		return domain.SendResult{}, e
	}
	if len(r.Variables) > 0 {
		vars, e := boundedVariables(ctx, r.Variables)
		if e != nil {
			return domain.SendResult{}, domain.Invalid("variables", e.Error())
		}
		r.Variables = vars
	}
	sort.Strings(r.To)
	if len(r.Raw) > 0 {
		subject, e := mailmsg.ValidateRaw(r.Raw, r.From)
		if e != nil {
			return domain.SendResult{}, e
		}
		r.Subject = subject
	}
	original, e := json.Marshal(r)
	if e != nil {
		return domain.SendResult{}, e
	}
	fingerprint := security.Digest(string(original) + "\x00" + string(r.Raw))
	if result, exists, e := s.Repo.Replay(ctx, p.TenantID, key, fingerprint); e != nil {
		return domain.SendResult{}, e
	} else if exists {
		return result, nil
	}
	if s.Limiter != nil {
		wait, e := s.Limiter.Ingress(ctx, p.TenantID, max(p.Rate, 10))
		if e != nil {
			return domain.SendResult{}, fmt.Errorf("rate limiter: %w", domain.ErrUnavailable)
		}
		if wait > 0 {
			return domain.SendResult{}, domain.ErrUnavailable
		}
	}
	templateVersion := 0
	if r.Template != "" {
		if r.Text != "" || r.HTML != "" || len(r.Raw) > 0 {
			return domain.SendResult{}, domain.Invalid("template", "cannot combine template with body")
		}
		t, e := s.Repo.GetTemplate(ctx, p.TenantID, r.Template)
		if e != nil {
			return domain.SendResult{}, e
		}
		if e = RenderContext(ctx, t, r.Variables, &r); e != nil {
			return domain.SendResult{}, e
		}
		templateVersion = t.Version
	}
	if e = domain.Validate(&r); e != nil {
		return domain.SendResult{}, e
	}
	if templateVersion > 0 {
		if r.Metadata == nil {
			r.Metadata = map[string]string{}
		}
		r.Metadata["template_version"] = fmt.Sprint(templateVersion)
	}
	if e = mailmsg.ValidateWire(r, s.WireLimit); e != nil {
		return domain.SendResult{}, e
	}
	return s.Repo.Enqueue(ctx, p, key, fingerprint, r, s.AllowUnverified)
}
func Render(t domain.Template, vars map[string]any, r *domain.SendRequest) error {
	return RenderContext(context.Background(), t, vars, r)
}

var templateName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func ValidateTemplate(t domain.Template) error {
	if !templateName.MatchString(t.Name) || len(t.Subject) > 1000 || len(t.Text)+len(t.HTML) > 128*1024 {
		return domain.Invalid("template", "invalid name or size")
	}
	if e := CheckTemplateSource(t.Subject, false); e != nil {
		return domain.Invalid("subject", e.Error())
	}
	if e := CheckTemplateSource(t.Text, false); e != nil {
		return domain.Invalid("text", e.Error())
	}
	if e := CheckTemplateSource(t.HTML, true); e != nil {
		return domain.Invalid("html", e.Error())
	}
	return nil
}
func (s *Service) CreateDomain(ctx context.Context, tenant, name string) (domain.Domain, error) {
	name = strings.ToLower(name)
	if !domain.ValidDomain(name) {
		return domain.Domain{}, domain.Invalid("domain", "invalid domain")
	}
	key, pub, e := mailmsg.GenerateKey()
	if e != nil {
		return domain.Domain{}, e
	}
	d := domain.Domain{ID: domain.ID(), Name: name, Token: "selfmail-verification=" + domain.Token(), PublicKey: pub}
	d.Selector = "mail" + time.Now().UTC().Format("20060102") + d.ID[:8]
	d.EncryptedKey, e = s.Vault.Seal(key, "dkim:"+d.ID)
	if e != nil {
		return d, e
	}
	return d, s.Repo.AddDomain(ctx, tenant, d)
}
func (s *Service) CheckDomain(ctx context.Context, tenant, name string) (domain.Domain, error) {
	d, e := s.Repo.GetDomain(ctx, tenant, name)
	if e != nil {
		return d, e
	}
	resolver := s.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	txt, e := resolver.LookupTXT(ctx, "_selfmail."+d.Name)
	if e != nil {
		return d, domain.Invalid("dns", "verification TXT lookup failed")
	}
	verified := false
	for _, v := range txt {
		if v == d.Token {
			verified = true
		}
	}
	if !verified {
		return d, domain.Invalid("dns", "verification TXT mismatch")
	}
	txt, e = resolver.LookupTXT(ctx, d.Selector+"._domainkey."+d.Name)
	if e != nil {
		return d, domain.Invalid("dns", "DKIM TXT lookup failed")
	}
	verified = false
	for _, v := range txt {
		tags := map[string]string{}
		for _, part := range strings.Split(v, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 {
				tags[kv[0]] = strings.ReplaceAll(kv[1], " ", "")
			}
		}
		if tags["v"] == "DKIM1" && tags["p"] == d.PublicKey {
			verified = true
		}
	}
	if !verified {
		return d, domain.Invalid("dns", "DKIM public key mismatch")
	}
	e = s.Repo.VerifyDomain(ctx, tenant, name)
	d.Verified = e == nil
	return d, e
}
func IsValidation(e error) bool { var v *domain.ValidationError; return errors.As(e, &v) }
