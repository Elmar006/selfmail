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

func TestIndependentJournalFencesRolledBackSQL(t *testing.T) {
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
	sys.Control = control
	app.Control = control
	sys.Journal = evidence
	app.Journal = evidence
	req := request(p)
	result, e := app.Enqueue(ctx, p, "restore-key", "restore-fingerprint", req, true)
	if e != nil {
		t.Fatal(e)
	}
	m, e := sys.Claim(ctx, domain.Job{TenantID: p.TenantID, MessageID: result.MessageIDs[0]}, "restore-node")
	if e != nil {
		t.Fatal(e)
	}
	if e = sys.MarkSending(ctx, m); e != nil {
		t.Fatal(e)
	}
	if e = sys.FinishSubmission(ctx, m, "submitted", "RECOVERYQUEUE", ""); e != nil {
		t.Fatal(e)
	}
	file := domain.ID()
	if e = sys.ApplyLogLine(ctx, "restore-node", file, 0, 100, "RECOVERYQUEUE", m.ID, m.AttemptID, "", "", "", "", true); e != nil {
		t.Fatal(e)
	}
	if e = sys.ApplyLogLine(ctx, "restore-node", file, 100, 200, "RECOVERYQUEUE", "", "", "sent", m.Recipient, "2.0.0", "accepted", true); e != nil {
		t.Fatal(e)
	}
	if e = control.Hold(ctx, "test older SQL snapshot"); e != nil {
		t.Fatal(e)
	}
	if _, e = app.Enqueue(ctx, p, domain.ID(), "new", req, true); !errors.Is(e, domain.ErrUnavailable) {
		t.Fatal("hold allowed acceptance", e)
	}
	if _, e = sys.Pool.Exec(ctx, "UPDATE messages SET status='queued',attempt_id=NULL,attempt_count=0,queue_id='',lease_until=NULL WHERE id=$1", m.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = sys.Pool.Exec(ctx, "DELETE FROM attempts WHERE message_id=$1", m.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = sys.Pool.Exec(ctx, "DELETE FROM mta_receipts WHERE node_id='restore-node';DELETE FROM mta_log_lines WHERE node_id='restore-node';DELETE FROM mta_log_cursors WHERE node_id='restore-node'"); e != nil {
		t.Fatal(e)
	}
	report, e := sys.ReconcileJournal(ctx)
	if e != nil {
		t.Fatal(e)
	}
	got, e := app.GetMessage(ctx, p.TenantID, m.ID)
	if e != nil || got.Status != "delivered" {
		t.Fatalf("reconciliation: %+v %v", got, e)
	}
	if e = control.Release(ctx, report.Digest); e != nil {
		t.Fatal(e)
	}
	if _, e = sys.Claim(ctx, domain.Job{TenantID: p.TenantID, MessageID: m.ID}, "restore-node"); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("restored delivered message eligible for resend", e)
	}
	replay, exists, e := app.Replay(ctx, p.TenantID, "restore-key", "restore-fingerprint")
	if e != nil || !exists || replay.BatchID != result.BatchID {
		t.Fatalf("idempotency: %+v %v", replay, e)
	}
}
