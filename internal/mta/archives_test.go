package mta

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
)

func TestCompressedDeliveryEvidenceAndCorruptArchive(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("integration DB required")
	}
	ctx := context.Background()
	sys, e := store.Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer sys.Close()
	if e = sys.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	app, e := store.Open(ctx, os.Getenv("TEST_APP_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer app.Close()
	tenant, key, e := sys.CreateTenant(ctx, "gzip-"+domain.ID(), 100, 100)
	if e != nil {
		t.Fatal(e)
	}
	p, e := app.Authenticate(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	sender := tenant + ".example.test"
	if e = app.AddDomain(ctx, tenant, domain.Domain{ID: domain.ID(), Name: sender, Selector: "test", Token: "x", PublicKey: "test", EncryptedKey: []byte("test")}); e != nil {
		t.Fatal(e)
	}
	for _, corrupt := range []bool{false, true} {
		result, e := app.Enqueue(ctx, p, domain.ID(), "fingerprint", domain.SendRequest{From: "a@" + sender, To: []string{"b@example.net"}, Text: "test", Priority: "normal"}, true)
		if e != nil {
			t.Fatal(e)
		}
		m, e := sys.Claim(ctx, domain.Job{TenantID: tenant, MessageID: result.MessageIDs[0]}, "archive-node")
		if e != nil {
			t.Fatal(e)
		}
		if e = sys.MarkSending(ctx, m); e != nil {
			t.Fatal(e)
		}
		queue := "ARCH" + strings.ReplaceAll(domain.ID(), "-", "")
		lines := fmt.Sprintf("Oct 08 12:00:00 mail postfix/cleanup[1]: %s: message-id=<%s.%s@mail.example.test>\nOct 08 12:00:01 mail postfix/qmgr[2]: %s: from=<a@%s>, size=100, nrcpt=1 (queue active)\nOct 08 12:00:02 mail postfix/smtp[3]: %s: to=<b@example.net>, dsn=2.0.0, status=sent (accepted)\n", queue, m.ID, m.AttemptID, queue, sender, queue)
		path := filepath.Join(t.TempDir(), "mail.log.old.gz")
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		z := gzip.NewWriter(f)
		if _, e = z.Write([]byte(lines)); e != nil {
			t.Fatal(e)
		}
		if e = z.Close(); e != nil {
			t.Fatal(e)
		}
		f.Close()
		if corrupt {
			b, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			b[len(b)-5] ^= 1
			if e = os.WriteFile(path, b, 0600); e != nil {
				t.Fatal(e)
			}
		}
		done, e := scanCompressed(ctx, sys, "archive-node", path)
		got, getErr := app.GetMessage(ctx, tenant, m.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if corrupt {
			if e == nil || got.Status != "dispatching" {
				t.Fatalf("corrupt archive changed state: %s %v", got.Status, e)
			}
			continue
		}
		if e != nil || !done || got.Status != "delivered" {
			t.Fatalf("gzip evidence: %s %v", got.Status, e)
		}
		events, e := app.Events(ctx, tenant, m.ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = scanCompressed(ctx, sys, "archive-node", path); e != nil {
			t.Fatal(e)
		}
		again, e := app.Events(ctx, tenant, m.ID)
		if e != nil || len(events) != len(again) {
			t.Fatalf("duplicate events: %d %d %v", len(events), len(again), e)
		}
	}
}
