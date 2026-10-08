package journal

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Elmar006/selfmail/internal/security"
)

func TestManifestDetectsLostEvidence(t *testing.T) {
	for _, fault := range []string{"record", "tail", "head", "entire-journal"} {
		t.Run(fault, func(t *testing.T) {
			vault, _ := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
			root, anchor := t.TempDir(), t.TempDir()
			j, _ := New(root, vault)
			if e := j.Bind(context.Background(), anchor, "instance", true); e != nil {
				t.Fatal(e)
			}
			r := Record{ID: ID("test"), Kind: "intent"}
			if e := j.Put(r); e != nil {
				t.Fatal(e)
			}
			switch fault {
			case "record":
				p, _ := j.path(r.ID)
				os.Remove(p)
			case "tail":
				os.Truncate(j.manifestPath(), 0)
			case "head":
				os.Remove(j.headPath())
			case "entire-journal":
				os.RemoveAll(root)
				os.Mkdir(root, 0700)
			}
			if _, e := j.Digest(context.Background()); e == nil {
				t.Fatal("lost history was accepted")
			}
		})
	}
}
func TestManifestConcurrentWritersAndStreaming(t *testing.T) {
	vault, _ := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	root := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, _ := New(root, vault)
			errs <- j.Put(Record{ID: ID(fmt.Sprint(i)), Kind: "intent"})
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	j, _ := New(root, vault)
	n := 0
	if e := j.Visit(context.Background(), func(Record) error { n++; return nil }); e != nil || n != 32 {
		t.Fatalf("stream %d: %v", n, e)
	}
	bad, _ := security.NewVault(base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012")))
	other, _ := New(root, bad)
	if _, e := other.Digest(context.Background()); e == nil {
		t.Fatal("wrong key accepted")
	}
	if _, e := os.Stat(filepath.Join(root, "manifest.log")); e != nil {
		t.Fatal(e)
	}
}
