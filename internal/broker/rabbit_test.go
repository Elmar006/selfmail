package broker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestRabbitConfirmedRoutingAndRedelivery(t *testing.T) {
	raw := os.Getenv("RABBITMQ_URL")
	if raw == "" {
		t.Skip("RABBITMQ_URL not set")
	}
	b, e := OpenNamespace(raw, "test-"+domain.ID())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	defer func() {
		for _, p := range append(Priorities, "dead") {
			b.Pub.QueueDelete(b.Prefix+"."+p, false, false, false)
		}
		b.Pub.ExchangeDelete(b.Prefix+".dispatch", false, false)
		b.Pub.ExchangeDelete(b.Prefix+".dead", false, false)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	job := domain.Job{MessageID: domain.ID(), TenantID: domain.ID()}
	if e = b.Publish(ctx, "unbound", job); e == nil {
		t.Fatal("unroutable message marked confirmed")
	}
	if e = b.Publish(ctx, "critical", job); e != nil {
		t.Fatal(e)
	}
	first := true
	consumerCtx, stop := context.WithCancel(ctx)
	e = b.Consume(consumerCtx, "critical", func(ctx context.Context, got domain.Job) error {
		if got != job {
			t.Error("payload changed")
		}
		if first {
			first = false
			return errors.New("simulate crash before SQL commit")
		}
		return nil
	})
	if e == nil {
		t.Fatal("handler failure swallowed")
	}
	e = b.Consume(consumerCtx, "critical", func(ctx context.Context, got domain.Job) error {
		if got != job {
			t.Error("wrong redelivery")
		}
		stop()
		return nil
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("%v", e)
	}
	q, e := b.Pub.QueueInspect(b.Prefix + ".critical")
	if e != nil || q.Messages != 0 {
		t.Fatalf("message not acknowledged: %+v %v", q, e)
	}
}
