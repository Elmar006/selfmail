package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type RetentionPolicy struct{ Body, Metadata, Idempotency time.Duration }

func DefaultRetention() RetentionPolicy {
	return RetentionPolicy{7 * 24 * time.Hour, 30 * 24 * time.Hour, 90 * 24 * time.Hour}
}

type CleanupResult struct {
	Payloads, Messages, Batches int64
	More                        bool
}

func (s *Store) Cleanup(ctx context.Context, p RetentionPolicy, dry bool) (out CleanupResult, e error) {
	if p.Body < 24*time.Hour || p.Metadata < p.Body || p.Idempotency < p.Metadata {
		return out, fmt.Errorf("invalid retention horizons")
	}
	release, e := s.DeliveryPermit(ctx)
	if e != nil {
		return out, e
	}
	defer release()
	rows, e := s.Pool.Query(ctx, "SELECT id::text FROM tenants ORDER BY id")
	if e != nil {
		return out, e
	}
	var tenants []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		tenants = append(tenants, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, tenant := range tenants {
		e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, "SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE", tenant); e != nil {
				return e
			}
			candidates, e := tx.Query(ctx, `SELECT b.id::text FROM batches b JOIN batch_payloads p ON p.batch_id=b.id WHERE b.tenant_id=$1 AND EXISTS(SELECT 1 FROM messages m WHERE m.batch_id=b.id) AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.batch_id=b.id AND (m.status NOT IN ('delivered','bounced','failed','canceled','suppressed') OR m.updated_at>clock_timestamp()-$2::interval)) ORDER BY b.created_at,b.id FOR UPDATE OF b SKIP LOCKED LIMIT 100`, tenant, Interval(p.Body))
			if e != nil {
				return e
			}
			var bodies []string
			for candidates.Next() {
				var id string
				if e = candidates.Scan(&id); e != nil {
					candidates.Close()
					return e
				}
				bodies = append(bodies, id)
			}
			e = candidates.Err()
			candidates.Close()
			if e != nil {
				return e
			}
			bodies, e = retentionEligible(ctx, tx, bodies, Interval(p.Body), false)
			if e != nil {
				return e
			}
			out.Payloads += int64(len(bodies))
			if !dry && len(bodies) > 0 {
				if _, e = tx.Exec(ctx, "DELETE FROM batch_payloads WHERE batch_id=ANY($1::uuid[])", bodies); e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, "UPDATE batches SET payload_expired_at=clock_timestamp() WHERE id=ANY($1::uuid[])", bodies); e != nil {
					return e
				}
			}
			candidates, e = tx.Query(ctx, `SELECT b.id::text FROM batches b WHERE b.tenant_id=$1 AND b.payload_expired_at IS NOT NULL AND EXISTS(SELECT 1 FROM messages m WHERE m.batch_id=b.id) AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.batch_id=b.id AND (m.status NOT IN ('delivered','bounced','failed','canceled','suppressed') OR m.updated_at>clock_timestamp()-$2::interval)) AND NOT EXISTS(SELECT 1 FROM webhook_jobs j JOIN events e ON e.id=j.event_id JOIN messages m ON m.id=e.message_id WHERE m.batch_id=b.id AND (j.status='pending' OR (j.status='dead' AND j.resolved_at IS NULL))) ORDER BY b.created_at,b.id FOR UPDATE OF b SKIP LOCKED LIMIT 100`, tenant, Interval(p.Metadata))
			if e != nil {
				return e
			}
			var metadata []string
			for candidates.Next() {
				var id string
				if e = candidates.Scan(&id); e != nil {
					candidates.Close()
					return e
				}
				metadata = append(metadata, id)
			}
			e = candidates.Err()
			candidates.Close()
			if e != nil {
				return e
			}
			metadata, e = retentionEligible(ctx, tx, metadata, Interval(p.Metadata), true)
			if e != nil {
				return e
			}
			if len(metadata) > 0 {
				var count int64
				if e = tx.QueryRow(ctx, "SELECT count(*) FROM messages WHERE batch_id=ANY($1::uuid[])", metadata).Scan(&count); e != nil {
					return e
				}
				out.Messages += count
			}
			if !dry && len(metadata) > 0 {
				for _, batch := range metadata {
					if e = s.recordRetirement(batch, tenant, "retention-metadata"); e != nil {
						return e
					}
				}
				if e = deleteBatchMetadata(ctx, tx, metadata); e != nil {
					return e
				}
			}
			var expired []string
			tombstones, e := tx.Query(ctx, "SELECT b.id::text FROM batches b WHERE tenant_id=$1 AND created_at<clock_timestamp()-$2::interval AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.batch_id=b.id) AND NOT EXISTS(SELECT 1 FROM batch_payloads p WHERE p.batch_id=b.id) ORDER BY b.created_at,b.id FOR UPDATE OF b SKIP LOCKED LIMIT 100", tenant, Interval(p.Idempotency))
			if e != nil {
				return e
			}
			for tombstones.Next() {
				var id string
				if e = tombstones.Scan(&id); e != nil {
					tombstones.Close()
					return e
				}
				expired = append(expired, id)
			}
			e = tombstones.Err()
			tombstones.Close()
			if e != nil {
				return e
			}
			out.Batches += int64(len(expired))
			if !dry {
				for _, batch := range expired {
					if e = s.recordRetirement(batch, tenant, "retention-idempotency"); e != nil {
						return e
					}
				}
				for _, query := range []string{
					`DELETE FROM outbox WHERE id IN(SELECT o.id FROM outbox o JOIN messages m ON m.id=o.message_id WHERE o.tenant_id=$1 AND o.published_at<clock_timestamp()-interval '7 days' AND m.status IN ('delivered','bounced','failed','canceled','suppressed') ORDER BY o.id LIMIT 1000)`,
					`DELETE FROM webhook_jobs WHERE id IN(SELECT j.id FROM webhook_jobs j WHERE j.tenant_id=$1 AND ((j.status='delivered' AND j.available_at<clock_timestamp()-interval '30 days') OR (j.status='dead' AND j.resolved_at<clock_timestamp()-interval '30 days')) ORDER BY j.available_at,j.id LIMIT 1000)`,
					`DELETE FROM audit_log WHERE id IN(SELECT id FROM audit_log WHERE tenant_id=$1 AND created_at<clock_timestamp()-interval '365 days' ORDER BY created_at,id LIMIT 1000)`,
				} {
					tag, err := tx.Exec(ctx, query, tenant)
					if err != nil {
						return err
					}
					out.More = out.More || tag.RowsAffected() > 0
				}
				_, e := tx.Exec(ctx, "DELETE FROM batches WHERE id=ANY($1::uuid[])", expired)
				if e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, "DELETE FROM daily_usage WHERE tenant_id=$1 AND day<(clock_timestamp()-$2::interval)::date", tenant, Interval(p.Idempotency)); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return out, e
		}
	}
	if !dry {
		// File archives expire after 90 days only with an ingestion watermark.
		// Cursor/line dedupe gets two extra days and remains independent of bodies.
		tag, err := s.Pool.Exec(ctx, `WITH expired AS(SELECT node_id,file_id FROM mta_log_cursors WHERE completed_at<clock_timestamp()-interval '92 days' ORDER BY completed_at LIMIT 100), deleted AS(DELETE FROM mta_log_lines l USING expired e WHERE l.node_id=e.node_id AND l.file_id=e.file_id) DELETE FROM mta_log_cursors c USING expired e WHERE c.node_id=e.node_id AND c.file_id=e.file_id`)
		e = err
		if e != nil {
			return out, e
		}
		out.More = out.More || tag.RowsAffected() > 0
		tag, e = s.Pool.Exec(ctx, `DELETE FROM mta_receipts WHERE (node_id,queue_id) IN(SELECT r.node_id,r.queue_id FROM mta_receipts r WHERE r.updated_at<clock_timestamp()-interval '90 days' AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.id=r.message_id AND m.status IN ('queued','dispatching','submission_unknown','submitted','deferred')) ORDER BY r.updated_at LIMIT 1000)`)
		if e != nil {
			return out, e
		}
		out.More = out.More || tag.RowsAffected() > 0
	}
	out.More = out.More || out.Payloads > 0 || out.Messages > 0 || out.Batches > 0
	return out, nil
}
