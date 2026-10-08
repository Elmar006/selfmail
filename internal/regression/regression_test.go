package audit

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/application"
	"github.com/Elmar006/selfmail/internal/broker"
	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/mailmsg"
	"github.com/Elmar006/selfmail/internal/mta"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/internal/worker"
)

// Independent timing check without Recover: now() retains transaction-start time.
func TestAuditSendingFenceRejectsExpiredLeaseAfterWait(t *testing.T) {
	system, app, p := auditSetup(t)
	m := auditMessage(t, system, app, p)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, e := system.Pool.Exec(ctx, "UPDATE messages SET lease_until=clock_timestamp()+interval '500 milliseconds' WHERE id=$1", m.ID)
	if e != nil {
		t.Fatal(e)
	}
	blocker, e := system.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback(context.Background())
	_, e = blocker.Exec(ctx, "SELECT id FROM attempts WHERE id=$1 FOR UPDATE", m.AttemptID)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- system.MarkSending(ctx, m) }()
	for i := 0; i < 100; i++ {
		var n int
		e = system.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%attempts%'").Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(700 * time.Millisecond)
	if e = blocker.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	markErr := <-done
	var expired bool
	if e = system.Pool.QueryRow(ctx, "SELECT lease_until<clock_timestamp() FROM messages WHERE id=$1", m.ID).Scan(&expired); e != nil {
		t.Fatal(e)
	}
	t.Logf("mark_error=%v wall_elapsed=%s lease_expired=%t", markErr, time.Since(started), expired)
	if markErr == nil && expired {
		t.Fatal("VIOLATION: successful sending fence uses stale transaction time after waiting past lease deadline")
	}
}

// Real worker + real SQL + local SMTP protocol socket with an ambiguous final reply.
func TestAuditAmbiguousDATARecovery(t *testing.T) {
	system, app, p := auditSetup(t)
	ctx := context.Background()
	vault, e := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	priv, pub, e := mailmsg.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	d, e := app.GetDomain(ctx, p.TenantID, p.TenantID+".example.test")
	if e != nil {
		t.Fatal(e)
	}
	encrypted, e := vault.Seal(priv, "dkim:"+d.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = system.Pool.Exec(ctx, "UPDATE sender_domains SET encrypted_key=$2,public_key=$3 WHERE id=$1", d.ID, encrypted, pub)
	if e != nil {
		t.Fatal(e)
	}
	request := domain.SendRequest{From: "audit@" + d.Name, To: []string{"audit@example.net"}, Text: "ambiguous DATA", Priority: "normal"}
	result, e := app.Enqueue(ctx, p, domain.ID(), "hash", request, true)
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "220 audit ESMTP\r\n")
		reader := bufio.NewReader(conn)
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				fmt.Fprint(conn, "250 audit\r\n")
			case strings.HasPrefix(line, "DATA"):
				fmt.Fprint(conn, "354 continue\r\n")
				for {
					line, e = reader.ReadString('\n')
					if e != nil {
						return
					}
					if line == ".\r\n" {
						accepted <- struct{}{}
						return
					}
				}
			default:
				fmt.Fprint(conn, "250 OK\r\n")
			}
		}
	}()
	w := worker.Worker{Store: system, Limiter: alwaysPermit{}, Vault: vault, MTA: &mta.Client{Address: listener.Addr().String(), Hostname: "mail.example.test", Timeout: time.Second}, NodeID: "audit-mta", AllowUnverified: true}
	job := domain.Job{MessageID: result.MessageIDs[0], TenantID: p.TenantID}
	if e = w.Handle(ctx, job); e != nil {
		t.Fatal(e)
	}
	<-accepted
	unknown, e := app.GetMessage(ctx, p.TenantID, job.MessageID)
	if e != nil || unknown.Status != "submission_unknown" {
		t.Fatalf("%+v %v", unknown, e)
	}
	if e = w.Handle(ctx, job); e != nil {
		t.Fatal(e)
	}
	var count int
	var aid string
	system.Pool.QueryRow(ctx, "SELECT attempt_count,attempt_id::text FROM messages WHERE id=$1", job.MessageID).Scan(&count, &aid)
	if count != 1 {
		t.Fatalf("ambiguous attempt blindly repeated %d", count)
	}
	file, queue := domain.ID(), "AUDIT"+strings.ReplaceAll(domain.ID(), "-", "")
	if e = system.ApplyLogLine(ctx, "audit-mta", file, 0, 100, queue, job.MessageID, aid, "", "", "", "", true); e != nil {
		t.Fatal(e)
	}
	if e = system.ApplyLogLine(ctx, "audit-mta", file, 100, 200, queue, "", "", "sent", request.To[0], "2.0.0", "local protocol sink accepted", true); e != nil {
		t.Fatal(e)
	}
	got, e := app.GetMessage(ctx, p.TenantID, job.MessageID)
	if e != nil || got.Status != "delivered" {
		t.Fatalf("%+v %v", got, e)
	}
	t.Logf("after_drop=%s redelivery_attempt_count=%d after_receipts=%s", unknown.Status, count, got.Status)
}

type alwaysPermit struct{}

func (alwaysPermit) Take(context.Context, string, string, int) (time.Duration, error) { return 0, nil }

type acknowledgedThenError struct{ b *broker.Broker }

func (p acknowledgedThenError) Publish(ctx context.Context, priority string, job domain.Job) error {
	if e := p.b.Publish(ctx, priority, job); e != nil {
		return e
	}
	return errors.New("injected process failure after publisher confirm before SQL commit")
}
func TestAuditConfirmedPublishThenSQLRollback(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	original, e := store.Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer original.Close()
	dbname := "audit_dispatch_" + strings.ReplaceAll(domain.ID(), "-", "")
	if _, e = original.Pool.Exec(ctx, "CREATE DATABASE "+dbname); e != nil {
		t.Fatal(e)
	}
	system, e := store.Open(ctx, strings.Replace(os.Getenv("TEST_DATABASE_URL"), "/selfmail_test", "/"+dbname, 1))
	if e != nil {
		t.Fatal(e)
	}
	defer system.Close()
	if e = system.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	app, e := store.Open(ctx, strings.Replace(os.Getenv("TEST_APP_DATABASE_URL"), "/selfmail_test", "/"+dbname, 1))
	if e != nil {
		t.Fatal(e)
	}
	defer app.Close()
	tenant, key, e := system.CreateTenant(ctx, "audit-dispatch", 100, 100)
	if e != nil {
		t.Fatal(e)
	}
	p, e := app.Authenticate(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	e = app.AddDomain(ctx, tenant, domain.Domain{ID: domain.ID(), Name: tenant + ".example.test", Selector: "audit", Token: "proof", PublicKey: "test", EncryptedKey: []byte("test")})
	if e != nil {
		t.Fatal(e)
	}
	request := domain.SendRequest{From: "audit@" + tenant + ".example.test", To: []string{"audit@example.net"}, Text: "test", Priority: "normal"}
	result, e := app.Enqueue(ctx, p, domain.ID(), "hash", request, true)
	if e != nil {
		t.Fatal(e)
	}
	b, e := broker.OpenNamespace(os.Getenv("RABBITMQ_URL"), "audit-"+domain.ID())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	defer func() {
		for _, priority := range []string{"critical", "normal", "bulk", "dead"} {
			b.Pub.QueueDelete(b.Prefix+"."+priority, false, false, false)
		}
		b.Pub.ExchangeDelete(b.Prefix+".dispatch", false, false)
		b.Pub.ExchangeDelete(b.Prefix+".dead", false, false)
	}()
	if _, e = system.PublishOne(ctx, acknowledgedThenError{b}); e == nil {
		t.Fatal("expected injected post-confirm failure")
	}
	if _, e = system.PublishOne(ctx, b); e != nil {
		t.Fatal(e)
	}
	queue, e := b.Pub.QueueInspect(b.Prefix + ".normal")
	if e != nil {
		t.Fatal(e)
	}
	if queue.Messages != 2 {
		t.Fatalf("expected duplicate broker refs, got %d", queue.Messages)
	}
	calls, wins := 0, 0
	consumeCtx, stop := context.WithCancel(ctx)
	defer stop()
	e = b.Consume(consumeCtx, "normal", func(ctx context.Context, job domain.Job) error {
		calls++
		m, e := system.Claim(ctx, job, "audit-mta")
		if e == nil {
			wins++
			if e = system.MarkSending(ctx, m); e != nil {
				return e
			}
			e = system.FinishSubmission(ctx, m, "submitted", "AUDIT", "local state check")
		} else if errors.Is(e, domain.ErrNotFound) {
			e = nil
		}
		if calls == 2 {
			stop()
		}
		return e
	})
	if !errors.Is(e, context.Canceled) || wins != 1 {
		t.Fatalf("consume=%v wins=%d", e, wins)
	}
	got, e := app.GetMessage(context.Background(), p.TenantID, result.MessageIDs[0])
	if e != nil || got.AttemptCount != 1 {
		t.Fatalf("%+v %v", got, e)
	}
	t.Logf("broker_refs=%d consumed=%d successful_SQL_claims=%d submission_attempts=%d database=%s", queue.Messages, calls, wins, got.AttemptCount, dbname)
}

func auditSetup(t *testing.T) (*store.Store, *store.Store, domain.Principal) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	root, e := store.Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(root.Close)
	db := "regression_" + strings.ReplaceAll(domain.ID(), "-", "")
	if _, e = root.Pool.Exec(ctx, "CREATE DATABASE "+db); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := root.Pool.Exec(context.Background(), "DROP DATABASE "+db+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
	})
	system, e := store.Open(ctx, strings.Replace(os.Getenv("TEST_DATABASE_URL"), "/selfmail_test", "/"+db, 1))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(system.Close)
	if e = system.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	app, e := store.Open(ctx, strings.Replace(os.Getenv("TEST_APP_DATABASE_URL"), "/selfmail_test", "/"+db, 1))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(app.Close)
	id, key, e := system.CreateTenant(ctx, "audit-"+domain.ID(), 100, 10000)
	if e != nil {
		t.Fatal(e)
	}
	p, e := app.Authenticate(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	e = app.AddDomain(ctx, id, domain.Domain{ID: domain.ID(), Name: id + ".example.test", Selector: "audit", Token: "proof", PublicKey: "test", EncryptedKey: []byte("test")})
	if e != nil {
		t.Fatal(e)
	}
	return system, app, p
}
func auditMessage(t *testing.T, system, app *store.Store, p domain.Principal) domain.Message {
	t.Helper()
	ctx := context.Background()
	r, e := app.Enqueue(ctx, p, domain.ID(), "hash", domain.SendRequest{From: "audit@" + p.TenantID + ".example.test", To: []string{"audit@example.net"}, Text: "test", Priority: "normal"}, true)
	if e != nil {
		t.Fatal(e)
	}
	m, e := system.Claim(ctx, domain.Job{MessageID: r.MessageIDs[0], TenantID: p.TenantID}, "audit-mta")
	if e != nil {
		t.Fatal(e)
	}
	return m
}

// Desired invariant: successful sending fence and preparing recovery cannot both win.
// Expected to FAIL on the audited source, preserving the exact race reproduction.
func TestAuditSendingRecoveryFence(t *testing.T) {
	system, app, p := auditSetup(t)
	m := auditMessage(t, system, app, p)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, e := system.Pool.Exec(ctx, "UPDATE messages SET lease_until=clock_timestamp()+interval '2 seconds' WHERE id=$1", m.ID)
	if e != nil {
		t.Fatal(e)
	}
	blocker, e := system.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback(context.Background())
	_, e = blocker.Exec(ctx, "SELECT id FROM attempts WHERE id=$1 FOR UPDATE", m.AttemptID)
	if e != nil {
		t.Fatal(e)
	}
	sending := make(chan error, 1)
	go func() { sending <- system.MarkSending(ctx, m) }()
	for i := 0; i < 100; i++ {
		var n int
		e = system.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%attempts%'").Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
		if i == 99 {
			t.Fatal("sending did not block")
		}
	}
	time.Sleep(2100 * time.Millisecond)
	recovering := make(chan error, 1)
	go func() { recovering <- system.Recover(ctx) }()
	time.Sleep(50 * time.Millisecond)
	if e = blocker.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	markErr, recoverErr := <-sending, <-recovering
	got, e := app.GetMessage(ctx, p.TenantID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("mark_error=%v recover_error=%v message_status=%s", markErr, recoverErr, got.Status)
	if recoverErr != nil {
		t.Fatal(recoverErr)
	}
	if markErr == nil && got.Status == "queued" {
		t.Fatal("VIOLATION: sending fence succeeded while recovery made the same message eligible for another SMTP attempt")
	}
}

// Desired invariant: an accepted decoded payload fits configured MTA wire limit.
func TestAuditAcceptedMIMEFitsPostfix(t *testing.T) {
	r := domain.SendRequest{From: "audit@example.test", To: []string{"audit@example.net"}, Text: strings.Repeat("я", 2*1024*1024)}
	if e := domain.Validate(&r); e != nil {
		t.Fatal(e)
	}
	raw, e := mailmsg.Build(domain.Message{ID: domain.ID(), AttemptID: domain.ID(), From: r.From, Recipient: r.To[0], CreatedAt: time.Now(), Payload: r}, "mail.example.test")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("decoded_text_bytes=%d wire_mime_bytes=%d postfix_limit=%d", len(r.Text), len(raw), 10485760)
	if len(raw) > 10485760 {
		t.Fatal("VIOLATION: valid API payload expands beyond configured Postfix message_size_limit")
	}
}

type renderRepo struct {
	application.Repository
	template domain.Template
}

func (r renderRepo) Replay(context.Context, string, string, string) (domain.SendResult, bool, error) {
	return domain.SendResult{}, false, nil
}
func (r renderRepo) GetTemplate(context.Context, string, string) (domain.Template, error) {
	return r.template, nil
}
func (r renderRepo) Enqueue(ctx context.Context, p domain.Principal, key, hash string, req domain.SendRequest, bypass bool) (domain.SendResult, error) {
	return domain.SendResult{}, ctx.Err()
}

// Empty-output loops bypass the output byte cap. Deadline must bound CPU work.
func TestAuditTemplateRespectsCanceledRequest(t *testing.T) {
	items := make([]any, 1000)
	for i := range items {
		items[i] = i
	}
	svc := application.Service{Repo: renderRepo{template: domain.Template{Subject: "test", Text: "{{range .Items}}{{range $.Items}}{{end}}{{end}}"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, e := svc.Send(ctx, domain.Principal{TenantID: domain.ID(), Scopes: []string{"messages:write"}}, "audit-template", domain.SendRequest{From: "a@example.test", To: []string{"b@example.net"}, Template: "audit", Variables: map[string]any{"Items": items}})
	elapsed := time.Since(started)
	t.Logf("elapsed=%s request_deadline=10ms result=%v", elapsed, e)
	if e == nil || (!errors.Is(e, context.DeadlineExceeded) && !application.IsValidation(e)) {
		t.Fatal(e)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatal("VIOLATION: canceled request keeps evaluating empty nested template loops")
	}
}

// One undecryptable endpoint must not starve all other tenants' ready jobs.
func TestAuditPoisonWebhookDoesNotStarve(t *testing.T) {
	system, app, p := auditSetup(t)
	ctx := context.Background()
	vault, e := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer endpoint.Close()
	poisonID, goodID := domain.ID(), domain.ID()
	goodSecret, e := vault.Seal([]byte("testsecret"), "webhook:"+goodID)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.AddWebhook(ctx, p.TenantID, domain.Webhook{ID: poisonID, URL: endpoint.URL}, []byte("bad")); e != nil {
		t.Fatal(e)
	}
	if e = app.AddWebhook(ctx, p.TenantID, domain.Webhook{ID: goodID, URL: endpoint.URL}, goodSecret); e != nil {
		t.Fatal(e)
	}
	m := auditMessage(t, system, app, p)
	_, e = system.Pool.Exec(ctx, "UPDATE webhook_jobs SET available_at=now()-interval '1 hour' WHERE endpoint_id=$1", poisonID)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		_, e = worker.WebhookOne(ctx, system, vault, endpoint.Client())
		t.Logf("iteration=%d err=%v", i, e)
	}
	t.Logf("good_endpoint_calls=%d message=%s", calls.Load(), m.ID)
	if calls.Load() == 0 {
		t.Fatal("VIOLATION: permanent decrypt failure repeatedly selects the same job and blocks healthy endpoint delivery")
	}
	var event string
	if e = system.Pool.QueryRow(ctx, "SELECT event_id::text FROM webhook_jobs WHERE endpoint_id=$1 AND status='dead' LIMIT 1", poisonID).Scan(&event); e != nil {
		t.Fatal(e)
	}
	replacement, e := vault.Seal([]byte("replacement-secret-at-least-32-bytes"), "webhook:"+poisonID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = system.RepairWebhook(ctx, p.TenantID, poisonID, "receiver secret replaced after corruption", replacement, false, 100); e != nil {
		t.Fatal(e)
	}
	n, e := system.RepairWebhook(ctx, p.TenantID, poisonID, "replay original event after secret repair", nil, true, 100)
	if e != nil || n == 0 {
		t.Fatal("bounded replay failed", n, e)
	}
	for i := int64(0); i < n; i++ {
		if _, e = worker.WebhookOne(ctx, system, vault, endpoint.Client()); e != nil {
			t.Fatal(e)
		}
	}
	var delivered bool
	if e = system.Pool.QueryRow(ctx, "SELECT status='delivered' FROM webhook_jobs WHERE endpoint_id=$1 AND event_id=$2", poisonID, event).Scan(&delivered); e != nil || !delivered {
		t.Fatal("original event ID was not replayed", e)
	}
}
