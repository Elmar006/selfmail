package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/application"
	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/security"
)

type principalKey struct{}
type API struct {
	Service              *application.Service
	AllowPrivateWebhooks bool
	Ready                func(context.Context) error
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	code := http.StatusInternalServerError
	message := "internal server error"
	switch {
	case errors.Is(e, domain.ErrForbidden):
		code = 403
		message = "forbidden"
	case errors.Is(e, domain.ErrNotFound):
		code = 404
		message = "not found"
	case errors.Is(e, domain.ErrGone):
		code = 410
		message = "message metadata expired; idempotency record retained"
	case errors.Is(e, domain.ErrConflict):
		code = 409
		message = "idempotency conflict or invalid state"
	case errors.Is(e, domain.ErrSuppressed):
		code = 422
		message = "recipient suppressed"
	case errors.Is(e, domain.ErrUnavailable):
		code = 503
		message = "temporarily unavailable"
		w.Header().Set("Retry-After", "2")
	case application.IsValidation(e):
		code = 422
		message = e.Error()
	}
	if code == 500 {
		slog.Error("API operation failed", "error", e)
	}
	respond(w, code, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		respond(w, 400, map[string]string{"error": "invalid JSON body or request too large"})
		return false
	}
	if e := d.Decode(new(any)); e != io.EOF {
		respond(w, 400, map[string]string{"error": "exactly one JSON object required"})
		return false
	}
	return true
}
func (a *API) auth(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			respond(w, 401, map[string]string{"error": "Bearer API key required"})
			return
		}
		p, e := a.Service.Authenticate(r.Context(), strings.TrimPrefix(header, "Bearer "))
		if e != nil {
			if errors.Is(e, domain.ErrForbidden) {
				respond(w, 401, map[string]string{"error": "invalid API key"})
			} else {
				fail(w, domain.ErrUnavailable)
			}
			return
		}
		if !p.Can(scope) {
			fail(w, domain.ErrForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}
func p(r *http.Request) domain.Principal { return r.Context().Value(principalKey{}).(domain.Principal) }
func validPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !domain.ValidID(id) {
		fail(w, domain.ErrNotFound)
		return "", false
	}
	return id, true
}
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if a.Ready != nil {
			if e := a.Ready(ctx); e != nil {
				fail(w, domain.ErrUnavailable)
				return
			}
		}
		respond(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /v1/messages", a.auth("messages:write", func(w http.ResponseWriter, r *http.Request) {
		var req domain.SendRequest
		if !decode(w, r, &req) {
			return
		}
		result, e := a.Service.Send(r.Context(), p(r), r.Header.Get("Idempotency-Key"), req)
		if e != nil {
			fail(w, e)
			return
		}
		status := 202
		if result.Replayed {
			status = 200
		}
		respond(w, status, result)
	}))
	mux.HandleFunc("GET /v1/messages/{id}", a.auth("messages:read", func(w http.ResponseWriter, r *http.Request) {
		id, ok := validPath(w, r)
		if !ok {
			return
		}
		result, e := a.Service.Repo.GetMessage(r.Context(), p(r).TenantID, id)
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, result)
	}))
	mux.HandleFunc("GET /v1/messages", a.auth("messages:read", func(w http.ResponseWriter, r *http.Request) {
		before := r.URL.Query().Get("before")
		if before != "" && !domain.ValidID(before) {
			fail(w, domain.Invalid("before", "invalid UUID"))
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil || n < 1 || n > 100 {
				fail(w, domain.Invalid("limit", "must be 1 to 100"))
				return
			}
			limit = n
		}
		items, e := a.Service.Repo.ListMessages(r.Context(), p(r).TenantID, before, limit)
		if e != nil {
			fail(w, e)
			return
		}
		next := ""
		if len(items) == limit {
			next = items[len(items)-1].ID
		}
		respond(w, 200, map[string]any{"messages": items, "next_cursor": next})
	}))
	mux.HandleFunc("GET /v1/messages/{id}/events", a.auth("messages:read", func(w http.ResponseWriter, r *http.Request) {
		id, ok := validPath(w, r)
		if !ok {
			return
		}
		var items []domain.Event
		var e error
		next := ""
		if repo, ok := a.Service.Repo.(interface {
			EventsPage(context.Context, string, string, string, int) ([]domain.Event, string, error)
		}); ok {
			limit := 1000
			if raw := r.URL.Query().Get("limit"); raw != "" {
				limit, e = strconv.Atoi(raw)
				if e != nil {
					fail(w, domain.Invalid("limit", "integer required"))
					return
				}
			}
			items, next, e = repo.EventsPage(r.Context(), p(r).TenantID, id, r.URL.Query().Get("cursor"), limit)
		} else {
			items, e = a.Service.Repo.Events(r.Context(), p(r).TenantID, id)
		}
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, map[string]any{"events": items, "next_cursor": next})
	}))
	mux.HandleFunc("POST /v1/messages/{id}/cancel", a.auth("messages:write", func(w http.ResponseWriter, r *http.Request) {
		id, ok := validPath(w, r)
		if !ok {
			return
		}
		if e := a.Service.Repo.Cancel(r.Context(), p(r).TenantID, id); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /v1/domains", a.auth("domains:write", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if !decode(w, r, &req) {
			return
		}
		d, e := a.Service.CreateDomain(r.Context(), p(r).TenantID, req.Name)
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 201, domainDNS(d))
	}))
	mux.HandleFunc("GET /v1/domains/{name}", a.auth("domains:write", func(w http.ResponseWriter, r *http.Request) {
		d, e := a.Service.Repo.GetDomain(r.Context(), p(r).TenantID, r.PathValue("name"))
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, domainDNS(d))
	}))
	mux.HandleFunc("POST /v1/domains/{name}/verify", a.auth("domains:write", func(w http.ResponseWriter, r *http.Request) {
		d, e := a.Service.CheckDomain(r.Context(), p(r).TenantID, r.PathValue("name"))
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, domainDNS(d))
	}))
	mux.HandleFunc("PUT /v1/templates/{name}", a.auth("templates:write", func(w http.ResponseWriter, r *http.Request) {
		var t domain.Template
		if !decode(w, r, &t) {
			return
		}
		t.Name = r.PathValue("name")
		if e := application.ValidateTemplate(t); e != nil {
			fail(w, e)
			return
		}
		version, e := a.Service.Repo.PutTemplate(r.Context(), p(r).TenantID, t)
		if e != nil {
			fail(w, e)
			return
		}
		t.Version = version
		respond(w, 201, t)
	}))
	mux.HandleFunc("POST /v1/suppressions", a.auth("suppressions:write", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Recipient string `json:"recipient"`
			Reason    string `json:"reason"`
		}
		if !decode(w, r, &req) {
			return
		}
		recipient, e := domain.Address(req.Recipient)
		if e != nil {
			fail(w, e)
			return
		}
		if len(req.Reason) > 256 {
			fail(w, domain.Invalid("reason", "too long"))
			return
		}
		if e = a.Service.Repo.Suppress(r.Context(), p(r).TenantID, recipient, req.Reason, false); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("DELETE /v1/suppressions/{recipient}", a.auth("suppressions:write", func(w http.ResponseWriter, r *http.Request) {
		recipient, e := domain.Address(r.PathValue("recipient"))
		if e != nil {
			fail(w, e)
			return
		}
		if e = a.Service.Repo.Suppress(r.Context(), p(r).TenantID, recipient, "", true); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /v1/webhooks", a.auth("webhooks:write", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if !decode(w, r, &req) {
			return
		}
		if e := security.ValidateWebhook(req.URL, a.AllowPrivateWebhooks); e != nil {
			fail(w, domain.Invalid("url", e.Error()))
			return
		}
		hook := domain.Webhook{ID: domain.ID(), URL: req.URL, Secret: domain.Token()}
		encrypted, e := a.Service.Vault.Seal([]byte(hook.Secret), "webhook:"+hook.ID)
		if e != nil {
			fail(w, e)
			return
		}
		if e = a.Service.Repo.AddWebhook(r.Context(), p(r).TenantID, hook, encrypted); e != nil {
			fail(w, e)
			return
		}
		respond(w, 201, hook)
	}))
	mux.HandleFunc("DELETE /v1/webhooks/{id}", a.auth("webhooks:write", func(w http.ResponseWriter, r *http.Request) {
		id, ok := validPath(w, r)
		if !ok {
			return
		}
		if e := a.Service.Repo.DeleteWebhook(r.Context(), p(r).TenantID, id); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(204)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
			ctx, release, e := a.Service.Budget.Acquire(r.Context())
			if e != nil {
				fail(w, e)
				return
			}
			defer release()
			r = r.WithContext(ctx)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Request-ID", domain.ID())
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		defer func() {
			if v := recover(); v != nil {
				slog.Error("HTTP panic", "error", v)
				respond(w, 500, map[string]string{"error": "internal server error"})
			}
		}()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func domainDNS(d domain.Domain) any {
	return map[string]any{"domain": d, "dns": []map[string]string{{"type": "TXT", "name": "_selfmail." + d.Name, "value": d.Token}, {"type": "TXT", "name": d.Selector + "._domainkey." + d.Name, "value": "v=DKIM1; k=rsa; p=" + d.PublicKey}}}
}
