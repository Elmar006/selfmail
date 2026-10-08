package broker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestBatchConfirmationFailureModes(t *testing.T) {
	for _, failure := range []string{"none", "nack", "return", "sequence", "closed", "timeout"} {
		t.Run(failure, func(t *testing.T) {
			confirms := make(chan amqp.Confirmation, 3)
			returns := make(chan amqp.Return, 3)
			if failure == "closed" {
				close(confirms)
			} else if failure != "timeout" {
				for i := range 3 {
					seq := uint64(7 + i)
					if failure == "sequence" && i == 1 {
						seq++
					}
					confirms <- amqp.Confirmation{DeliveryTag: seq, Ack: failure != "nack" || i != 1}
				}
			}
			if failure == "return" {
				returns <- amqp.Return{}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			e := waitConfirms(ctx, confirms, returns, 7, 3)
			if (e == nil) != (failure == "none") {
				t.Fatal("incorrect batch acknowledgement", e)
			}
			if (failure == "nack" || failure == "return") && (len(confirms) != 0 || len(returns) != 0) {
				t.Fatal("partial failure left notifications for the next batch")
			}
		})
	}
}

func TestRabbitBatchMandatoryRoutingAndSubsequentBatch(t *testing.T) {
	if os.Getenv("RABBITMQ_URL") == "" {
		t.Skip("RABBITMQ_URL required")
	}
	b, e := OpenNamespace(os.Getenv("RABBITMQ_URL"), "test-"+domain.ID())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	defer func() {
		for _, p := range append(append([]string{}, Priorities...), "dead") {
			b.Pub.QueueDelete(b.Prefix+"."+p, false, false, false)
		}
		b.Pub.ExchangeDelete(b.Prefix+".dispatch", false, false)
		b.Pub.ExchangeDelete(b.Prefix+".dead", false, false)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobs := make([]domain.Dispatch, MaxPublishBatch)
	for i := range jobs {
		jobs[i] = domain.Dispatch{Priority: "normal", Job: domain.Job{MessageID: domain.ID(), TenantID: domain.ID()}}
	}
	jobs[17].Priority = "unbound"
	if e = b.PublishBatch(ctx, jobs); e == nil {
		t.Fatal("partially unroutable batch reported success")
	}
	jobs[17].Priority = "normal"
	if e = b.PublishBatch(ctx, jobs); e != nil {
		t.Fatal("previous returns/confirmations contaminated subsequent batch", e)
	}
	counts := make(map[string]int)
	consumerCtx, stop := context.WithCancel(ctx)
	n := 0
	e = b.Consume(consumerCtx, "normal", func(_ context.Context, job domain.Job) error {
		counts[job.MessageID]++
		n++
		if n == 2*MaxPublishBatch-1 {
			stop()
		}
		return nil
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	for i, job := range jobs {
		want := 2
		if i == 17 {
			want = 1
		}
		if counts[job.Job.MessageID] != want {
			t.Fatalf("missing confirmed reference: job %d copies %d want %d", i, counts[job.Job.MessageID], want)
		}
	}
}
