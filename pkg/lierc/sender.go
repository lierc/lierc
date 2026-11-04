package lierc

import (
	"context"
	"log"
	"sync"

	"golang.org/x/time/rate"
)

const sendQueueSize = 256

// sender rate-limits our own sends (1/s, burst 4) and never blocks the caller.
type sender struct {
	log     *log.Logger
	debug   int64
	send    func(line string) error
	queue   chan string
	limiter *rate.Limiter
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once

	mu      sync.Mutex
	dropped int // lines dropped since the queue was last accepting
}

// newSender starts the goroutine that drains the queue through send. It
// logs to logger: dropped sends only when debug > 0, and a full queue
// once per burst of drops.
func newSender(logger *log.Logger, debug int64, send func(line string) error) *sender {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sender{
		log:     logger,
		debug:   debug,
		send:    send,
		queue:   make(chan string, sendQueueSize),
		limiter: rate.NewLimiter(1, 4),
		ctx:     ctx,
		cancel:  cancel,
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
				s.log.Printf("dropped send: %v", err)
			}
		}
	}
}

// Send queues line without blocking and reports whether it did; when the
// queue is full or the sender is stopped the line is dropped.
func (s *sender) Send(line string) bool {
	if s.ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.queue <- line:
		s.logDropped()
		return true
	default:
		if s.dropped == 0 {
			s.log.Print("send queue full, dropping lines")
		}
		s.dropped++
		return false
	}
}

// logDropped reports how many lines a full queue dropped, once it's
// taking lines again (or the sender stops). s.mu is held.
func (s *sender) logDropped() {
	if s.dropped > 0 {
		s.log.Printf("send queue full: dropped %d lines", s.dropped)
		s.dropped = 0
	}
}

// Stop ends the goroutine; Send after Stop is a no-op.
func (s *sender) Stop() {
	s.once.Do(func() {
		s.cancel()
		s.mu.Lock()
		s.logDropped()
		s.mu.Unlock()
	})
}
