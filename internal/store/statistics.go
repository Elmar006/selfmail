package store

import (
	"context"
)

// AggregateStatistics touches only deltas and counter rows, never message locks.
// This avoids synchronous global counter locks in delivery transactions.
func (s *Store) AggregateStatistics(ctx context.Context) (int, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	var owned bool
	if e = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(7200702)").Scan(&owned); e != nil {
		return 0, e
	}
	if !owned {
		return 0, nil
	}
	var ids []int64
	rows, e := tx.Query(ctx, "SELECT id FROM statistics_deltas ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1000")
	if e != nil {
		return 0, e
	}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	if len(ids) > 0 {
		_, e = tx.Exec(ctx, "INSERT INTO statistics_stock(status,shard,count) SELECT DISTINCT status,shard,0 FROM statistics_deltas WHERE id=ANY($1) ON CONFLICT DO NOTHING", ids)
		if e != nil {
			return 0, e
		}
		_, e = tx.Exec(ctx, "UPDATE statistics_stock s SET count=s.count+d.n FROM (SELECT status,shard,sum(delta) n FROM statistics_deltas WHERE id=ANY($1) GROUP BY status,shard) d WHERE s.status=d.status AND s.shard=d.shard", ids)
		if e != nil {
			return 0, e
		}
		if _, e = tx.Exec(ctx, "DELETE FROM statistics_deltas WHERE id=ANY($1)", ids); e != nil {
			return 0, e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE statistics_checkpoint SET updated_at=clock_timestamp() WHERE id"); e != nil {
		return 0, e
	}
	return len(ids), tx.Commit(ctx)
}
