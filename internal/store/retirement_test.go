package store

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
)

func TestRecoveryHonorsRetentionAndKeyReuse(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata_tombstone", true: "expired_key_reused"}[reuse], func(t *testing.T) {
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
			req := request(p)
			old, e := app.Enqueue(ctx, p, "reused-key", "old-fingerprint", req, true)
			if e != nil {
				t.Fatal(e)
			}
			m, e := sys.Claim(ctx, domain.Job{TenantID: p.TenantID, MessageID: old.MessageIDs[0]}, "retirement-node")
			if e != nil {
				t.Fatal(e)
			}
			if e = sys.MarkSending(ctx, m); e != nil {
				t.Fatal(e)
			}
			if e = sys.FinishSubmission(ctx, m, "submitted", domain.ID(), ""); e != nil {
				t.Fatal(e)
			}
			if _, e = sys.Pool.Exec(ctx, "UPDATE messages SET status='delivered',updated_at=clock_timestamp()-interval '40 days' WHERE id=$1", m.ID); e != nil {
				t.Fatal(e)
			}
			if reuse {
				if _, e = sys.Pool.Exec(ctx, "UPDATE batches SET created_at=clock_timestamp()-interval '100 days' WHERE id=$1", old.BatchID); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = sys.Cleanup(ctx, DefaultRetention(), false); e != nil {
				t.Fatal(e)
			}
			var newer domain.SendResult
			if reuse {
				newer, e = app.Enqueue(ctx, p, "reused-key", "new-fingerprint", req, true)
				if e != nil || newer.BatchID == old.BatchID {
					t.Fatalf("key reuse: %+v %v", newer, e)
				}
			}
			if e = control.Hold(ctx, "retention-aware recovery regression"); e != nil {
				t.Fatal(e)
			}
			if _, e = sys.ReconcileJournal(ctx); e != nil {
				t.Fatal(e)
			}
			_, e = app.GetMessage(ctx, p.TenantID, m.ID)
			if (!reuse && !errors.Is(e, domain.ErrGone)) || (reuse && !errors.Is(e, domain.ErrNotFound)) {
				t.Fatalf("expired history reintroduced: %v", e)
			}
			if reuse {
				got, exists, e := app.Replay(ctx, p.TenantID, "reused-key", "new-fingerprint")
				if e != nil || !exists || got.BatchID != newer.BatchID {
					t.Fatalf("new operation lost: %+v %v", got, e)
				}
				current, e := app.GetMessage(ctx, p.TenantID, newer.MessageIDs[0])
				if e != nil || current.Status != "queued" {
					t.Fatalf("new message changed: %+v %v", current, e)
				}
			} else {
				got, exists, e := app.Replay(ctx, p.TenantID, "reused-key", "old-fingerprint")
				if e != nil || !exists || got.BatchID != old.BatchID {
					t.Fatalf("tombstone lost: %+v %v", got, e)
				}
			}
		})
	}
}
