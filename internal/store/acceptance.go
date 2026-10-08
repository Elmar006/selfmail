package store

import (
	"context"
	"errors"
	"os"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/jackc/pgx/v5"
)

func (s *Store) acceptanceIntent(ctx context.Context, tx pgx.Tx, p domain.Principal, keyHash, fingerprint string, r domain.SendRequest, result domain.SendResult) error {
	if s.Journal == nil {
		return nil
	}
	rec := journal.Record{ID: journal.ID("acceptance", result.BatchID), Kind: "acceptance", Tenant: p.TenantID, Batch: result.BatchID, KeyDigest: keyHash, Fingerprint: fingerprint, From: r.From, Priority: r.Priority, MessageIDs: result.MessageIDs, Recipients: r.To}
	if e := tx.QueryRow(ctx, "SELECT created_at FROM batches WHERE id=$1", result.BatchID).Scan(&rec.Created); e != nil {
		return e
	}
	return s.Journal.Put(rec)
}
func (s *Store) commitAcceptance(batch, tenant string) error {
	if s.Journal == nil {
		return nil
	}
	return s.Journal.Put(journal.Record{ID: journal.ID("acceptance-committed", batch), Kind: "acceptance-committed", Tenant: tenant, Batch: batch})
}

// A visible SQL acceptance can repair an interrupted commit-marker write. This
// is done before replay returns success and before a worker can claim the batch.
func (s *Store) EnsureAcceptance(ctx context.Context, batch, tenant string) error {
	if s.Journal == nil {
		return nil
	}
	if _, e := s.Journal.Get(journal.ID("acceptance-committed", batch)); e == nil {
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	var exists bool
	if e := s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM batches WHERE id=$1 AND tenant_id=$2)", batch, tenant).Scan(&exists)
	}); e != nil {
		return e
	}
	if !exists {
		return domain.ErrNotFound
	}
	if _, e := s.Journal.Get(journal.ID("acceptance", batch)); e != nil {
		return e
	}
	return s.commitAcceptance(batch, tenant)
}
