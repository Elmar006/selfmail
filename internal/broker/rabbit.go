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
	b.Confirms = ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	b.Returns = ch.NotifyReturn(make(chan amqp.Return, 1))
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

// Publish is single-threaded. Confirm and mandatory return are both required.
func (b *Broker) Publish(ctx context.Context, priority string, job domain.Job) error {
	raw, e := json.Marshal(job)
	if e != nil {
		return e
	}
	seq := b.Pub.GetNextPublishSeqNo()
	if e = b.Pub.PublishWithContext(ctx, b.Prefix+".dispatch", priority, true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: job.MessageID, Timestamp: time.Now(), Body: raw}); e != nil {
		return e
	}
	returned := false
	for {
		select {
		case _, ok := <-b.Returns:
			if !ok {
				return errors.New("publisher return channel closed")
			}
			returned = true
		case c, ok := <-b.Confirms:
			if !ok {
				return errors.New("publisher confirm channel closed")
			}
			if c.DeliveryTag != seq {
				return fmt.Errorf("publisher confirm sequence mismatch")
			}
			// Returns are sent before confirms, but Go selects among ready channels randomly.
			select {
			case _, ok := <-b.Returns:
				if !ok {
					return errors.New("publisher return channel closed")
				}
				returned = true
			default:
			}
			if !c.Ack || returned {
				return errors.New("broker rejected or could not route message")
			}
			return nil
		case <-ctx.Done():
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
