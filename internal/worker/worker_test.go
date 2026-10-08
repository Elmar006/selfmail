package worker

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/mailmsg"
	"github.com/Elmar006/selfmail/internal/mta"
	"github.com/Elmar006/selfmail/internal/security"
)

type fakeRepo struct {
	DeliveryRepository
	m                 domain.Message
	d                 domain.Domain
	status            string
	permanent         bool
	markErr, claimErr error
	marked            bool
}

func (r *fakeRepo) Claim(context.Context, domain.Job, string) (domain.Message, error) {
	return r.m, r.claimErr
}
func (r *fakeRepo) DeliveryPolicy(context.Context, string) (int, bool, error)        { return 10, false, nil }
func (r *fakeRepo) GetDomain(context.Context, string, string) (domain.Domain, error) { return r.d, nil }
func (r *fakeRepo) MarkSending(context.Context, domain.Message) error {
	r.marked = true
	return r.markErr
}
func (r *fakeRepo) FinishSubmission(ctx context.Context, m domain.Message, status, queue, diagnostic string) error {
	r.status = status
	return nil
}
func (r *fakeRepo) Retry(ctx context.Context, m domain.Message, reason string, permanent bool, delay time.Duration) error {
	r.status = "queued"
	r.permanent = permanent
	if permanent {
		r.status = "failed"
	}
	return nil
}

type fakeLimiter struct{ err error }

func (l fakeLimiter) Take(context.Context, string, string, int) (time.Duration, error) {
	return 0, l.err
}

type fakeTransport struct {
	result mta.Result
	calls  int
	repo   *fakeRepo
}

func (t *fakeTransport) LocalHostname() string { return "mail.example.test" }
func (t *fakeTransport) Submit(context.Context, string, string, string, []byte) mta.Result {
	t.calls++
	if !t.repo.marked {
		panic("SMTP called before durable sending state")
	}
	return t.result
}
func TestWorkerFailurePolicy(t *testing.T) {
	vault, e := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	key, pub, e := mailmsg.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	d := domain.Domain{ID: domain.ID(), Name: "example.test", Selector: "mail", PublicKey: pub, Verified: true}
	d.EncryptedKey, e = vault.Seal(key, "dkim:"+d.ID)
	if e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name                      string
		result                    mta.Result
		limErr, markErr, claimErr error
		status                    string
		calls                     int
		unverified                bool
	}{
		{name: "accepted", result: mta.Result{QueueID: "ABC"}, status: "submitted", calls: 1},
		{name: "unknown final response", result: mta.Result{Unknown: true, Err: errors.New("EOF")}, status: "submission_unknown", calls: 1},
		{name: "permanent refusal", result: mta.Result{Permanent: true, Err: errors.New("550")}, status: "failed", calls: 1},
		{name: "temporary refusal", result: mta.Result{Err: errors.New("451")}, status: "queued", calls: 1},
		{name: "Redis unavailable", limErr: errors.New("Redis down"), status: "queued", calls: 0},
		{name: "stale lease", markErr: domain.ErrConflict, status: "", calls: 0},
		{name: "suppressed during preparation", markErr: domain.ErrSuppressed, status: "failed", calls: 0},
		{name: "duplicate job", claimErr: domain.ErrNotFound, status: "", calls: 0},
		{name: "unverified domain", unverified: true, status: "failed", calls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dd := d
			if tc.unverified {
				dd.Verified = false
			}
			repo := &fakeRepo{m: domain.Message{ID: domain.ID(), AttemptID: domain.ID(), TenantID: domain.ID(), From: "a@example.test", Recipient: "b@example.net", CreatedAt: time.Now(), AttemptCount: 1, Payload: domain.SendRequest{Text: "test"}}, d: dd, markErr: tc.markErr, claimErr: tc.claimErr}
			transport := &fakeTransport{result: tc.result, repo: repo}
			w := Worker{Store: repo, Limiter: fakeLimiter{tc.limErr}, Vault: vault, MTA: transport}
			if e := w.Handle(context.Background(), domain.Job{}); e != nil {
				t.Fatal(e)
			}
			if repo.status != tc.status || transport.calls != tc.calls {
				t.Fatalf("status %q calls %d", repo.status, transport.calls)
			}
		})
	}
}
