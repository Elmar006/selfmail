package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elmar006/selfmail/internal/application"
	"github.com/Elmar006/selfmail/internal/domain"
)

type testRepo struct {
	application.Repository
	principal domain.Principal
}

func (r testRepo) Authenticate(ctx context.Context, key string) (domain.Principal, error) {
	if key != "test-key" {
		return domain.Principal{}, domain.ErrForbidden
	}
	return r.principal, nil
}
func (r testRepo) Replay(context.Context, string, string, string) (domain.SendResult, bool, error) {
	return domain.SendResult{}, false, nil
}
func (r testRepo) Enqueue(context.Context, domain.Principal, string, string, domain.SendRequest, bool) (domain.SendResult, error) {
	return domain.SendResult{BatchID: domain.ID(), MessageIDs: []string{domain.ID()}}, nil
}
func (r testRepo) GetMessage(context.Context, string, string) (domain.Message, error) {
	return domain.Message{}, domain.ErrNotFound
}
func TestHTTPAuthenticationScopeAndStrictInput(t *testing.T) {
	p := domain.Principal{TenantID: domain.ID(), Scopes: []string{"messages:read", "messages:write"}, Rate: 100}
	a := &API{Service: &application.Service{Repo: testRepo{principal: p}}}
	handler := a.Handler()
	cases := []struct {
		path, method, key, body, idempotency string
		status                               int
	}{
		{"/v1/messages", "POST", "", "{}", "x", 401},
		{"/v1/messages", "POST", "wrong", "{}", "x", 401},
		{"/v1/messages", "POST", "test-key", `{"from":"a@example.test","to":["b@example.net"],"text":"ok","unknown":true}`, "x", 400},
		{"/v1/messages", "POST", "test-key", `{"from":"a@example.test","to":["b@example.net"],"text":"ok"}{}`, "x", 400},
		{"/v1/messages", "POST", "test-key", `{"from":"a@example.test","to":["b@example.net"],"text":"ok"}`, "", 422},
		{"/v1/messages", "POST", "test-key", `{"from":"a@example.test","to":["b@example.net"],"text":"ok"}`, "x", 202},
		{"/v1/messages/bad-id", "GET", "test-key", "", "", 404},
		{"/v1/messages/" + domain.ID(), "GET", "test-key", "", "", 404},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		req.Header.Set("Idempotency-Key", c.idempotency)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != c.status {
			t.Errorf("%s: %d want %d: %s", c.path, w.Code, c.status, w.Body.String())
		}
	}
	p.Scopes = []string{"messages:read"}
	a = &API{Service: &application.Service{Repo: testRepo{principal: p}}}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("read-only key can send")
	}
}
