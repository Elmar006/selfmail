package recovery

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/security"
)

func TestHoldWaitsForInFlightPermitAndRequiresReconciliation(t *testing.T) {
	vault, _ := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	c, e := New(t.TempDir(), vault)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if _, e = c.Acquire(ctx); !errors.Is(e, ErrHeld) {
		t.Fatal("missing state allowed dispatch", e)
	}
	if e = c.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	p, e := c.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	waiting, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	if e = c.Hold(waiting, "restore in progress"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("hold ignored active permit", e)
	}
	p.Close()
	if e = c.Hold(ctx, "restore in progress"); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Acquire(ctx); !errors.Is(e, ErrHeld) {
		t.Fatal(e)
	}
	digest := security.Digest("reconciliation")
	if e = c.Release(ctx, digest); e == nil {
		t.Fatal("released without proof")
	}
	state, e := c.Status(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Reconciled(ctx, state.Generation, digest); e != nil {
		t.Fatal(e)
	}
	if e = c.Release(ctx, digest); e != nil {
		t.Fatal(e)
	}
	p, e = c.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p.Close()
}
