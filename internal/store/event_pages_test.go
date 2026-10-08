package store

import (
	"context"
	"strconv"
	"testing"
)

func TestEventPaginationBeyondThousand(t *testing.T) {
	sys, app := setup(t)
	p := tenant(t, sys, app, 100)
	ctx := context.Background()
	id := enqueue(t, app, p)
	tx, e := sys.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	for i := 0; i < 1100; i++ {
		if e = EventTx(ctx, tx, p.TenantID, id, "test", nil); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	cursor := ""
	total, last := 0, int64(0)
	for {
		items, next, e := app.EventsPage(ctx, p.TenantID, id, cursor, 100)
		if e != nil {
			t.Fatal(e)
		}
		for _, ev := range items {
			if ev.Sequence <= last {
				t.Fatal("non-monotonic sequence")
			}
			last = ev.Sequence
			total++
		}
		if next == "" {
			break
		}
		if _, e = strconv.ParseInt(next, 10, 64); e != nil {
			t.Fatal(e)
		}
		cursor = next
	}
	if total != 1101 {
		t.Fatalf("events=%d", total)
	}
}
