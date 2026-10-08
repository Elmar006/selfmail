package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5"
)

// RepairWebhook keeps original event IDs: receivers deduplicate callbacks even
// when the HTTP result of an earlier delivery was ambiguous.
func (s *Store) RepairWebhook(ctx context.Context, tenant, id, reason string, encrypted []byte, replay bool, limit int) (n int64, e error) {
	if len(reason) < 10 || len(reason) > 512 || limit < 1 || limit > 1000 {
		return 0, domain.Invalid("repair", "reason 10..512 bytes, limit 1..1000 required")
	}
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		var found string
		e := tx.QueryRow(ctx, "SELECT id::text FROM webhook_endpoints WHERE id=$1 AND tenant_id=$2 FOR UPDATE", id, tenant).Scan(&found)
		if errors.Is(e, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if e != nil {
			return e
		}
		if len(encrypted) > 0 {
			if _, e = tx.Exec(ctx, "UPDATE webhook_endpoints SET encrypted_secret=$2,enabled=true WHERE id=$1", id, encrypted); e != nil {
				return e
			}
		}
		if replay {
			var enabled bool
			if e = tx.QueryRow(ctx, "SELECT enabled FROM webhook_endpoints WHERE id=$1", id).Scan(&enabled); e != nil {
				return e
			}
			if !enabled {
				return domain.ErrConflict
			}
			tag, e := tx.Exec(ctx, `WITH selected AS(SELECT id FROM webhook_jobs WHERE endpoint_id=$1 AND status='dead' ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT $2) UPDATE webhook_jobs SET status='pending',attempt_count=0,last_error='',resolved_at=NULL,available_at=clock_timestamp() WHERE id IN(SELECT id FROM selected)`, id, limit)
			if e != nil {
				return e
			}
			n = tag.RowsAffected()
		}
		detail, _ := json.Marshal(map[string]any{"endpoint_id": id, "replayed": n, "secret_replaced": len(encrypted) > 0, "reason": reason})
		_, e = tx.Exec(ctx, "INSERT INTO audit_log(tenant_id,action,details) VALUES($1,'webhook_repair',$2)", tenant, detail)
		return e
	})
	return
}
func (s *Store) ResolveDeadWebhook(ctx context.Context, tenant, id, reason string) (n int64, e error) {
	if len(reason) < 10 || len(reason) > 512 {
		return 0, domain.Invalid("reason", "10..512 bytes required")
	}
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		var found string
		if e := tx.QueryRow(ctx, "SELECT id::text FROM webhook_endpoints WHERE tenant_id=$1 AND id=$2 FOR UPDATE", tenant, id).Scan(&found); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, "UPDATE webhook_jobs SET resolved_at=clock_timestamp() WHERE id IN(SELECT id FROM webhook_jobs WHERE endpoint_id=$1 AND status='dead' AND resolved_at IS NULL ORDER BY available_at,id LIMIT 1000)", id)
		if e != nil {
			return e
		}
		n = tag.RowsAffected()
		detail, _ := json.Marshal(map[string]any{"endpoint_id": id, "resolved": n, "reason": reason})
		_, e = tx.Exec(ctx, "INSERT INTO audit_log(tenant_id,action,details) VALUES($1,'webhook_dead_resolved',$2)", tenant, detail)
		return e
	})
	return
}
