package store

import (
	"context"
	"errors"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

type batchPublisherFunc func(context.Context, []domain.Dispatch) error

func (f batchPublisherFunc) PublishBatch(ctx context.Context, jobs []domain.Dispatch) error {
	return f(ctx, jobs)
}

func TestOutboxBatchPartialPublishFailureAndReplay(t *testing.T) {
	system, app := setup(t)
	p := tenant(t, system, app, 100)
	ctx := context.Background()
	var ids []string
	for range 3 {
		ids = append(ids, enqueue(t, app, p))
	}
	if _, e := system.Pool.Exec(ctx, "UPDATE outbox SET available_at=now()-interval '10 years' WHERE message_id=ANY($1::uuid[])", ids); e != nil {
		t.Fatal(e)
	}
	var published []domain.Dispatch
	fail := batchPublisherFunc(func(_ context.Context, jobs []domain.Dispatch) error {
		published = append([]domain.Dispatch{}, jobs...)
		return errors.New("last confirm lost after earlier publications")
	})
	if n, e := system.PublishBatch(ctx, fail, 3); e == nil || n != 0 || len(published) != 3 {
		t.Fatal("partial batch committed", n, e, len(published))
	}
	var pending int
	if e := system.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE message_id=ANY($1::uuid[]) AND published_at IS NULL", ids).Scan(&pending); e != nil || pending != 3 {
		t.Fatal("failed batch lost pending references", pending, e)
	}
	succeed := batchPublisherFunc(func(_ context.Context, jobs []domain.Dispatch) error {
		if len(jobs) != len(published) {
			t.Fatal("incomplete replay")
		}
		for i := range jobs {
			if jobs[i] != published[i] {
				t.Fatal("replay changed references")
			}
		}
		return nil
	})
	if n, e := system.PublishBatch(ctx, succeed, 3); e != nil || n != 3 {
		t.Fatal(n, e)
	}
	for _, id := range ids {
		job := domain.Job{MessageID: id, TenantID: p.TenantID}
		if _, e := system.Claim(ctx, job, "batch-node"); e != nil {
			t.Fatal(e)
		}
		if _, e := system.Claim(ctx, job, "batch-node"); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("repeated queue reference claimed twice", e)
		}
	}
}
