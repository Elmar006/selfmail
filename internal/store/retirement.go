package store

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/jackc/pgx/v5"
)

// Retirement is independent of SQL rollback. It certifies that retention has
// already established a terminal, sufficiently old batch before deleting it.
// A restore must not recreate its expired metadata or bind an old key to a new
// operation. The marker is durable before the cleanup transaction commits.
func (s *Store) recordRetirement(batch, tenant, kind string) error {
	if s.Journal == nil {
		return nil
	}
	return s.Journal.Put(journal.Record{ID: journal.ID(kind, batch), Kind: kind, Batch: batch, Tenant: tenant})
}

func (s *Store) retirement(batch string) (metadata, idempotency bool, e error) {
	if s.Journal == nil {
		return false, false, nil
	}
	for _, kind := range []string{"retention-idempotency", "retention-metadata"} {
		r, err := s.Journal.Get(journal.ID(kind, batch))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, false, err
		}
		if r.Kind != kind || r.Batch != batch || !domain.ValidID(r.Tenant) {
			return false, false, fmt.Errorf("invalid retirement evidence")
		}
		if kind == "retention-idempotency" {
			return true, true, nil
		}
		metadata = true
	}
	return metadata, false, nil
}

func (s *Store) retiredAttempt(attempt string) (bool, error) {
	r, e := s.Journal.Get(journal.ID("intent", attempt))
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	retired, _, e := s.retirement(r.Batch)
	return retired, e
}

func deleteBatchMetadata(ctx context.Context, tx pgx.Tx, batches []string) error {
	for _, query := range []string{
		"DELETE FROM batch_payloads WHERE batch_id=ANY($1::uuid[])",
		"DELETE FROM webhook_jobs WHERE event_id IN(SELECT e.id FROM events e JOIN messages m ON m.id=e.message_id WHERE m.batch_id=ANY($1::uuid[]))",
		"DELETE FROM events WHERE message_id IN(SELECT id FROM messages WHERE batch_id=ANY($1::uuid[]))",
		"DELETE FROM outbox WHERE message_id IN(SELECT id FROM messages WHERE batch_id=ANY($1::uuid[]))",
		"DELETE FROM mta_receipts WHERE message_id IN(SELECT id FROM messages WHERE batch_id=ANY($1::uuid[]))",
		"DELETE FROM attempts WHERE message_id IN(SELECT id FROM messages WHERE batch_id=ANY($1::uuid[]))",
		"DELETE FROM messages WHERE batch_id=ANY($1::uuid[])",
		"UPDATE batches SET payload_expired_at=coalesce(payload_expired_at,clock_timestamp()) WHERE id=ANY($1::uuid[])",
	} {
		if _, e := tx.Exec(ctx, query, batches); e != nil {
			return e
		}
	}
	return nil
}

// Lock children before certifying retention. DSNs take message locks and
// webhook replay takes job locks; the initial candidate query alone cannot
// prevent their eligibility changes while cleanup waits.
func retentionEligible(ctx context.Context, tx pgx.Tx, batches []string, horizon string, callbacks bool) ([]string, error) {
	if len(batches) == 0 {
		return nil, nil
	}
	queries := []string{"SELECT id FROM messages WHERE batch_id=ANY($1::uuid[]) ORDER BY id FOR UPDATE"}
	if callbacks {
		queries = append(queries, "SELECT j.id FROM webhook_jobs j JOIN events e ON e.id=j.event_id JOIN messages m ON m.id=e.message_id WHERE m.batch_id=ANY($1::uuid[]) ORDER BY j.id FOR UPDATE OF j")
	}
	for _, query := range queries {
		rows, e := tx.Query(ctx, query, batches)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	query := `SELECT b.id::text FROM batches b WHERE b.id=ANY($1::uuid[]) AND EXISTS(SELECT 1 FROM messages m WHERE m.batch_id=b.id) AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.batch_id=b.id AND (m.status NOT IN ('delivered','bounced','failed','canceled','suppressed') OR m.updated_at>clock_timestamp()-$2::interval))`
	if callbacks {
		query += ` AND NOT EXISTS(SELECT 1 FROM webhook_jobs j JOIN events e ON e.id=j.event_id JOIN messages m ON m.id=e.message_id WHERE m.batch_id=b.id AND (j.status='pending' OR (j.status='dead' AND j.resolved_at IS NULL)))`
	}
	rows, e := tx.Query(ctx, query, batches, horizon)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var eligible []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		eligible = append(eligible, id)
	}
	return eligible, rows.Err()
}

func (s *Store) applyRetirement(ctx context.Context, r journal.Record) error {
	if !domain.ValidID(r.Batch) || !domain.ValidID(r.Tenant) {
		return fmt.Errorf("invalid retirement journal")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = deleteBatchMetadata(ctx, tx, []string{r.Batch}); e != nil {
		return e
	}
	if r.Kind == "retention-idempotency" {
		if _, e = tx.Exec(ctx, "DELETE FROM batches WHERE id=$1 AND tenant_id=$2", r.Batch, r.Tenant); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
