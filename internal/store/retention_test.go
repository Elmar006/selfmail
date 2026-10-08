package store

import (
	"context"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestRetentionPreservesActiveAndIdempotency(t *testing.T) {
	sys, app := setup(t)
	p := tenant(t, sys, app, 100)
	ctx := context.Background()
	req := request(p)
	final, e := app.Enqueue(ctx, p, "retained-key", "fingerprint", req, true)
	if e != nil {
		t.Fatal(e)
	}
	active := enqueue(t, app, p)
	if _, e = sys.Pool.Exec(ctx, "UPDATE messages SET status='delivered',updated_at=clock_timestamp()-interval '40 days' WHERE id=$1", final.MessageIDs[0]); e != nil {
		t.Fatal(e)
	}
	result, e := sys.Cleanup(ctx, DefaultRetention(), false)
	if e != nil {
		t.Fatal(e)
	}
	if result.Payloads < 1 || result.Messages < 1 {
		t.Fatalf("cleanup: %+v", result)
	}
	if _, e = app.GetMessage(ctx, p.TenantID, final.MessageIDs[0]); e != domain.ErrGone {
		t.Fatalf("expired metadata must return gone: %v", e)
	}
	replay, exists, e := app.Replay(ctx, p.TenantID, "retained-key", "fingerprint")
	if e != nil || !exists || replay.MessageIDs[0] != final.MessageIDs[0] {
		t.Fatalf("lost idempotency: %+v %v", replay, e)
	}
	got, e := app.GetMessage(ctx, p.TenantID, active)
	if e != nil || got.Status != "queued" {
		t.Fatalf("removed active: %+v %v", got, e)
	}
	var payload bool
	if e = sys.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM batch_payloads WHERE batch_id=$1)", got.BatchID).Scan(&payload); e != nil || !payload {
		t.Fatal("removed active payload", e)
	}
	if _, e = app.Enqueue(ctx, p, "retained-key", "different", req, true); e != domain.ErrConflict {
		t.Fatalf("replay conflict changed: %v", e)
	}
}
