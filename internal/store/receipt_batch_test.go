package store

import (
	"context"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestLogBatchRollbackReplayAndTerminalStatus(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	ctx := context.Background()
	id := enqueue(t, app, p)
	m, e := system.Claim(ctx, domain.Job{TenantID: p.TenantID, MessageID: id}, "batch-node")
	if e != nil {
		t.Fatal(e)
	}
	if e = system.MarkSending(ctx, m); e != nil {
		t.Fatal(e)
	}
	file, queue := domain.ID(), domain.ID()
	lines := []LogLine{
		{Offset: 0, Next: 100, QueueID: queue, MessageID: id, AttemptID: m.AttemptID, Parsed: true},
		{Offset: 100, Next: 200, QueueID: queue, Status: "active", Parsed: true},
		{Offset: 200, Next: 300, QueueID: queue, Status: "sent", Recipient: m.Recipient, DSN: "2.0.0", Parsed: true},
		{Offset: 300, Next: 400, QueueID: "OTHER", MessageID: id, AttemptID: "invalid-uuid", Parsed: true},
	}
	if e = system.ApplyLogLines(ctx, "batch-node", file, lines); e == nil {
		t.Fatal("invalid final line did not roll back the batch")
	}
	got, e := app.GetMessage(ctx, p.TenantID, id)
	if e != nil || got.Status != "dispatching" {
		t.Fatal("partial batch changed message status", got.Status, e)
	}
	if offset, e := system.LogCursor(ctx, "batch-node", file); e != nil || offset != 0 {
		t.Fatal("failed batch advanced cursor", offset, e)
	}
	var count int
	if e = system.Pool.QueryRow(ctx, "SELECT count(*) FROM mta_log_lines WHERE node_id='batch-node' AND file_id=$1", file).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed batch committed dedupe lines", count, e)
	}
	lines = lines[:3]
	if e = system.ApplyLogLines(ctx, "batch-node", file, lines); e != nil {
		t.Fatal(e)
	}
	events, e := app.Events(ctx, p.TenantID, id)
	if e != nil {
		t.Fatal(e)
	}
	if e = system.ApplyLogLines(ctx, "batch-node", file, lines); e != nil {
		t.Fatal(e)
	}
	again, e := app.Events(ctx, p.TenantID, id)
	if e != nil || len(events) != len(again) {
		t.Fatal("batch replay duplicated events", e)
	}
	if e = system.ApplyLogLines(ctx, "batch-node", file, []LogLine{{Offset: 300, Next: 400, QueueID: queue, Status: "deferred", Recipient: m.Recipient, Parsed: true}}); e != nil {
		t.Fatal(e)
	}
	got, e = app.GetMessage(ctx, p.TenantID, id)
	if e != nil || got.Status != "delivered" {
		t.Fatal("late log batch regressed terminal state", got.Status, e)
	}
}

func TestLogBatchRejectsGapsBeforeWriting(t *testing.T) {
	s := &Store{}
	for _, lines := range [][]LogLine{
		nil,
		make([]LogLine, MaxLogBatch+1),
		{{Offset: 0, Next: 100}, {Offset: 101, Next: 200}},
		{{Offset: -1, Next: 100}},
		{{Offset: 100, Next: 100}},
	} {
		if e := s.ApplyLogLines(context.Background(), "node", "file", lines); e == nil {
			t.Fatal("invalid batch was accepted")
		}
	}
}
