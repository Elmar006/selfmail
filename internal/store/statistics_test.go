package store

import (
	"context"
	"testing"
)

func TestAsynchronousStatisticsMatchCommittedState(t *testing.T) {
	sys, app := setup(t)
	p := tenant(t, sys, app, 100)
	ctx := context.Background()
	id := enqueue(t, app, p)
	if e := app.Cancel(ctx, p.TenantID, id); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		n, e := sys.AggregateStatistics(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if n < 1000 {
			break
		}
	}
	var mismatch int
	if e := sys.Pool.QueryRow(ctx, "SELECT count(*) FROM (SELECT status,count(*)::bigint n FROM messages GROUP BY status) a FULL JOIN (SELECT status,sum(count)::bigint n FROM statistics_stock GROUP BY status) b USING(status) WHERE coalesce(a.n,0)<>coalesce(b.n,0)").Scan(&mismatch); e != nil || mismatch != 0 {
		t.Fatalf("counter mismatch=%d %v", mismatch, e)
	}
}
