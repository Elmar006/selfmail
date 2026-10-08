package journal

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/Elmar006/selfmail/internal/security"
)

func TestSnapshotIsConsistentAndIndependent(t *testing.T) {
	v, _ := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	source, _ := New(t.TempDir(), v)
	r := Record{ID: ID("first"), Kind: "intent"}
	if e := source.Put(r); e != nil {
		t.Fatal(e)
	}
	dest := t.TempDir()
	root, anchor := filepath.Join(dest, "journal"), filepath.Join(dest, "anchor")
	if e := source.CopySnapshot(context.Background(), root, anchor); e != nil {
		t.Fatal(e)
	}
	if e := source.Put(Record{ID: ID("later"), Kind: "outcome"}); e != nil {
		t.Fatal(e)
	}
	snapshot, _ := New(root, v)
	if e := snapshot.Bind(context.Background(), anchor, "local", false); e != nil {
		t.Fatal(e)
	}
	n := 0
	if e := snapshot.Visit(context.Background(), func(Record) error { n++; return nil }); e != nil || n != 1 {
		t.Fatalf("snapshot records=%d: %v", n, e)
	}
}
