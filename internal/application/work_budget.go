package application

import (
	"context"

	"github.com/Elmar006/selfmail/internal/domain"
)

type budgetKey struct{}

// WorkBudget bounds simultaneous large decoded requests and MIME buffers. A
// permit acquired before reading HTTP/SMTP bodies is reused by Service.Send.
type WorkBudget struct{ slots chan struct{} }

func NewWorkBudget(n int) *WorkBudget {
	if n < 1 {
		n = 1
	}
	return &WorkBudget{make(chan struct{}, n)}
}
func (b *WorkBudget) Acquire(ctx context.Context) (context.Context, func(), error) {
	if b == nil || ctx.Value(budgetKey{}) == b {
		return ctx, func() {}, nil
	}
	select {
	case b.slots <- struct{}{}:
		return context.WithValue(ctx, budgetKey{}, b), func() { <-b.slots }, nil
	default:
		return ctx, nil, domain.ErrUnavailable
	}
}
