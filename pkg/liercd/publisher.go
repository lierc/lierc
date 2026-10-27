package liercd

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

type publisher interface {
	Publish(topic string, body []byte) error
}

type outgoing struct {
	topic string
	body  []byte
}

// Publisher sends events to nsqd in the order they were queued. IRC
// clients queue events from their read loops, so queueing never blocks:
// while nsqd is unreachable events wait in the queue, and once it is
// full new ones are dropped.
type Publisher struct {
	pub      publisher
	queue    chan outgoing
	interval time.Duration
}

// QueueSize is how many events are held while nsqd is unreachable.
const QueueSize = 10000

// Like lierc-logger and lierc-notify, retry nsqd every 5s.
const reconnectInterval = 5 * time.Second

func NewPublisher(pub publisher, size int) *Publisher {
	return &Publisher{
		pub:      pub,
		queue:    make(chan outgoing, size),
		interval: reconnectInterval,
	}
}

// Enqueue encodes v and queues it for topic.
func (p *Publisher) Enqueue(topic string, v interface{}) {
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("encoding %s message: %v", topic, err)
		return
	}

	select {
	case p.queue <- outgoing{topic, body}:
	default:
		log.Printf("dropping %s message, publish queue is full\n%s", topic, body)
	}
}

// Run publishes queued events until ctx is done. A failed publish is
// retried, interval apart, before anything queued after it.
func (p *Publisher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case o := <-p.queue:
			if !p.publish(ctx, o) {
				return
			}
		}
	}
}

// publish sends o, retrying until it succeeds. It returns false if ctx
// is done first.
func (p *Publisher) publish(ctx context.Context, o outgoing) bool {
	failed := false
	for {
		err := p.pub.Publish(o.topic, o.body)
		if err == nil {
			if failed {
				log.Printf("publishing to nsqd again, %d messages queued", len(p.queue))
			}
			return true
		}
		failed = true
		log.Printf("publishing to %s: %v (retrying in %s, %d messages queued)", o.topic, err, p.interval, len(p.queue))

		select {
		case <-ctx.Done():
			return false
		case <-time.After(p.interval):
		}
	}
}
