package store

import (
	"context"
	"errors"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Store) AddDomain(ctx context.Context, tenant string, d domain.Domain) error {
	return s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, "INSERT INTO sender_domains(id,tenant_id,name,selector,verification_token,public_key,encrypted_key) VALUES($1,$2,$3,$4,$5,$6,$7)", d.ID, tenant, d.Name, d.Selector, d.Token, d.PublicKey, d.EncryptedKey)
		var pgErr *pgconn.PgError
		if errors.As(e, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrConflict
		}
		return e
	})
}
func (s *Store) GetDomain(ctx context.Context, tenant, name string) (d domain.Domain, e error) {
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT id::text,name,selector,verification_token,public_key,verified_at IS NOT NULL,encrypted_key FROM sender_domains WHERE tenant_id=$1 AND name=$2", tenant, name).Scan(&d.ID, &d.Name, &d.Selector, &d.Token, &d.PublicKey, &d.Verified, &d.EncryptedKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return err
	})
	return
}
func (s *Store) VerifyDomain(ctx context.Context, tenant, name string) error {
	return s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		t, e := tx.Exec(ctx, "UPDATE sender_domains SET verified_at=now() WHERE tenant_id=$1 AND name=$2", tenant, name)
		if e == nil && t.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return e
	})
}
func (s *Store) PutTemplate(ctx context.Context, tenant string, t domain.Template) (version int, e error) {
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		// A tenant row lock serializes version allocation without mutable template versions.
		if _, e := tx.Exec(ctx, "SELECT id FROM tenants WHERE id=$1 FOR UPDATE", tenant); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, "SELECT coalesce(max(version),0)+1 FROM templates WHERE tenant_id=$1 AND name=$2", tenant, t.Name).Scan(&version); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "INSERT INTO templates(tenant_id,name,version,subject,body_text,body_html) VALUES($1,$2,$3,$4,$5,$6)", tenant, t.Name, version, t.Subject, t.Text, t.HTML)
		return e
	})
	return
}
func (s *Store) GetTemplate(ctx context.Context, tenant, name string) (t domain.Template, e error) {
	e = s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, "SELECT name,version,subject,body_text,body_html FROM templates WHERE tenant_id=$1 AND name=$2 ORDER BY version DESC LIMIT 1", tenant, name).Scan(&t.Name, &t.Version, &t.Subject, &t.Text, &t.HTML)
		if errors.Is(e, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return e
	})
	return
}
func (s *Store) AddWebhook(ctx context.Context, tenant string, w domain.Webhook, encrypted []byte) error {
	return s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, "INSERT INTO webhook_endpoints(id,tenant_id,url,encrypted_secret) VALUES($1,$2,$3,$4)", w.ID, tenant, w.URL, encrypted)
		return e
	})
}
func (s *Store) DeleteWebhook(ctx context.Context, tenant, id string) error {
	return s.TenantTx(ctx, tenant, func(tx pgx.Tx) error {
		t, e := tx.Exec(ctx, "UPDATE webhook_endpoints SET enabled=false WHERE tenant_id=$1 AND id=$2", tenant, id)
		if e == nil && t.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return e
	})
}
