package store

import (
	"context"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestCleanupAloneDoesNotProveQueueCommit(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	id := enqueue(t, app, p)
	m, e := system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta")
	if e != nil {
		t.Fatal(e)
	}
	if e = system.MarkSending(context.Background(), m); e != nil {
		t.Fatal(e)
	}
	if e = system.FinishSubmission(context.Background(), m, "submission_unknown", "", "no final response"); e != nil {
		t.Fatal(e)
	}
	file := domain.ID()
	queue := domain.ID()
	if e = system.ApplyLogLine(context.Background(), "test-mta", file, 0, 100, queue, id, m.AttemptID, "", "", "", "", true); e != nil {
		t.Fatal(e)
	}
	got, e := app.GetMessage(context.Background(), p.TenantID, id)
	if e != nil || got.Status != "submission_unknown" {
		t.Fatal("cleanup was mistaken for durable acceptance", got, e)
	}
	if e = system.ApplyLogLine(context.Background(), "test-mta", file, 100, 200, queue, "", "", "active", "", "", "", true); e != nil {
		t.Fatal(e)
	}
	got, e = app.GetMessage(context.Background(), p.TenantID, id)
	if e != nil || got.Status != "submitted" {
		t.Fatal("queue activation not reconciled", got, e)
	}
}
