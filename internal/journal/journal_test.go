package journal

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/Elmar006/selfmail/internal/security"
)

func TestImmutableEncryptedEvidence(t *testing.T) {
	vault, _ := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	j, e := New(t.TempDir(), vault)
	if e != nil {
		t.Fatal(e)
	}
	r := Record{ID: ID("intent", "attempt"), Kind: "intent", Recipient: "private@example.test"}
	if e = j.Put(r); e != nil {
		t.Fatal(e)
	}
	if e = j.Put(r); e != nil {
		t.Fatal(e)
	}
	path, _ := j.path(r.ID)
	b, e := os.ReadFile(path)
	if e != nil || strings.Contains(string(b), r.Recipient) {
		t.Fatal("plaintext evidence", e)
	}
	r.Status = "changed"
	if e = j.Put(r); e == nil {
		t.Fatal("overwrote immutable record")
	}
	b[len(b)-1] ^= 1
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = j.Records(context.Background()); e == nil {
		t.Fatal("accepted corrupt journal")
	}
}
