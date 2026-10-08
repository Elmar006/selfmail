package store

import (
	"context"
	"encoding/json"
	"fmt"

	"errors"
	"os"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
)

type RecoveryReport struct {
	Imported       int    `json:"imported"`
	Unknown        int    `json:"unknown"`
	MissingPayload int    `json:"missing_payload"`
	Digest         string `json:"digest"`
	Generation     uint64 `json:"generation"`
}

func (s *Store) ReconcileJournal(ctx context.Context) (report RecoveryReport, err error) {
	if s.Control == nil || s.Journal == nil {
		return report, fmt.Errorf("recovery storage required")
	}
	state, e := s.Control.Status(ctx)
	if e != nil {
		return report, e
	}
	if !state.Held {
		return report, fmt.Errorf("recovery hold required")
	}
	report.Generation = state.Generation
	report.Digest, e = s.Journal.Digest(ctx)
	if e != nil {
		return report, e
	}
	// Retirement must precede acceptance replay, regardless of record order.
	// Otherwise an expired key in an older snapshot can collide with a newer
	// accepted operation using that key after the retention horizon.
	e = s.Journal.Visit(ctx, func(r journal.Record) error {
		if r.Kind == "retention-metadata" || r.Kind == "retention-idempotency" {
			return s.applyRetirement(ctx, r)
		}
		return nil
	})
	if e != nil {
		return report, e
	}
	e = s.Journal.Visit(ctx, func(r journal.Record) error {
		if r.Kind != "acceptance" {
			return nil
		}
		if _, e := s.Journal.Get(journal.ID("acceptance-committed", r.Batch)); errors.Is(e, os.ErrNotExist) {
			return nil
		} else if e != nil {
			return e
		}
		if !domain.ValidID(r.Batch) || !domain.ValidID(r.Tenant) || len(r.MessageIDs) != len(r.Recipients) {
			return fmt.Errorf("invalid acceptance journal")
		}
		metadataExpired, keyExpired, e := s.retirement(r.Batch)
		if e != nil || keyExpired {
			return e
		}
		tx, e := s.Pool.Begin(ctx)
		if e != nil {
			return e
		}
		result := domain.SendResult{BatchID: r.Batch, MessageIDs: r.MessageIDs}
		response, _ := json.Marshal(result)
		_, e = tx.Exec(ctx, "INSERT INTO batches(id,tenant_id,idempotency_key,idempotency_digest,fingerprint,response,created_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING", r.Batch, r.Tenant, "recovered:"+r.KeyDigest, r.KeyDigest, r.Fingerprint, response, r.Created)
		if e == nil && metadataExpired {
			_, e = tx.Exec(ctx, "UPDATE batches SET payload_expired_at=coalesce(payload_expired_at,clock_timestamp()) WHERE id=$1", r.Batch)
		}
		if e == nil && !metadataExpired {
			for i, id := range r.MessageIDs {
				tag, writeErr := tx.Exec(ctx, "INSERT INTO messages(id,tenant_id,batch_id,sender,recipient,priority,status,next_attempt_at,expires_at,last_error,created_at) VALUES($1,$2,$3,$4,$5,$6,'submission_unknown',clock_timestamp(),clock_timestamp(),'recovery: payload/state not present in SQL snapshot',$7) ON CONFLICT(id) DO NOTHING", id, r.Tenant, r.Batch, r.From, r.Recipients[i], r.Priority, r.Created)
				if writeErr != nil {
					e = writeErr
					break
				}
				report.Imported += int(tag.RowsAffected())
			}
		}
		if e != nil {
			tx.Rollback(ctx)
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		return nil
	})
	if e != nil {
		return report, e
	}
	// Restore attempt correlation before replaying MTA evidence. Every intent
	// initially blocks automatic resend; only stronger evidence resolves it.
	e = s.Journal.Visit(ctx, func(r journal.Record) error {
		if r.Kind != "intent" {
			return nil
		}
		return s.importIntent(ctx, r)
	})
	if e != nil {
		return report, e
	}
	e = s.Journal.Visit(ctx, func(r journal.Record) error {
		switch r.Kind {
		case "outcome":
			if r.Status == "submitted" {
				retired, err := s.retiredAttempt(r.Attempt)
				if err != nil || retired {
					return err
				}
				m := domain.Message{ID: r.Message, TenantID: r.Tenant, AttemptID: r.Attempt, NodeID: r.Node}
				if e = s.FinishSubmission(ctx, m, "submitted", r.Queue, r.Diagnostic); e != nil {
					return e
				}
			}
		case "log":
			if e = s.ApplyLogLine(ctx, r.Node, r.File, r.Offset, r.Next, r.Queue, r.Message, r.Attempt, r.Status, r.Recipient, r.DSN, r.Diagnostic, true); e != nil {
				return e
			}
		case "dsn":
			if e = s.ApplyDSN(ctx, r.Attempt, r.Recipient, r.Action, r.DSN); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		return report, e
	}
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE status='submission_unknown'").Scan(&report.Unknown); e != nil {
		return report, e
	}
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM messages m WHERE status IN ('queued','dispatching','submission_unknown') AND NOT EXISTS(SELECT 1 FROM batch_payloads p WHERE p.batch_id=m.batch_id)").Scan(&report.MissingPayload); e != nil {
		return report, e
	}
	finalDigest, e := s.Journal.Digest(ctx)
	if e != nil {
		return report, e
	}
	if finalDigest != report.Digest {
		return report, fmt.Errorf("journal changed during reconciliation; repeat before release")
	}
	if e = s.Control.Reconciled(ctx, state.Generation, report.Digest); e != nil {
		return report, e
	}
	return report, nil
}

func (s *Store) importIntent(ctx context.Context, r journal.Record) error {
	if !domain.ValidID(r.Message) || !domain.ValidID(r.Attempt) || r.Node == "" {
		return fmt.Errorf("invalid handoff journal")
	}
	retired, _, e := s.retirement(r.Batch)
	if e != nil || retired {
		return e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var status string
	if e = tx.QueryRow(ctx, "SELECT status FROM messages WHERE id=$1 AND tenant_id=$2 FOR UPDATE", r.Message, r.Tenant).Scan(&status); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO attempts(id,tenant_id,message_id,phase,node_id,created_at) VALUES($1,$2,$3,'unknown',$4,$5) ON CONFLICT(id) DO NOTHING", r.Attempt, r.Tenant, r.Message, r.Node, r.At)
	if e != nil {
		return e
	}
	if status == "queued" || status == "dispatching" || status == "submission_unknown" {
		if _, e = tx.Exec(ctx, "UPDATE messages SET status='submission_unknown',attempt_id=$2,lease_until=NULL,last_error='recovery: independent handoff intent' WHERE id=$1", r.Message, r.Attempt); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// SeedLegacyAcceptance is restricted to a held, reviewed upgrade. It certifies
// visible SQL batches; it cannot reconstruct delivery history already lost.
func (s *Store) SeedLegacyAcceptance(ctx context.Context) error {
	state, e := s.Control.Status(ctx)
	if e != nil || !state.Held {
		return fmt.Errorf("held upgrade required")
	}
	rows, e := s.Pool.Query(ctx, "SELECT id::text,tenant_id::text,idempotency_digest,fingerprint,created_at FROM batches ORDER BY created_at,id")
	if e != nil {
		return e
	}
	var batches []journal.Record
	for rows.Next() {
		var r journal.Record
		r.Kind = "acceptance"
		if e = rows.Scan(&r.Batch, &r.Tenant, &r.KeyDigest, &r.Fingerprint, &r.Created); e != nil {
			rows.Close()
			return e
		}
		r.ID = journal.ID("acceptance", r.Batch)
		batches = append(batches, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, r := range batches {
		existing, e := s.Journal.Get(r.ID)
		if e == nil {
			r = existing
		} else {
			if !errors.Is(e, os.ErrNotExist) {
				return e
			}
			rows, e := s.Pool.Query(ctx, "SELECT id::text,sender,recipient,priority FROM messages WHERE batch_id=$1 ORDER BY recipient", r.Batch)
			if e != nil {
				return e
			}
			for rows.Next() {
				var id, recipient string
				if e = rows.Scan(&id, &r.From, &recipient, &r.Priority); e != nil {
					rows.Close()
					return e
				}
				r.MessageIDs = append(r.MessageIDs, id)
				r.Recipients = append(r.Recipients, recipient)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if len(r.MessageIDs) == 0 {
				continue
			}
			if e = s.Journal.Put(r); e != nil {
				return e
			}
		}
		if e = s.commitAcceptance(r.Batch, r.Tenant); e != nil {
			return e
		}
	}
	return nil
}
