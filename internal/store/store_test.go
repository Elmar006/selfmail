package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5"
)

func setup(t *testing.T) (*Store, *Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	system, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(system.Close)
	if e = system.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	appDSN := os.Getenv("TEST_APP_DATABASE_URL")
	if appDSN == "" {
		t.Fatal("TEST_APP_DATABASE_URL required")
	}
	app, e := Open(ctx, appDSN)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(app.Close)
	return system, app
}
func tenant(t *testing.T, system *Store, app *Store, daily int) domain.Principal {
	t.Helper()
	ctx := context.Background()
	id, key, e := system.CreateTenant(ctx, "test-"+domain.ID(), 100, daily)
	if e != nil {
		t.Fatal(e)
	}
	p, e := app.Authenticate(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	e = app.AddDomain(ctx, id, domain.Domain{ID: domain.ID(), Name: id + ".example.test", Selector: "mail", Token: "proof", PublicKey: "test", EncryptedKey: []byte("test")})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func request(p domain.Principal) domain.SendRequest {
	return domain.SendRequest{From: "hello@" + p.TenantID + ".example.test", To: []string{"user@example.net"}, Subject: "test", Text: "body", Priority: "normal"}
}
func enqueue(t *testing.T, app *Store, p domain.Principal) string {
	t.Helper()
	r, e := app.Enqueue(context.Background(), p, domain.ID(), "hash", request(p), true)
	if e != nil {
		t.Fatal(e)
	}
	return r.MessageIDs[0]
}

func TestConcurrentIdempotency(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 1000)
	var wg sync.WaitGroup
	ids := make(chan string, 40)
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, e := app.Enqueue(context.Background(), p, "same-request", "same-hash", request(p), true)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- result.BatchID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("multiple batches")
		}
	}
	var n int
	system.Pool.QueryRow(context.Background(), "SELECT count(*) FROM messages WHERE tenant_id=$1", p.TenantID).Scan(&n)
	if n != 1 {
		t.Fatalf("created %d messages", n)
	}
	if _, e := app.Enqueue(context.Background(), p, "same-request", "other-hash", request(p), true); !errors.Is(e, domain.ErrConflict) {
		t.Fatalf("%v", e)
	}
}
func TestQuotaRollbackAndConcurrentLimit(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 10)
	r := request(p)
	r.To = make([]string, 11)
	for i := range r.To {
		r.To[i] = domain.ID() + "@example.net"
	}
	if _, e := app.Enqueue(context.Background(), p, "oversized", "hash", r, true); e == nil {
		t.Fatal("over quota accepted")
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := app.Enqueue(context.Background(), p, domain.ID(), "hash", request(p), true); e == nil {
				successes.Add(1)
			} else if _, ok := e.(*domain.ValidationError); !ok {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 10 {
		t.Fatalf("accepted %d, want 10", successes.Load())
	}
}
func TestRowLevelSecurityAndCrossTenantAccess(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	other := tenant(t, system, app, 100)
	id := enqueue(t, app, p)
	if _, e := app.GetMessage(context.Background(), other.TenantID, id); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("cross-tenant API access")
	}
	e := app.TenantTx(context.Background(), other.TenantID, func(tx pgx.Tx) error {
		var n int
		e := tx.QueryRow(context.Background(), "SELECT count(*) FROM messages WHERE id=$1", id).Scan(&n)
		if n != 0 {
			t.Error("RLS leak without explicit tenant predicate")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	var n int
	if e = app.Pool.QueryRow(context.Background(), "SELECT count(*) FROM messages").Scan(&n); e != nil || n != 0 {
		t.Fatalf("tenant context leaked in pool: %d %v", n, e)
	}
	if _, e = app.Pool.Exec(context.Background(), "SELECT * FROM api_keys"); e == nil {
		t.Fatal("API role can read global credentials")
	}
}
func TestExclusiveClaimsAndCancellation(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	id := enqueue(t, app, p)
	job := domain.Job{MessageID: id, TenantID: p.TenantID}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := system.Claim(context.Background(), job, "test-mta")
			if e == nil {
				wins.Add(1)
			} else if !errors.Is(e, domain.ErrNotFound) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claimed %d times", wins.Load())
	}
	if e := app.Cancel(context.Background(), p.TenantID, id); !errors.Is(e, domain.ErrConflict) {
		t.Fatalf("dispatching canceled: %v", e)
	}
	id = enqueue(t, app, p)
	if e := app.Cancel(context.Background(), p.TenantID, id); e != nil {
		t.Fatal(e)
	}
	if _, e := system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta"); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("canceled job claimed")
	}
}
func TestCrashRecoveryCommitBoundary(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	for _, sending := range []bool{false, true} {
		id := enqueue(t, app, p)
		m, e := system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta")
		if e != nil {
			t.Fatal(e)
		}
		if sending {
			if e = system.MarkSending(context.Background(), m); e != nil {
				t.Fatal(e)
			}
		}
		system.Pool.Exec(context.Background(), "UPDATE messages SET lease_until=now()-interval '1 second' WHERE id=$1", id)
		if e = system.Recover(context.Background()); e != nil {
			t.Fatal(e)
		}
		got, e := app.GetMessage(context.Background(), p.TenantID, id)
		if e != nil {
			t.Fatal(e)
		}
		expected := "queued"
		if sending {
			expected = "submission_unknown"
		}
		if got.Status != expected {
			t.Fatalf("%s want %s", got.Status, expected)
		}
		if sending {
			if _, e = system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta"); !errors.Is(e, domain.ErrNotFound) {
				t.Fatal("unknown handoff was retried")
			}
		}
	}
}
func TestReceiptOutOfOrderAndNoRegression(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	id := enqueue(t, app, p)
	m, e := system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta")
	if e != nil {
		t.Fatal(e)
	}
	if e = system.MarkSending(context.Background(), m); e != nil {
		t.Fatal(e)
	}
	if e = system.FinishSubmission(context.Background(), m, "submission_unknown", "", "lost response"); e != nil {
		t.Fatal(e)
	}
	file := domain.ID()
	queue := domain.ID()
	if e = system.ApplyLogLine(context.Background(), "test-mta", file, 0, 100, queue, "", "", "sent", m.Recipient, "2.0.0", "accepted", true); e != nil {
		t.Fatal(e)
	}
	if e = system.ApplyLogLine(context.Background(), "test-mta", file, 100, 200, queue, id, m.AttemptID, "", "", "", "", true); e != nil {
		t.Fatal(e)
	}
	if e = system.FinishSubmission(context.Background(), m, "submitted", queue, ""); e != nil {
		t.Fatal(e)
	}
	got, e := app.GetMessage(context.Background(), p.TenantID, id)
	if e != nil || got.Status != "delivered" {
		t.Fatalf("%+v %v", got, e)
	}
	if e = system.ApplyLogLine(context.Background(), "test-mta", file, 0, 100, queue, "", "", "sent", m.Recipient, "2.0.0", "accepted", true); e != nil {
		t.Fatal(e)
	}
	events, e := app.Events(context.Background(), p.TenantID, id)
	if e != nil {
		t.Fatal(e)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == "delivered" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("delivered events %d", n)
	}
}
func TestSuppressionIsolationAndDSN(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	other := tenant(t, system, app, 100)
	id := enqueue(t, app, p)
	m, e := system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta")
	if e != nil {
		t.Fatal(e)
	}
	if e = system.ApplyDSN(context.Background(), m.AttemptID, "wrong@example.net", "failed", "5.1.1"); !errors.Is(e, domain.ErrForbidden) {
		t.Fatal("foreign DSN recipient accepted")
	}
	if e = system.ApplyDSN(context.Background(), m.AttemptID, m.Recipient, "failed", "5.1.1"); e != nil {
		t.Fatal(e)
	}
	if _, e = app.Enqueue(context.Background(), p, domain.ID(), "hash", request(p), true); !errors.Is(e, domain.ErrSuppressed) {
		t.Fatal("hard bounce not suppressed")
	}
	if _, e = app.Enqueue(context.Background(), other, domain.ID(), "hash", request(other), true); e != nil {
		t.Fatal("suppression leaked across tenants", e)
	}
}

type failedPublisher struct{}

func (failedPublisher) Publish(context.Context, string, domain.Job) error {
	return errors.New("broker unavailable")
}
func TestOutboxFailureDoesNotLoseJob(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	id := enqueue(t, app, p)
	_, e := system.PublishOne(context.Background(), failedPublisher{})
	if e == nil {
		t.Fatal("publisher error swallowed")
	}
	var pending bool
	if e = system.Pool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM outbox WHERE message_id=$1 AND published_at IS NULL)", id).Scan(&pending); e != nil || !pending {
		t.Fatalf("outbox lost: %v", e)
	}
}
func TestRateDeferralPreservesRetryBudget(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	id := enqueue(t, app, p)
	for range 12 {
		m, e := system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta")
		if e != nil {
			t.Fatal(e)
		}
		if e = system.Retry(context.Background(), m, "rate limited", false, 0); e != nil {
			t.Fatal(e)
		}
	}
	got, e := app.GetMessage(context.Background(), p.TenantID, id)
	if e != nil || got.AttemptCount != 0 || got.Status != "queued" {
		t.Fatalf("retry budget consumed: %+v %v", got, e)
	}
}
func TestAuthenticationRevocation(t *testing.T) {
	system, app := setup(t)
	id, key, e := system.CreateTenant(context.Background(), "revocation", 10, 10)
	if e != nil {
		t.Fatal(e)
	}
	p, e := app.Authenticate(context.Background(), key)
	if e != nil || p.TenantID != id {
		t.Fatal(e)
	}
	if e = system.RevokeKey(context.Background(), key); e != nil {
		t.Fatal(e)
	}
	if _, e = app.Authenticate(context.Background(), key); !errors.Is(e, domain.ErrForbidden) {
		t.Fatal("revoked key accepted")
	}
}
func TestRetryDelayBounded(t *testing.T) {
	if RetryDelay(100) > time.Hour || RetryDelay(0) <= 0 {
		t.Fatal("invalid delay")
	}
}

func TestExpiredMessageNeverClaims(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	id := enqueue(t, app, p)
	if _, e := system.Pool.Exec(context.Background(), "UPDATE messages SET expires_at=now()-interval '1 second' WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	if _, e := system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta"); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("expired message claimed", e)
	}
	m, e := app.GetMessage(context.Background(), p.TenantID, id)
	if e != nil || m.Status != "failed" || m.AttemptCount != 0 {
		t.Fatalf("%+v %v", m, e)
	}
}
