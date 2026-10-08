package store

import (
	"context"
	"strconv"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) EventsPage(ctx context.Context, tenant, id, cursor string, limit int) (out []domain.Event, next string, e error) {
	out = []domain.Event{}
	after := int64(0)
	if cursor != "" {
		after, e = strconv.ParseInt(cursor, 10, 64)
		if e != nil || after < 0 {
			return out, "", domain.Invalid("cursor", "non-negative event sequence required")
		}
	}
	if limit < 1 || limit > 1000 {
		return out, "", domain.Invalid("limit", "must be 1..1000")
	}
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE id=$1 AND tenant_id=$2)", id, tenant).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return messageAbsent(ctx, tx, tenant, id)
		}
		rows, e := tx.Query(ctx, "SELECT id::text,message_id::text,type,details,created_at,sequence FROM events WHERE tenant_id=$1 AND message_id=$2 AND sequence>$3 ORDER BY sequence LIMIT $4", tenant, id, after, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var ev domain.Event
			if e = rows.Scan(&ev.ID, &ev.MessageID, &ev.Type, &ev.Details, &ev.CreatedAt, &ev.Sequence); e != nil {
				return e
			}
			out = append(out, ev)
		}
		return rows.Err()
	})
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[len(out)-1].Sequence, 10)
	}
	return
}
