package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Elmar006/selfmail/internal/application"
	"github.com/Elmar006/selfmail/internal/broker"
	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/mailmsg"
	"github.com/Elmar006/selfmail/internal/mta"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/internal/telemetry"
)

type DeliveryRepository interface {
	Claim(context.Context, domain.Job, string) (domain.Message, error)
	DeliveryPolicy(context.Context, string) (int, bool, error)
	GetDomain(context.Context, string, string) (domain.Domain, error)
	MarkSending(context.Context, domain.Message) error
	FinishSubmission(context.Context, domain.Message, string, string, string) error
	Retry(context.Context, domain.Message, string, bool, time.Duration) error
	Recover(context.Context) error
}
type DeliveryLimiter interface {
	Take(context.Context, string, string, int) (time.Duration, error)
}
type DeliveryTransport interface {
	Submit(context.Context, string, string, string, []byte) mta.Result
	LocalHostname() string
}
type Worker struct {
	Store                DeliveryRepository
	Limiter              DeliveryLimiter
	Vault                *security.Vault
	MTA                  DeliveryTransport
	NodeID, BounceDomain string
	BounceKey            []byte
	AllowUnverified      bool
	WireLimit            int
	Budget               *application.WorkBudget
}

func (w *Worker) Handle(ctx context.Context, job domain.Job) error {
	ctx, release, e := w.Budget.AcquireWait(ctx)
	if e != nil {
		return e
	}
	defer release()
	if guarded, ok := w.Store.(interface {
		DeliveryPermit(context.Context) (func(), error)
	}); ok {
		release, e := guarded.DeliveryPermit(ctx)
		if e != nil {
			return e
		}
		defer release()
	}
	m, e := w.Store.Claim(ctx, job, w.NodeID)
	if errors.Is(e, domain.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	retry := func(reason string, permanent bool, delay time.Duration) error {
		return w.Store.Retry(ctx, m, reason, permanent, delay)
	}
	rate, paused, e := w.Store.DeliveryPolicy(ctx, m.TenantID)
	if e != nil {
		return retry("tenant state unavailable", false, 30*time.Second)
	}
	if paused {
		return retry("project paused", false, time.Minute)
	}
	wait, e := w.Limiter.Take(ctx, m.TenantID, domain.SenderDomain(m.Recipient), rate)
	if e != nil {
		return retry("rate limiter unavailable", false, 30*time.Second)
	}
	if wait > 0 {
		return retry("rate limited", false, max(wait, time.Second))
	}
	d, e := w.Store.GetDomain(ctx, m.TenantID, domain.SenderDomain(m.From))
	if e != nil {
		return retry("sender domain unavailable", false, time.Minute)
	}
	if !d.Verified && !w.AllowUnverified {
		return retry("sender domain unverified", true, 0)
	}
	raw, e := mailmsg.Build(m, w.MTA.LocalHostname())
	if e != nil {
		return retry("MIME construction failed", true, 0)
	}
	key, e := w.Vault.Open(d.EncryptedKey, "dkim:"+d.ID)
	if e != nil {
		return retry("DKIM key unavailable", true, 0)
	}
	raw, e = mailmsg.Sign(raw, d, key)
	if e != nil {
		return retry("DKIM signing failed", true, 0)
	}
	limit := w.WireLimit
	if limit == 0 {
		limit = mailmsg.MaxWireBytes
	}
	if len(raw) > limit {
		return retry("signed MIME exceeds configured wire budget", true, 0)
	}
	if e = mailmsg.ValidateLines(raw); e != nil {
		return retry("signed MIME line budget exceeded", true, 0)
	}
	if e = w.Store.MarkSending(ctx, m); e != nil {
		if errors.Is(e, domain.ErrSuppressed) {
			return retry("recipient suppressed", true, 0)
		}
		if errors.Is(e, domain.ErrConflict) {
			return nil
		}
		return retry("sending precondition failed", false, time.Minute)
	}
	envelope := m.From
	if w.BounceDomain != "" {
		envelope = mta.BounceAddress(m.AttemptID, w.BounceDomain, w.BounceKey)
	}
	result := w.MTA.Submit(ctx, envelope, m.Recipient, m.AttemptID, raw)
	// Persist with a fresh context even during shutdown. A failure leaves the sending
	// lease for reconciliation; another delivery will never blindly resubmit it.
	persist, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result.Err == nil {
		telemetry.Handoff("accepted")
		return w.Store.FinishSubmission(persist, m, "submitted", result.QueueID, "")
	}
	if result.Unknown {
		telemetry.Handoff("unknown")
		return w.Store.FinishSubmission(persist, m, "submission_unknown", "", truncate(result.Err.Error(), 512))
	}
	telemetry.Handoff("rejected")
	return w.Store.Retry(persist, m, truncate(result.Err.Error(), 512), result.Permanent, store.RetryDelay(m.AttemptCount))
}
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func RunDispatcher(ctx context.Context, s *store.Store, url string) error {
	for ctx.Err() == nil {
		b, e := broker.Open(url)
		telemetry.Progress("dispatcher_connect", e)
		if e != nil {
			slog.Warn("broker connection unavailable", "error", e)
			if !pause(ctx, 2*time.Second) {
				break
			}
			continue
		}
		for ctx.Err() == nil {
			op, cancel := context.WithTimeout(ctx, 8*time.Second)
			n, e := s.PublishBatch(op, b, 32)
			telemetry.Progress("dispatcher", e)
			cancel()
			if e != nil {
				slog.Warn("outbox publish will retry", "error", e)
				break
			}
			if n == 0 && !pause(ctx, 250*time.Millisecond) {
				break
			}
		}
		b.Close()
		if !pause(ctx, time.Second) {
			break
		}
	}
	return ctx.Err()
}
func RunConsumers(ctx context.Context, w *Worker, url string, n int) error {
	if w.Budget == nil {
		w.Budget = application.NewWorkBudget(4)
	}
	var wg sync.WaitGroup
	for _, priority := range broker.Priorities {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(priority string) {
				defer wg.Done()
				for ctx.Err() == nil {
					b, e := broker.Open(url)
					if e == nil {
						telemetry.Consumer(priority, 1)
						e = b.Consume(ctx, priority, w.Handle)
						telemetry.Consumer(priority, -1)
						b.Close()
					}
					if ctx.Err() == nil {
						slog.Warn("consumer reconnect", "priority", priority, "error", e)
					}
					if !pause(ctx, 2*time.Second) {
						return
					}
				}
			}(priority)
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			e := w.Store.Recover(ctx)
			telemetry.Progress("watchdog", e)
			if e != nil && ctx.Err() == nil {
				slog.Warn("lease recovery failed", "error", e)
			}
			if !pause(ctx, 15*time.Second) {
				return
			}
		}
	}()
	wg.Wait()
	return ctx.Err()
}
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func Run(ctx context.Context, fn func(context.Context) error) error {
	e := fn(ctx)
	if errors.Is(e, context.Canceled) {
		return nil
	}
	return fmt.Errorf("worker stopped: %w", e)
}
