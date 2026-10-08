package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5"
)

type Publisher interface {
	Publish(context.Context, string, domain.Job) error
}

func (s *Store) PublishOne(ctx context.Context, p Publisher) (bool, error) {
	release, e := s.DeliveryPermit(ctx)
	if e != nil {
		return false, e
	}
	defer release()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var id int64
	var job domain.Job
	var priority string
	e = tx.QueryRow(ctx, "SELECT id,tenant_id::text,message_id::text,priority FROM outbox WHERE published_at IS NULL AND available_at<=now() ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&id, &job.TenantID, &job.MessageID, &priority)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if e = p.Publish(ctx, priority, job); e != nil {
		return false, e
	}
	if _, e = tx.Exec(ctx, "UPDATE outbox SET published_at=now() WHERE id=$1", id); e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}
func (s *Store) Claim(ctx context.Context, job domain.Job, node string) (m domain.Message, e error) {
	m.NodeID = node
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return m, e
	}
	defer tx.Rollback(ctx)
	var payload, raw []byte
	var due, expires time.Time
	var attempt *string
	e = tx.QueryRow(ctx, "SELECT "+messageCols+",next_attempt_at,expires_at,attempt_id::text FROM messages WHERE tenant_id=$1 AND id=$2 FOR UPDATE", job.TenantID, job.MessageID).Scan(&m.ID, &m.TenantID, &m.BatchID, &m.From, &m.Recipient, &m.Priority, &m.Status, &m.AttemptCount, &m.LastError, &m.QueueID, &m.CreatedAt, &m.UpdatedAt, &due, &expires, &attempt)
	if errors.Is(e, pgx.ErrNoRows) {
		return m, domain.ErrNotFound
	}
	if e != nil {
		return m, e
	}
	if m.Status != "queued" || due.After(time.Now()) {
		return m, domain.ErrNotFound
	}
	if e = s.EnsureAcceptance(ctx, m.BatchID, m.TenantID); e != nil {
		return m, e
	}
	var banned bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM suppressions WHERE tenant_id=$1 AND recipient=$2)", m.TenantID, m.Recipient).Scan(&banned); e != nil {
		return m, e
	}
	if banned || m.AttemptCount >= 8 || expires.Before(time.Now()) {
		status := "failed"
		reason := "submission retry budget or message TTL exhausted"
		if banned {
			status = "suppressed"
			reason = "recipient suppressed"
		}
		if _, e = tx.Exec(ctx, "UPDATE messages SET status=$2,last_error=$3,updated_at=now() WHERE id=$1", m.ID, status, reason); e != nil {
			return m, e
		}
		if e = EventTx(ctx, tx, m.TenantID, m.ID, status, map[string]string{"reason": reason}); e != nil {
			return m, e
		}
		if e = tx.Commit(ctx); e != nil {
			return m, e
		}
		return m, domain.ErrNotFound
	}
	if e = tx.QueryRow(ctx, "SELECT p.payload,p.raw_mime,b.idempotency_digest,b.fingerprint FROM batch_payloads p JOIN batches b ON b.id=p.batch_id WHERE p.tenant_id=$1 AND p.batch_id=$2", m.TenantID, m.BatchID).Scan(&payload, &raw, &m.KeyDigest, &m.Fingerprint); e != nil {
		return m, e
	}
	if e = json.Unmarshal(payload, &m.Payload); e != nil {
		return m, e
	}
	m.Payload.Raw = raw
	m.AttemptID = domain.ID()
	m.AttemptCount++
	if _, e = tx.Exec(ctx, "INSERT INTO attempts(id,tenant_id,message_id,phase,node_id) VALUES($1,$2,$3,'preparing',$4)", m.AttemptID, m.TenantID, m.ID, node); e != nil {
		return m, e
	}
	if _, e = tx.Exec(ctx, "UPDATE messages SET status='dispatching',attempt_id=$2,attempt_count=attempt_count+1,lease_until=clock_timestamp()+interval '2 minutes',updated_at=now() WHERE id=$1", m.ID, m.AttemptID); e != nil {
		return m, e
	}
	if e = EventTx(ctx, tx, m.TenantID, m.ID, "dispatching", map[string]string{"attempt_id": m.AttemptID}); e != nil {
		return m, e
	}
	return m, tx.Commit(ctx)
}
func (s *Store) MarkSending(ctx context.Context, m domain.Message) error {
	release, e := s.DeliveryPermit(ctx)
	if e != nil {
		return e
	}
	defer release()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var paused, banned bool
	if e = tx.QueryRow(ctx, "SELECT paused FROM tenants WHERE id=$1 FOR SHARE", m.TenantID).Scan(&paused); e != nil {
		return e
	}
	if paused {
		return domain.ErrUnavailable
	}
	// Every state transition serializes on message, then active attempt. Do not
	// read the attempt phase before acquiring both locks: recovery may be waiting.
	var status, phase string
	var active *string
	var lease *time.Time
	var expires time.Time
	e = tx.QueryRow(ctx, "SELECT status,attempt_id::text,lease_until,expires_at FROM messages WHERE id=$1 AND tenant_id=$2 FOR UPDATE", m.ID, m.TenantID).Scan(&status, &active, &lease, &expires)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if e != nil {
		return e
	}
	if status != "dispatching" || active == nil || *active != m.AttemptID || lease == nil {
		return domain.ErrConflict
	}
	if e = tx.QueryRow(ctx, "SELECT phase FROM attempts WHERE id=$1 AND message_id=$2 FOR UPDATE", *active, m.ID).Scan(&phase); e != nil {
		return e
	}
	// Refresh suppression after waiting for state locks.
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM suppressions WHERE tenant_id=$1 AND recipient=$2)", m.TenantID, m.Recipient).Scan(&banned); e != nil {
		return e
	}
	if banned {
		return domain.ErrSuppressed
	}
	var now time.Time
	if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); e != nil {
		return e
	}
	if phase != "preparing" || !lease.After(now) || !expires.After(now) {
		return domain.ErrConflict
	}
	if e = s.recordIntent(m); e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); e != nil {
		return e
	}
	if !lease.After(now) || !expires.After(now) {
		return domain.ErrConflict
	}
	t, e := tx.Exec(ctx, "UPDATE attempts SET phase='sending',updated_at=now() WHERE id=$1 AND phase='preparing'", *active)
	if e != nil {
		return e
	}
	if t.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return tx.Commit(ctx)
}

// FinishSubmission never regresses an outcome already observed from Postfix logs.
func (s *Store) FinishSubmission(ctx context.Context, m domain.Message, status, queueID, diagnostic string) error {
	if e := s.recordOutcome(m, status, queueID, diagnostic); e != nil {
		return e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var current, attempt string
	if e = tx.QueryRow(ctx, "SELECT status,attempt_id::text FROM messages WHERE id=$1 FOR UPDATE", m.ID).Scan(&current, &attempt); e != nil {
		return e
	}
	if attempt != m.AttemptID || (current != "dispatching" && current != "submission_unknown") {
		return tx.Commit(ctx)
	}
	phase := "unknown"
	if status == "submitted" {
		phase = "accepted"
	}
	if _, e = tx.Exec(ctx, "UPDATE attempts SET phase=$2,queue_id=$3,updated_at=now() WHERE id=$1", m.AttemptID, phase, queueID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE messages SET status=$2,queue_id=$3,last_error=$4,lease_until=NULL,updated_at=now() WHERE id=$1", m.ID, status, queueID, diagnostic); e != nil {
		return e
	}
	if e = EventTx(ctx, tx, m.TenantID, m.ID, status, map[string]string{"attempt_id": m.AttemptID, "queue_id": queueID, "diagnostic": diagnostic}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 10 {
		attempt = 10
	}
	d := time.Duration(1<<uint(attempt-1)) * 5 * time.Second
	if d > time.Hour {
		d = time.Hour
	}
	return d
}
func (s *Store) Retry(ctx context.Context, m domain.Message, reason string, permanent bool, delay time.Duration) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var status, attempt string
	if e = tx.QueryRow(ctx, "SELECT status,attempt_id::text FROM messages WHERE id=$1 FOR UPDATE", m.ID).Scan(&status, &attempt); e != nil {
		return e
	}
	if status != "dispatching" || attempt != m.AttemptID {
		return tx.Commit(ctx)
	}
	var phase string
	if e = tx.QueryRow(ctx, "SELECT phase FROM attempts WHERE id=$1", attempt).Scan(&phase); e != nil {
		return e
	}
	if e = s.recordOutcome(m, "rejected", "", reason); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE attempts SET phase='rejected',updated_at=now() WHERE id=$1", attempt); e != nil {
		return e
	}
	status = "queued"
	if permanent {
		status = "failed"
	}
	if _, e = tx.Exec(ctx, "UPDATE messages SET status=$2,last_error=$3,lease_until=NULL,next_attempt_at=now()+$4::interval,attempt_count=GREATEST(0,attempt_count-$5),updated_at=now() WHERE id=$1", m.ID, status, reason, Interval(delay), boolInt(phase == "preparing" && !permanent)); e != nil {
		return e
	}
	if !permanent {
		if _, e = tx.Exec(ctx, "INSERT INTO outbox(tenant_id,message_id,priority,available_at) VALUES($1,$2,$3,now()+$4::interval)", m.TenantID, m.ID, m.Priority, Interval(delay)); e != nil {
			return e
		}
	}
	if e = EventTx(ctx, tx, m.TenantID, m.ID, status, map[string]string{"reason": reason, "attempt_id": attempt}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Recover(ctx context.Context) error {
	release, e := s.DeliveryPermit(ctx)
	if e != nil {
		return e
	}
	defer release()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, "SELECT m.id::text,m.tenant_id::text,m.attempt_id::text,m.priority FROM messages m WHERE m.status='dispatching' AND m.lease_until<clock_timestamp() ORDER BY m.lease_until,m.id FOR UPDATE OF m SKIP LOCKED LIMIT 100")
	if e != nil {
		return e
	}
	type stale struct{ id, tenant, attempt, priority, phase string }
	var items []stale
	for rows.Next() {
		var i stale
		if e = rows.Scan(&i.id, &i.tenant, &i.attempt, &i.priority); e != nil {
			rows.Close()
			return e
		}
		items = append(items, i)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, i := range items {
		if e = tx.QueryRow(ctx, "SELECT phase FROM attempts WHERE id=$1 AND message_id=$2 FOR UPDATE", i.attempt, i.id).Scan(&i.phase); e != nil {
			return e
		}
		status := "submission_unknown"
		phase := "unknown"
		if i.phase == "preparing" {
			status = "queued"
			phase = "rejected"
		}
		if _, e = tx.Exec(ctx, "UPDATE messages SET status=$2,lease_until=NULL,last_error='worker lease expired',next_attempt_at=now(),attempt_count=GREATEST(0,attempt_count-$3),updated_at=now() WHERE id=$1", i.id, status, boolInt(i.phase == "preparing")); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE attempts SET phase=$2,updated_at=now() WHERE id=$1", i.attempt, phase); e != nil {
			return e
		}
		if status == "queued" {
			if _, e = tx.Exec(ctx, "INSERT INTO outbox(tenant_id,message_id,priority) VALUES($1,$2,$3)", i.tenant, i.id, i.priority); e != nil {
				return e
			}
		}
		if e = EventTx(ctx, tx, i.tenant, i.id, status, map[string]string{"reason": "worker lease expired"}); e != nil {
			return e
		}
	}
	// PostgreSQL retains the work even after broker loss or exhausted consumer deliveries.
	_, e = tx.Exec(ctx, "INSERT INTO outbox(tenant_id,message_id,priority) SELECT m.tenant_id,m.id,m.priority FROM messages m WHERE m.status='queued' AND m.next_attempt_at<=now() AND NOT EXISTS(SELECT 1 FROM outbox o WHERE o.message_id=m.id AND o.published_at IS NULL) AND NOT EXISTS(SELECT 1 FROM outbox o WHERE o.message_id=m.id AND o.published_at>now()-interval '5 minutes') ORDER BY m.next_attempt_at,m.id FOR UPDATE OF m SKIP LOCKED LIMIT 100")
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func Interval(d time.Duration) string { return fmt.Sprintf("%.3f seconds", d.Seconds()) }
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
