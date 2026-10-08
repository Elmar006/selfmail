package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/jackc/pgx/v5"
)

func (s *Store) Replay(ctx context.Context, tenant, key, hash string) (out domain.SendResult, exists bool, e error) {
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		var fingerprint string
		var response []byte
		e := tx.QueryRow(ctx, "SELECT id::text,fingerprint,response FROM batches WHERE tenant_id=$1 AND idempotency_digest=$2", tenant, security.Digest(key)).Scan(&out.BatchID, &fingerprint, &response)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		exists = true
		if fingerprint != hash {
			return domain.ErrConflict
		}
		out.Replayed = true
		if len(response) > 0 {
			if e = json.Unmarshal(response, &out); e != nil {
				return e
			}
			out.Replayed = true
			return nil
		}
		rows, e := tx.Query(ctx, "SELECT id::text FROM messages WHERE tenant_id=$1 AND batch_id=$2 ORDER BY recipient", tenant, out.BatchID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			out.MessageIDs = append(out.MessageIDs, id)
		}
		return rows.Err()
	})
	if e == nil && exists {
		e = s.EnsureAcceptance(ctx, out.BatchID, tenant)
	}
	return
}

func (s *Store) Enqueue(ctx context.Context, p domain.Principal, key, fingerprint string, r domain.SendRequest, allowUnverified bool) (result domain.SendResult, err error) {
	release, e := s.DeliveryPermit(ctx)
	if e != nil {
		return result, e
	}
	defer release()
	body, e := json.Marshal(r)
	if e != nil {
		return result, e
	}
	err = s.TenantTx(ctx, p.TenantID, func(tx pgx.Tx) error {
		// Tenant lock makes quotas, pause and per-project ingress order durable and serializable.
		var paused bool
		var daily int
		var stored int64
		if e := tx.QueryRow(ctx, "SELECT paused,daily_limit,stored_bytes FROM tenants WHERE id=$1 FOR NO KEY UPDATE", p.TenantID).Scan(&paused, &daily, &stored); e != nil {
			return e
		}
		if paused {
			return domain.ErrForbidden
		}
		if !domain.ValidID(p.KeyID) {
			return domain.ErrForbidden
		}
		var keyTenant string
		var scopes []string
		e := tx.QueryRow(ctx, "SELECT tenant_id::text,scopes FROM authorize_key($1)", p.KeyID).Scan(&keyTenant, &scopes)
		if errors.Is(e, pgx.ErrNoRows) {
			return domain.ErrForbidden
		}
		if e != nil {
			return e
		}
		if keyTenant != p.TenantID || !(domain.Principal{Scopes: scopes}).Can("messages:write") {
			return domain.ErrForbidden
		}
		var oldID, oldHash string
		var cached []byte
		e = tx.QueryRow(ctx, "SELECT id::text,fingerprint,response FROM batches WHERE tenant_id=$1 AND idempotency_digest=$2", p.TenantID, security.Digest(key)).Scan(&oldID, &oldHash, &cached)
		if e == nil {
			if oldHash != fingerprint {
				return domain.ErrConflict
			}
			result.BatchID = oldID
			result.Replayed = true
			if len(cached) > 0 {
				if e = json.Unmarshal(cached, &result); e != nil {
					return e
				}
				result.Replayed = true
				return nil
			}
			rows, e := tx.Query(ctx, "SELECT id::text FROM messages WHERE tenant_id=$1 AND batch_id=$2 ORDER BY recipient", p.TenantID, oldID)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					return e
				}
				result.MessageIDs = append(result.MessageIDs, id)
			}
			return rows.Err()
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		limit := s.MaxTenantBytes
		if limit == 0 {
			limit = 512 * 1024 * 1024
		}
		if stored+int64(len(body)+len(r.Raw)+4096) > limit {
			return domain.ErrUnavailable
		}
		var active int
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM messages WHERE tenant_id=$1 AND status IN ('queued','dispatching','submission_unknown','submitted','deferred')", p.TenantID).Scan(&active); e != nil {
			return e
		}
		if active+len(r.To) > 100000 {
			return domain.ErrUnavailable
		}
		var verified bool
		if e = tx.QueryRow(ctx, "SELECT verified_at IS NOT NULL FROM sender_domains WHERE tenant_id=$1 AND name=$2", p.TenantID, domain.SenderDomain(r.From)).Scan(&verified); errors.Is(e, pgx.ErrNoRows) {
			return domain.ErrForbidden
		} else if e != nil {
			return e
		}
		if !verified && !allowUnverified {
			return domain.ErrForbidden
		}
		for _, rcpt := range r.To {
			var banned bool
			if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM suppressions WHERE tenant_id=$1 AND recipient=$2)", p.TenantID, rcpt).Scan(&banned); e != nil {
				return e
			}
			if banned {
				return domain.ErrSuppressed
			}
		}
		var count int
		e = tx.QueryRow(ctx, "INSERT INTO daily_usage(tenant_id,day,count) VALUES($1,(now() AT TIME ZONE 'UTC')::date,$2) ON CONFLICT(tenant_id,day) DO UPDATE SET count=daily_usage.count+EXCLUDED.count WHERE daily_usage.count+EXCLUDED.count<=$3 RETURNING count", p.TenantID, len(r.To), daily).Scan(&count)
		if errors.Is(e, pgx.ErrNoRows) {
			return domain.Invalid("quota", "daily message limit exceeded")
		}
		if e != nil {
			return e
		}
		if count > daily {
			return domain.Invalid("quota", "daily message limit exceeded")
		}
		result.BatchID = domain.ID()
		if _, e = tx.Exec(ctx, "INSERT INTO batches(id,tenant_id,idempotency_key,idempotency_digest,fingerprint) VALUES($1,$2,$3,$4,$5)", result.BatchID, p.TenantID, key, security.Digest(key), fingerprint); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO batch_payloads(batch_id,tenant_id,payload,raw_mime) VALUES($1,$2,$3,$4)", result.BatchID, p.TenantID, body, r.Raw); e != nil {
			return e
		}
		when := r.SendAt
		if when.IsZero() || when.Before(time.Now()) {
			when = time.Now().UTC()
		}
		ttl := r.TTLSeconds
		if ttl == 0 {
			ttl = 86400
		}
		expires := when.Add(time.Duration(ttl) * time.Second)
		for _, rcpt := range r.To {
			id := domain.ID()
			result.MessageIDs = append(result.MessageIDs, id)
			if _, e = tx.Exec(ctx, "INSERT INTO messages(id,tenant_id,batch_id,sender,recipient,priority,status,next_attempt_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,'queued',$7,$8)", id, p.TenantID, result.BatchID, r.From, rcpt, r.Priority, when, expires); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, "INSERT INTO outbox(tenant_id,message_id,priority,available_at) VALUES($1,$2,$3,$4)", p.TenantID, id, r.Priority, when); e != nil {
				return e
			}
			if e = EventTx(ctx, tx, p.TenantID, id, "queued", nil); e != nil {
				return e
			}
		}
		cached, e = json.Marshal(result)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "UPDATE batches SET response=$2 WHERE id=$1", result.BatchID, cached)
		if e != nil {
			return e
		}
		return s.acceptanceIntent(ctx, tx, p, security.Digest(key), fingerprint, r, result)
	})
	if err == nil {
		err = s.EnsureAcceptance(ctx, result.BatchID, p.TenantID)
	}
	return
}

const messageCols = "id::text,tenant_id::text,batch_id::text,sender,recipient,priority,status,attempt_count,last_error,queue_id,created_at,updated_at"

func scanMessage(row pgx.Row) (m domain.Message, e error) {
	e = row.Scan(&m.ID, &m.TenantID, &m.BatchID, &m.From, &m.Recipient, &m.Priority, &m.Status, &m.AttemptCount, &m.LastError, &m.QueueID, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = domain.ErrNotFound
	}
	return
}
func (s *Store) GetMessage(ctx context.Context, tenant, id string) (m domain.Message, e error) {
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		var err error
		m, err = scanMessage(tx.QueryRow(ctx, "SELECT "+messageCols+" FROM messages WHERE tenant_id=$1 AND id=$2", tenant, id))
		if errors.Is(err, domain.ErrNotFound) {
			return messageAbsent(ctx, tx, tenant, id)
		}
		return err
	})
	return
}
func (s *Store) ListMessages(ctx context.Context, tenant, before string, limit int) (out []domain.Message, e error) {
	out = []domain.Message{}
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+messageCols+" FROM messages WHERE tenant_id=$1 AND ($2='' OR (created_at,id) < (SELECT created_at,id FROM messages WHERE tenant_id=$1 AND id=nullif($2,'')::uuid)) ORDER BY created_at DESC,id DESC LIMIT $3", tenant, before, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			m, e := scanMessage(rows)
			if e != nil {
				return e
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return
}
func (s *Store) Events(ctx context.Context, tenant, id string) (out []domain.Event, e error) {
	out = []domain.Event{}
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE tenant_id=$1 AND id=$2)", tenant, id).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return messageAbsent(ctx, tx, tenant, id)
		}
		rows, e := tx.Query(ctx, "SELECT id::text,message_id::text,type,details,created_at,sequence FROM events WHERE tenant_id=$1 AND message_id=$2 ORDER BY sequence LIMIT 1000", tenant, id)
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
	return
}
func (s *Store) Cancel(ctx context.Context, tenant, id string) error {
	return s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		var status string
		e := tx.QueryRow(ctx, "SELECT status FROM messages WHERE tenant_id=$1 AND id=$2 FOR UPDATE", tenant, id).Scan(&status)
		if errors.Is(e, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if e != nil {
			return e
		}
		if status == "canceled" {
			return nil
		}
		if status != "queued" {
			return domain.ErrConflict
		}
		if _, e = tx.Exec(ctx, "UPDATE messages SET status='canceled',updated_at=now() WHERE id=$1", id); e != nil {
			return e
		}
		return EventTx(ctx, tx, tenant, id, "canceled", nil)
	})
}
func (s *Store) Suppress(ctx context.Context, tenant, recipient, reason string, remove bool) error {
	return s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE", tenant); e != nil {
			return e
		}
		var e error
		if remove {
			_, e = tx.Exec(ctx, "DELETE FROM suppressions WHERE tenant_id=$1 AND recipient=$2", tenant, recipient)
		} else {
			_, e = tx.Exec(ctx, "INSERT INTO suppressions(tenant_id,recipient,reason) VALUES($1,$2,$3) ON CONFLICT(tenant_id,recipient) DO UPDATE SET reason=EXCLUDED.reason", tenant, recipient, reason)
		}
		if e != nil {
			return e
		}
		b, _ := json.Marshal(map[string]any{"recipient": recipient, "removed": remove})
		_, e = tx.Exec(ctx, "INSERT INTO audit_log(tenant_id,action,details) VALUES($1,'suppression.changed',$2)", tenant, b)
		return e
	})
}
