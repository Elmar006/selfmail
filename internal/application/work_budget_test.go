package application

import (
	"context"
	"errors"
	"testing"

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
