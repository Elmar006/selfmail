package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	Pool           *pgxpool.Pool
	Control        *recovery.Controller
	Journal        *journal.Journal
	MaxTenantBytes int64
	Observe        func(string, time.Duration)
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	cfg.MaxConns = 16
	cfg.MinConns = 1
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "5000"
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	return &Store{Pool: p}, nil
}
func (s *Store) Close() { s.Pool.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	conn, e := s.Pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, "SET statement_timeout='5min'"); e != nil {
		return e
	}
	defer conn.Exec(context.Background(), "SET statement_timeout='15s'")
	if _, e = conn.Exec(ctx, "SELECT pg_advisory_lock(7200701)"); e != nil {
		return e
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(7200701)")
	if _, e = conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(name text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		return e
	}
	if _, e = conn.Exec(ctx, "ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS sha256 text"); e != nil {
		return e
	}
	files, e := migrations.ReadDir("migrations")
	if e != nil {
		return e
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, f := range files {
		b, e := migrations.ReadFile("migrations/" + f.Name())
		if e != nil {
			return e
		}
		digest := security.Digest(string(b))
		var done bool
		if e = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", f.Name()).Scan(&done); e != nil {
			return e
		}
		if done {
			var recorded *string
			if e = conn.QueryRow(ctx, "SELECT sha256 FROM schema_migrations WHERE name=$1", f.Name()).Scan(&recorded); e != nil {
				return e
			}
			if recorded != nil && *recorded != digest {
				return fmt.Errorf("migration checksum mismatch: %s", f.Name())
			}
			if recorded == nil {
				if _, e = conn.Exec(ctx, "UPDATE schema_migrations SET sha256=$2 WHERE name=$1", f.Name(), digest); e != nil {
					return e
				}
			}
			continue
		}
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, string(b)); e == nil {
			_, e = tx.Exec(ctx, "INSERT INTO schema_migrations(name,sha256) VALUES($1,$2)", f.Name(), digest)
		}
		if e != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", f.Name(), e)
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) TenantTx(ctx context.Context, tenant string, fn func(pgx.Tx) error) error {
	if !domain.ValidID(tenant) {
		return domain.ErrForbidden
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT set_config('selfmail.tenant',$1,true)", tenant); e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return transient(e)
	}
	return transient(tx.Commit(ctx))
}
func (s *Store) Authenticate(ctx context.Context, key string) (p domain.Principal, e error) {
	if len(key) < 32 || len(key) > 128 {
		return p, domain.ErrForbidden
	}
	e = s.Pool.QueryRow(ctx, "SELECT key_id::text,tenant_id::text,name,scopes,rate,daily_limit,paused FROM authenticate_key_v2($1)", security.Digest(key)).Scan(&p.KeyID, &p.TenantID, &p.Name, &p.Scopes, &p.Rate, &p.DailyLimit, &p.Paused)
	if errors.Is(e, pgx.ErrNoRows) {
		e = domain.ErrForbidden
	}
	return
}
func (s *Store) CreateTenant(ctx context.Context, name string, rate, daily int) (string, string, error) {
	if name == "" || len(name) > 128 || rate < 1 || rate > 10000 || daily < 1 {
		return "", "", domain.Invalid("tenant", "invalid tenant options")
	}
	id, key := domain.ID(), "mail_"+domain.Token()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return "", "", e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "INSERT INTO tenants(id,name,rate,daily_limit) VALUES($1,$2,$3,$4)", id, name, rate, daily); e != nil {
		return "", "", e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO api_keys(id,tenant_id,digest,scopes) VALUES($1,$2,$3,$4)", domain.ID(), id, security.Digest(key), []string{"messages:write", "messages:read", "domains:write", "templates:write", "webhooks:write", "suppressions:write"}); e != nil {
		return "", "", e
	}
	_, e = tx.Exec(ctx, "INSERT INTO audit_log(tenant_id,action) VALUES($1,'tenant.created')", id)
	if e != nil {
		return "", "", e
	}
	return id, key, tx.Commit(ctx)
}
func (s *Store) IssueKey(ctx context.Context, tenant string, scopes []string) (string, error) {
	key := "mail_" + domain.Token()
	_, e := s.Pool.Exec(ctx, "INSERT INTO api_keys(id,tenant_id,digest,scopes) VALUES($1,$2,$3,$4)", domain.ID(), tenant, security.Digest(key), scopes)
	return key, e
}
func (s *Store) RevokeKey(ctx context.Context, key string) error {
	_, e := s.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE digest=$1", security.Digest(key))
	return e
}
func (s *Store) PauseTenant(ctx context.Context, tenant string, paused bool) error {
	t, e := s.Pool.Exec(ctx, "UPDATE tenants SET paused=$2 WHERE id=$1", tenant, paused)
	if e == nil && t.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return e
}
func EventTx(ctx context.Context, tx pgx.Tx, tenant, message, kind string, details map[string]string) error {
	ev := domain.Event{ID: domain.ID(), MessageID: message, Type: kind, Details: details, CreatedAt: time.Now().UTC()}
	if e := tx.QueryRow(ctx, "UPDATE messages SET next_event_sequence=next_event_sequence+1 WHERE id=$1 AND tenant_id=$2 RETURNING next_event_sequence", message, tenant).Scan(&ev.Sequence); e != nil {
		return e
	}
	d, e := json.Marshal(details)
	if e != nil {
		return e
	}
	if details == nil {
		d = []byte("{}")
	}
	if _, e = tx.Exec(ctx, "INSERT INTO events(id,tenant_id,message_id,type,details,created_at,sequence) VALUES($1,$2,$3,$4,$5,$6,$7)", ev.ID, tenant, message, kind, d, ev.CreatedAt, ev.Sequence); e != nil {
		return e
	}
	body, e := json.Marshal(ev)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO webhook_jobs(id,tenant_id,endpoint_id,event_id,payload) SELECT gen_random_uuid(),tenant_id,id,$2,$3 FROM webhook_endpoints WHERE tenant_id=$1 AND enabled", tenant, ev.ID, body)
	return e
}
