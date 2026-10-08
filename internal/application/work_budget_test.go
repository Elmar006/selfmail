package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestBudgetSharedAcrossIngressAndService(t *testing.T) {
	b := NewWorkBudget(1)
	ctx, release, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	_, inner, e := b.Acquire(ctx)
	if e != nil {
		t.Fatal("nested service reacquired body permit", e)
	}
	inner()
	if _, _, e = b.Acquire(context.Background()); !errors.Is(e, domain.ErrUnavailable) {
		t.Fatal("accepted excess heavy work", e)
	}
	release()
	_, release, e = b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	release()
}

func TestWaitingBudgetBackpressureAndCancellation(t *testing.T) {
	b := NewWorkBudget(1)
	_, release, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	waiting, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, e = b.AcquireWait(waiting); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("busy consumer should wait until cancellation", e)
	}
	done := make(chan error, 1)
	go func() {
		ctx, free, err := b.AcquireWait(context.Background())
		if err == nil {
			_, nested, innerErr := b.AcquireWait(ctx)
			if innerErr == nil {
				nested()
			}
			free()
			err = innerErr
		}
		done <- err
	}()
	select {
	case e := <-done:
		t.Fatal("consumer acquired an occupied slot", e)
	default:
	}
	release()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("consumer did not resume after release")
	}
	if _, _, e = b.AcquireWait(waiting); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("cancelled context acquired a free slot", e)
	}
	_, free, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal("cancelled waiter leaked capacity", e)
	}
	free()
}
