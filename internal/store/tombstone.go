package store

import (
	"context"
	"encoding/json"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5"
)

func messageAbsent(ctx context.Context, tx pgx.Tx, tenant, id string) error {
	probe, _ := json.Marshal([]string{id})
	var retained bool
	if e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM batches WHERE tenant_id=$1 AND (response->'message_ids') @> $2::jsonb)", tenant, probe).Scan(&retained); e != nil {
		return e
	}
	if retained {
		return domain.ErrGone
	}
	return domain.ErrNotFound
}
