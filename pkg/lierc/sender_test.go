package lierc

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// logBuffer collects a test logger's output; log.Logger serializes the
// writes, the mutex covers reading while the sender writes.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testSender(send func(string) error) (*sender, *logBuffer) {
	out := &logBuffer{}
	return newSender(log.New(out, "", 0), 0, send), out
}

func TestSenderRateLimit(t *testing.T) {
	type sent struct {
		line string
		at   time.Duration
	}
	start := time.Now()
	ch := make(chan sent, 16)
	s, _ := testSender(func(line string) error {
		ch <- sent{line, time.Since(start)}
		return nil
	})
	defer s.Stop()

	for i := 0; i < 6; i++ {
		s.Send("line")
	}
	// Send never waits for the limiter; generous for a loaded -race run
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("6 Sends took %v", d)
	}
	var all []sent
	for i := 0; i < 6; i++ {
		select {
		case x := <-ch:
			all = append(all, x)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d lines sent", i)
		}
	}
	if all[3].at > 500*time.Millisecond {
		t.Errorf("4th line at %v, want immediate", all[3].at)
	}
	if all[5].at < 1800*time.Millisecond {
		t.Errorf("6th line at %v, want >= ~1.9s", all[5].at)
	}
}

// A full queue drops lines without blocking, and logs once for the burst
// of drops, then how many it dropped once it's taking lines again.
func TestSenderFull(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(block) }) }
	s, out := testSender(func(string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
		return nil
	})
	defer func() { release(); s.Stop() }()

	// the first line is taken off the queue and blocks in send, so the
	// queue then holds exactly sendQueueSize more
	s.Send("first")
	<-entered
	done := make(chan int)
	go func() {
		dropped := 0
		for i := 0; i < sendQueueSize+10; i++ {
			if !s.Send("line") {
				dropped++
			}
		}
		done <- dropped
	}()
	select {
	case dropped := <-done:
		if dropped != 10 {
			t.Errorf("%d lines dropped, want 10", dropped)
		}
	case <-time.After(time.Second):
		t.Fatal("Send blocked on a full queue")
	}
	if n := strings.Count(out.String(), "send queue full, dropping lines"); n != 1 {
		t.Errorf("logged the full queue %d times, want once:\n%s", n, out)
	}

	release()
	eventually(t, "room in the queue", func() bool { return len(s.queue) < sendQueueSize })
	if !s.Send("after") {
		t.Fatal("Send with room in the queue was dropped")
	}
	if !strings.Contains(out.String(), "send queue full: dropped 10 lines") {
		t.Errorf("drop count not logged:\n%s", out)
	}
}

// A send that fails (the connection is gone) drops that line only; later
// lines still go out.
func TestSendWhileDisconnected(t *testing.T) {
	var mu sync.Mutex
	fail := true
	attempts := make(chan string, 4)
	s, _ := testSender(func(line string) error {
		mu.Lock()
		failing := fail
		mu.Unlock()
		attempts <- line
		if failing {
			return errors.New("disconnected")
		}
		return nil
	})
	defer s.Stop()

	s.Send("lost")
	if got := <-attempts; got != "lost" {
		t.Fatalf("first attempt %q", got)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	s.Send("kept")
	select {
	case got := <-attempts:
		if got != "kept" {
			t.Fatalf("second attempt %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("kept was never sent")
	}
}
