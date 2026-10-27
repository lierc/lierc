package liercd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakePublisher struct {
	mu        sync.Mutex
	published []string // "topic body"
	fails     int      // how many publishes fail before they succeed
}

func (p *fakePublisher) Publish(topic string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fails > 0 {
		p.fails--
		return errors.New("connection refused")
	}
	p.published = append(p.published, topic+" "+string(body))
	return nil
}

func (p *fakePublisher) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		if len(p.published) >= n {
			defer p.mu.Unlock()
			return append([]string(nil), p.published...)
		}
		p.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d publishes", n)
	return nil
}

func newTestPublisher(pub *fakePublisher, size int) *Publisher {
	p := NewPublisher(pub, size)
	p.interval = time.Millisecond
	return p
}

func TestPublisherRetriesInOrder(t *testing.T) {
	pub := &fakePublisher{fails: 3}
	p := newTestPublisher(pub, 10)

	p.Enqueue("chats", 1)
	p.Enqueue("multi", 2)
	p.Enqueue("chats", 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	got := pub.wait(t, 3)
	want := []string{"chats 1", "multi 2", "chats 3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("published %v, want %v", got, want)
		}
	}
}

func TestPublisherDropsWhenFull(t *testing.T) {
	pub := &fakePublisher{}
	p := newTestPublisher(pub, 2)

	// Run isn't started, so nothing leaves the queue
	p.Enqueue("chats", 1)
	p.Enqueue("chats", 2)
	p.Enqueue("chats", 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	got := pub.wait(t, 2)
	time.Sleep(10 * time.Millisecond)
	if len(got) != 2 || got[0] != "chats 1" || got[1] != "chats 2" {
		t.Fatalf("published %v, want the first two", got)
	}
}

func TestPublisherStopsWhileRetrying(t *testing.T) {
	pub := &fakePublisher{fails: 1 << 30}
	p := newTestPublisher(pub, 10)
	p.Enqueue("chats", 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run kept retrying after ctx was cancelled")
	}
}

func TestPublisherSkipsUnencodable(t *testing.T) {
	pub := &fakePublisher{}
	p := newTestPublisher(pub, 10)

	p.Enqueue("chats", make(chan int))
	p.Enqueue("chats", 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	if got := pub.wait(t, 1); got[0] != "chats 1" {
		t.Fatalf("published %v, want only chats 1", got)
	}
}
