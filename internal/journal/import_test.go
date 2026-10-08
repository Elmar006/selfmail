package journal

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
)

func TestOffHostSnapshotRestoresIdentityHeld(t *testing.T) {
	ctx := context.Background()
	vault, e := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	controlRoot := t.TempDir()
	control, e := recovery.New(controlRoot, vault)
	if e != nil {
		t.Fatal(e)
	}
	if e = control.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	state, e := control.Status(ctx)
	if e != nil {
		t.Fatal(e)
	}
	live, e := New(t.TempDir(), vault)
	if e != nil {
		t.Fatal(e)
	}
	if e = live.Bind(ctx, controlRoot, state.Instance, true); e != nil {
		t.Fatal(e)
	}
	if e = live.Put(Record{ID: ID("offhost"), Kind: "test"}); e != nil {
		t.Fatal(e)
	}
	base := t.TempDir()
	root, anchor := filepath.Join(base, "journal"), filepath.Join(base, "control")
	if e = live.CopySnapshot(ctx, root, anchor); e != nil {
		t.Fatal(e)
	}
	restored, e := New(root, vault)
	if e != nil {
		t.Fatal(e)
	}
	if e = restored.Bind(ctx, anchor, state.Instance, false); e != nil {
		t.Fatal(e)
	}
	if _, e = restored.Digest(ctx); e != nil {
		t.Fatal(e)
	}
	fresh, e := recovery.New(anchor, vault)
	if e != nil {
		t.Fatal(e)
	}
	if e = fresh.ImportHeld(ctx, state, "restore original evidence on replacement host"); e != nil {
		t.Fatal(e)
	}
	got, e := fresh.Status(ctx)
	if e != nil || got.Instance != state.Instance || !got.Held || got.Generation <= state.Generation || got.Reconciled != 0 || got.Report != "" {
		t.Fatalf("import %+v %v", got, e)
	}
	if _, e = fresh.Acquire(ctx); e == nil {
		t.Fatal("import enabled delivery")
	}
	if e = fresh.Release(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); e == nil {
		t.Fatal("import reused reconciliation proof")
	}
}
