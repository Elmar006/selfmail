package store

import (
	"context"
	"testing"
)

func TestRetentionDrainsMoreThanOneBoundedPass(t *testing.T) {
	sys, app := setup(t)
	ctx := context.Background()
	p := tenant(t, sys, app, 1000)
	for i := 0; i < 201; i++ {
		enqueue(t, app, p)
	}
	if _, e := sys.Pool.Exec(ctx, "UPDATE messages SET status='delivered',updated_at=clock_timestamp()-interval '40 days' WHERE tenant_id=$1", p.TenantID); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		result, e := sys.Cleanup(ctx, DefaultRetention(), false)
		if e != nil {
			t.Fatal(e)
		}
		if !result.More {
			t.Fatal("cleanup failed to request backlog continuation")
		}
	}
	var remaining int
	if e := sys.Pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE tenant_id=$1", p.TenantID).Scan(&remaining); e != nil || remaining != 0 {
		t.Fatalf("remaining=%d %v", remaining, e)
	}
}
