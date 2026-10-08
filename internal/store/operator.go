package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ResolveUnknown(ctx context.Context, id, reason string) error {
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
	var tenant, attempt, priority, status string
	e = tx.QueryRow(ctx, "SELECT tenant_id::text,attempt_id::text,priority,status FROM messages WHERE id=$1 FOR UPDATE", id).Scan(&tenant, &attempt, &priority, &status)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if e != nil {
		return e
	}
	if status != "submission_unknown" {
		return domain.ErrConflict
	}
	var payload bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM batch_payloads p JOIN messages m ON m.batch_id=p.batch_id WHERE m.id=$1)", id).Scan(&payload); e != nil {
		return e
	}
	if !payload {
		return domain.ErrConflict
	}
	if _, e = tx.Exec(ctx, "UPDATE attempts SET phase='rejected',updated_at=now() WHERE id=$1", attempt); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE messages SET status='queued',lease_until=NULL,next_attempt_at=now(),expires_at=GREATEST(expires_at,now()+interval '1 hour'),updated_at=now() WHERE id=$1", id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO outbox(tenant_id,message_id,priority) VALUES($1,$2,$3)", tenant, id, priority); e != nil {
		return e
	}
	details, _ := json.Marshal(map[string]string{"message_id": id, "reason": reason})
	if _, e = tx.Exec(ctx, "INSERT INTO audit_log(tenant_id,action,details) VALUES($1,'unknown.manually_retried',$2)", tenant, details); e != nil {
		return e
	}
	if e = EventTx(ctx, tx, tenant, id, "queued", map[string]string{"reason": reason, "source": "operator"}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
