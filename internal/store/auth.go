package store

import (
	"context"
	"errors"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) RefreshPrincipal(ctx context.Context, old domain.Principal) (p domain.Principal, e error) {
	if !domain.ValidID(old.KeyID) {
		return p, domain.ErrForbidden
	}
	e = s.Pool.QueryRow(ctx, "SELECT key_id::text,tenant_id::text,name,scopes,rate,daily_limit,paused FROM authenticate_session($1)", old.KeyID).Scan(&p.KeyID, &p.TenantID, &p.Name, &p.Scopes, &p.Rate, &p.DailyLimit, &p.Paused)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, domain.ErrForbidden
	}
	if e == nil && p.TenantID != old.TenantID {
		return p, domain.ErrForbidden
	}
	return
}
