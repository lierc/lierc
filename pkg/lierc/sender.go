package lierc

import (
	"context"
	"log"
	"os"
	"sync"

	"golang.org/x/time/rate"
)

const sendQueueSize = 256

// sender rate-limits our own sends (1/s, burst 4) and never blocks the caller.
type sender struct {
	id      string
	send    func(line string) error
	queue   chan string
	limiter *rate.Limiter
	ctx     context.Context
	cancel  context.CancelFunc
	debug   int64
	once    sync.Once
}

// newSender starts the goroutine that drains the queue through send.
func newSender(id string, send func(line string) error) *sender {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sender{
		id:      id,
		send:    send,
		queue:   make(chan string, sendQueueSize),
		limiter: rate.NewLimiter(1, 4),
		ctx:     ctx,
		cancel:  cancel,
		debug:   LogLevel(os.Getenv("LIERC_DEBUG")),
	}
	go s.run()
	return s
}

func (s *sender) run() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case line := <-s.queue:
			if err := s.limiter.Wait(s.ctx); err != nil {
				return
			}
			if err := s.send(line); err != nil && s.debug > 0 {
				log.Printf("%s dropped send: %v", s.id, err)
			}
		}
	}
}

// Send queues line without blocking; when the queue is full or the sender
// is stopped the line is dropped.
func (s *sender) Send(line string) {
	if s.ctx.Err() != nil {
		return
	}
	select {
	case s.queue <- line:
	default:
		log.Printf("%s send queue full, dropping", s.id)
	}
}

// Stop ends the goroutine; Send after Stop is a no-op.
func (s *sender) Stop() {
	s.once.Do(s.cancel)
}
