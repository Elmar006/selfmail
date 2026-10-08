package store

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
)

func TestRecoveryKeySurrogateCannotCollideWithUserKey(t *testing.T) {
	sys, app := setup(t)
	ctx := context.Background()
	p := tenant(t, sys, app, 100)
	vault, e := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	control, e := recovery.New(t.TempDir(), vault)
	if e != nil {
		t.Fatal(e)
	}
	if e = control.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	evidence, e := journal.New(t.TempDir(), vault)
	if e != nil {
		t.Fatal(e)
	}
	sys.Control, app.Control = control, control
	sys.Journal, app.Journal = evidence, evidence
	keys := []string{"original-key", "recovered:" + security.Digest("original-key")}
	first, e := app.Enqueue(ctx, p, keys[0], "first", request(p), true)
	if e != nil {
		t.Fatal(e)
	}
	second, e := app.Enqueue(ctx, p, keys[1], "second", request(p), true)
	if e != nil {
		t.Fatal(e)
	}
	if e = control.Hold(ctx, "simulate snapshot missing first accepted operation"); e != nil {
		t.Fatal(e)
	}
	tx, e := sys.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = deleteBatchMetadata(ctx, tx, []string{first.BatchID}); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, "DELETE FROM batches WHERE id=$1", first.BatchID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = sys.ReconcileJournal(ctx); e != nil {
		t.Fatal(e)
	}
	for i, want := range []string{first.BatchID, second.BatchID} {
		got, exists, e := app.Replay(ctx, p.TenantID, keys[i], []string{"first", "second"}[i])
		if e != nil || !exists || got.BatchID != want {
			t.Fatalf("operation %d: %+v %v", i, got, e)
		}
	}
}
