package store

import (
	"context"
	"errors"
	"os"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/jackc/pgx/v5"
)

func (s *Store) LogCursor(ctx context.Context, node, file string) (n int64, e error) {
	e = s.Pool.QueryRow(ctx, "SELECT offset_bytes FROM mta_log_cursors WHERE node_id=$1 AND file_id=$2", node, file).Scan(&n)
	if errors.Is(e, pgx.ErrNoRows) {
		e = nil
	}
	return
}
func (s *Store) ArchiveComplete(ctx context.Context, node, file string) error {
	_, e := s.Pool.Exec(ctx, "UPDATE mta_log_cursors SET completed_at=coalesce(completed_at,clock_timestamp()) WHERE node_id=$1 AND file_id=$2", node, file)
	return e
}
func (s *Store) ApplyLogLine(ctx context.Context, node, file string, offset, next int64, queue, message, attempt, status, recipient, dsn, diagnostic string, parsed bool) error {
	if parsed {
		if e := s.recordLog(node, file, offset, next, queue, message, attempt, status, recipient, dsn, diagnostic); e != nil {
			return e
		}
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "INSERT INTO mta_log_lines(node_id,file_id,offset_bytes) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", node, file, offset)
	if e != nil {
		return e
	}
	if tag.RowsAffected() > 0 && parsed {
		if _, e = tx.Exec(ctx, "INSERT INTO mta_receipts(node_id,queue_id) VALUES($1,$2) ON CONFLICT DO NOTHING", node, queue); e != nil {
			return e
		}
		if message != "" {
			var valid bool
			if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM attempts WHERE id=$1 AND message_id=$2 AND node_id=$3)", attempt, message, node).Scan(&valid); e != nil {
				return e
			}
			if valid {
				if _, e = tx.Exec(ctx, "UPDATE mta_receipts SET message_id=$3,attempt_id=$4,updated_at=now() WHERE node_id=$1 AND queue_id=$2", node, queue, message, attempt); e != nil {
					return e
				}
			}
		}
		if status == "active" {
			if _, e = tx.Exec(ctx, "UPDATE mta_receipts SET activated=true,updated_at=now() WHERE node_id=$1 AND queue_id=$2", node, queue); e != nil {
				return e
			}
		}
		if status != "" && status != "active" {
			if _, e = tx.Exec(ctx, "UPDATE mta_receipts SET status=$3,recipient=$4,dsn=$5,diagnostic=$6,activated=true,updated_at=now() WHERE node_id=$1 AND queue_id=$2 AND status NOT IN ('sent','bounced')", node, queue, status, recipient, dsn, diagnostic); e != nil {
				return e
			}
		}
		if e = applyReceipt(ctx, tx, node, queue); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mta_log_cursors(node_id,file_id,offset_bytes) VALUES($1,$2,$3) ON CONFLICT(node_id,file_id) DO UPDATE SET offset_bytes=GREATEST(mta_log_cursors.offset_bytes,EXCLUDED.offset_bytes),updated_at=now()", node, file, next); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func applyReceipt(ctx context.Context, tx pgx.Tx, node, queue string) error {
	var mid, aid *string
	var status, recipient, dsn, diagnostic string
	var activated bool
	e := tx.QueryRow(ctx, "SELECT message_id::text,attempt_id::text,status,recipient,dsn,diagnostic,activated FROM mta_receipts WHERE node_id=$1 AND queue_id=$2", node, queue).Scan(&mid, &aid, &status, &recipient, &dsn, &diagnostic, &activated)
	if e != nil {
		return e
	}
	if mid == nil || aid == nil || !activated {
		return nil
	}
	var tenant, current, active, expected string
	if e = tx.QueryRow(ctx, "SELECT tenant_id::text,status,attempt_id::text,recipient FROM messages WHERE id=$1 FOR UPDATE", *mid).Scan(&tenant, &current, &active, &expected); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	}
	if active != *aid {
		return nil
	}
	if recipient != "" && recipient != expected {
		return nil
	}
	if _, e = tx.Exec(ctx, "UPDATE attempts SET phase='accepted',queue_id=$2,updated_at=now() WHERE id=$1", *aid, queue); e != nil {
		return e
	}
	if current == "dispatching" || current == "submission_unknown" {
		if _, e = tx.Exec(ctx, "UPDATE messages SET status='submitted',queue_id=$2,lease_until=NULL,updated_at=now() WHERE id=$1", *mid, queue); e != nil {
			return e
		}
		if e = EventTx(ctx, tx, tenant, *mid, "submitted", map[string]string{"queue_id": queue, "source": "postfix_log"}); e != nil {
			return e
		}
		current = "submitted"
	}
	if recipient != "" && recipient != expected {
		return nil
	}
	target := map[string]string{"sent": "delivered", "deferred": "deferred", "bounced": "bounced"}[status]
	if target == "" || target == current || current == "delivered" || current == "bounced" || current == "failed" || current == "canceled" || current == "suppressed" {
		return nil
	}
	if _, e = tx.Exec(ctx, "UPDATE messages SET status=$2,queue_id=$3,last_error=$4,lease_until=NULL,updated_at=now() WHERE id=$1", *mid, target, queue, diagnostic); e != nil {
		return e
	}
	if target == "bounced" && HardBounce(dsn) {
		if _, e = tx.Exec(ctx, "INSERT INTO suppressions(tenant_id,recipient,reason) VALUES($1,$2,'hard_bounce:'||$3) ON CONFLICT DO NOTHING", tenant, recipient, dsn); e != nil {
			return e
		}
	}
	return EventTx(ctx, tx, tenant, *mid, target, map[string]string{"queue_id": queue, "dsn": dsn, "diagnostic": diagnostic, "source": "postfix_log"})
}
func HardBounce(dsn string) bool { return dsn == "5.1.1" || dsn == "5.1.2" || dsn == "5.2.1" }
func (s *Store) AttemptRecipient(ctx context.Context, attempt string) (recipient string, e error) {
	e = s.Pool.QueryRow(ctx, "SELECT m.recipient FROM messages m JOIN attempts a ON a.message_id=m.id WHERE a.id=$1", attempt).Scan(&recipient)
	if errors.Is(e, pgx.ErrNoRows) {
		if s.Journal != nil {
			r, err := s.Journal.Get(journal.ID("intent", attempt))
			if err == nil {
				return r.Recipient, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
		e = domain.ErrNotFound
	}
	return
}
func (s *Store) ApplyDSN(ctx context.Context, attempt, recipient, action, dsn string) error {
	if s.Journal != nil {
		if e := s.Journal.Put(journal.Record{ID: journal.ID("dsn", attempt, recipient, action, dsn), Kind: "dsn", Attempt: attempt, Recipient: recipient, Action: action, DSN: dsn}); e != nil {
			return e
		}
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var mid, tenant, current, expected string
	e = tx.QueryRow(ctx, "SELECT m.id::text,m.tenant_id::text,m.status,m.recipient FROM messages m JOIN attempts a ON a.message_id=m.id WHERE a.id=$1 AND m.attempt_id=a.id FOR UPDATE OF m", attempt).Scan(&mid, &tenant, &current, &expected)
	if errors.Is(e, pgx.ErrNoRows) {
		if s.Journal != nil {
			return tx.Commit(ctx)
		} // Evidence survives metadata retention or SQL rollback.
		return domain.ErrNotFound
	}
	if e != nil {
		return e
	}
	if recipient != expected {
		return domain.ErrForbidden
	}
	target := ""
	if action == "failed" {
		target = "bounced"
	} else if action == "delayed" && current != "delivered" && current != "bounced" {
		target = "deferred"
	}
	if target == "" || target == current || current == "bounced" {
		return tx.Commit(ctx)
	}
	if _, e = tx.Exec(ctx, "UPDATE messages SET status=$2,last_error=$3,updated_at=now() WHERE id=$1", mid, target, "remote DSN: "+dsn); e != nil {
		return e
	}
	if target == "bounced" && HardBounce(dsn) {
		if _, e = tx.Exec(ctx, "INSERT INTO suppressions(tenant_id,recipient,reason) VALUES($1,$2,'hard_bounce:'||$3) ON CONFLICT DO NOTHING", tenant, recipient, dsn); e != nil {
			return e
		}
	}
	if e = EventTx(ctx, tx, tenant, mid, target, map[string]string{"source": "dsn", "dsn": dsn}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
