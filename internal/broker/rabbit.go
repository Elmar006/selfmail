package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	amqp "github.com/rabbitmq/amqp091-go"
)

var Priorities = []string{"critical", "normal", "bulk"}

const MaxPublishBatch = 64

type Broker struct {
	Conn     *amqp.Connection
	Pub      *amqp.Channel
	Confirms <-chan amqp.Confirmation
	Returns  <-chan amqp.Return
	Prefix   string
}

func Open(raw string) (*Broker, error) {
	return OpenNamespace(raw, "mail")
}
func OpenNamespace(raw, prefix string) (*Broker, error) {
	conn, e := amqp.DialConfig(raw, amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(5 * time.Second)})
	if e != nil {
		return nil, e
	}
	ch, e := conn.Channel()
	if e != nil {
		conn.Close()
		return nil, e
	}
	b := &Broker{Conn: conn, Pub: ch, Prefix: prefix}
	if e = b.Topology(); e != nil {
		b.Close()
		return nil, e
	}
	if e = ch.Confirm(false); e != nil {
		b.Close()
		return nil, e
	}
	b.Confirms = ch.NotifyPublish(make(chan amqp.Confirmation, MaxPublishBatch))
	b.Returns = ch.NotifyReturn(make(chan amqp.Return, MaxPublishBatch))
	return b, nil
}
func (b *Broker) Close() { b.Pub.Close(); b.Conn.Close() }
func (b *Broker) Topology() error {
	if e := b.Pub.ExchangeDeclare(b.Prefix+".dispatch", "direct", true, false, false, false, nil); e != nil {
		return e
	}
	if e := b.Pub.ExchangeDeclare(b.Prefix+".dead", "fanout", true, false, false, false, nil); e != nil {
		return e
	}
	if _, e := b.Pub.QueueDeclare(b.Prefix+".dead", true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); e != nil {
		return e
	}
	if e := b.Pub.QueueBind(b.Prefix+".dead", "", b.Prefix+".dead", false, nil); e != nil {
		return e
	}
	for _, p := range Priorities {
		_, e := b.Pub.QueueDeclare(b.Prefix+"."+p, true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": 20, "x-dead-letter-exchange": b.Prefix + ".dead", "x-dead-letter-strategy": "at-least-once", "x-max-length": int64(100000), "x-overflow": "reject-publish"})
		if e != nil {
			return e
		}
		if e = b.Pub.QueueBind(b.Prefix+"."+p, p, b.Prefix+".dispatch", false, nil); e != nil {
			return e
		}
	}
	return nil
}

// Publish and PublishBatch share a single publisher channel and must be called
// serially. Confirm and mandatory routing are both required.
func (b *Broker) Publish(ctx context.Context, priority string, job domain.Job) error {
	return b.PublishBatch(ctx, []domain.Dispatch{{Priority: priority, Job: job}})
}

// PublishBatch pipelines bounded persistent publications, then waits for every
// confirm and mandatory return. On partial success the SQL outbox retries the
// whole batch; message claims tolerate repeated queue references. A timeout or
// channel failure requires discarding this connection before another batch.
func (b *Broker) PublishBatch(ctx context.Context, jobs []domain.Dispatch) error {
	if len(jobs) < 1 || len(jobs) > MaxPublishBatch {
		return fmt.Errorf("publisher batch requires 1..%d jobs", MaxPublishBatch)
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	body := make([][]byte, len(jobs))
	for i, item := range jobs {
		var e error
		if body[i], e = json.Marshal(item.Job); e != nil {
			return e
		}
	}
	seq := b.Pub.GetNextPublishSeqNo()
	for i, item := range jobs {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := b.Pub.PublishWithContext(ctx, b.Prefix+".dispatch", item.Priority, true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: item.Job.MessageID, Timestamp: time.Now(), Body: body[i]}); e != nil {
			return e
		}
	}
	return waitConfirms(ctx, b.Confirms, b.Returns, seq, len(jobs))
}

func waitConfirms(ctx context.Context, confirms <-chan amqp.Confirmation, returns <-chan amqp.Return, first uint64, count int) error {
	rejected := false
	for confirmed := 0; confirmed < count; {
		select {
		case _, ok := <-returns:
			if !ok {
				return errors.New("publisher return channel closed")
			}
			rejected = true
		case c, ok := <-confirms:
			if !ok {
				return errors.New("publisher confirm channel closed")
			}
			if c.DeliveryTag != first+uint64(confirmed) {
				return fmt.Errorf("publisher confirm sequence mismatch")
			}
			rejected = rejected || !c.Ack
			confirmed++
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// amqp091 delivers returns before their corresponding confirms, but select
	// can choose a ready confirm first. Drain all returns before declaring success.
	for {
		select {
		case _, ok := <-returns:
			if !ok {
				return errors.New("publisher return channel closed")
			}
			rejected = true
		default:
			if rejected {
				return errors.New("broker rejected or could not route batch")
			}
			return ctx.Err()
		}
	}
}
func (b *Broker) Consume(ctx context.Context, priority string, handle func(context.Context, domain.Job) error) error {
	ch, e := b.Conn.Channel()
	if e != nil {
		return e
	}
	defer ch.Close()
	if e = ch.Qos(1, 0, false); e != nil {
		return e
	}
	deliveries, e := ch.Consume(b.Prefix+"."+priority, "", false, false, false, false, nil)
	if e != nil {
		return e
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("consumer connection closed")
			}
			var job domain.Job
			if e = json.Unmarshal(d.Body, &job); e != nil || !domain.ValidID(job.MessageID) || !domain.ValidID(job.TenantID) {
				if e = d.Reject(false); e != nil {
					return e
				}
				continue
			}
			if e = handle(ctx, job); e != nil {
				_ = d.Nack(false, true)
				return e
			} // reconnect loop backs off; no hot requeue.
			if e = d.Ack(false); e != nil {
				return e
			}
		}
	}
}
