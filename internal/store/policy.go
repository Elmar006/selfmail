package store

import (
	"context"
)

func (s *Store) DeliveryPolicy(ctx context.Context, tenant string) (rate int, paused bool, e error) {
	e = s.Pool.QueryRow(ctx, "SELECT rate,paused FROM tenants WHERE id=$1", tenant).Scan(&rate, &paused)
	return
}
