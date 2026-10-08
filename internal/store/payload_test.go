package store

import (
	"context"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestBatchSharesPayloadAcrossRecipients(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	r := request(p)
	r.To = []string{"a@example.net", "b@example.net", "c@example.net"}
	r.Attachments = []domain.Attachment{{Filename: "receipt.pdf", Data: []byte("%PDF-test")}}
	batch, e := app.Enqueue(context.Background(), p, domain.ID(), "hash", r, true)
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if e = system.Pool.QueryRow(context.Background(), "SELECT count(*) FROM batch_payloads WHERE batch_id=$1", batch.BatchID).Scan(&count); e != nil || count != 1 {
		t.Fatal("payload duplicated", count, e)
	}
	for _, id := range batch.MessageIDs {
		m, e := system.Claim(context.Background(), domain.Job{MessageID: id, TenantID: p.TenantID}, "test-mta")
		if e != nil || len(m.Payload.Attachments) != 1 || string(m.Payload.Attachments[0].Data) != "%PDF-test" {
			t.Fatal("shared payload unavailable", m, e)
		}
	}
}
