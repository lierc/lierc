package lierc

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSenderRateLimit(t *testing.T) {
	type sent struct {
		line string
		at   time.Duration
	}
	start := time.Now()
	ch := make(chan sent, 16)
	s := newSender("t", func(line string) error {
		ch <- sent{line, time.Since(start)}
		return nil
	})
	defer s.Stop()

	for i := 0; i < 6; i++ {
		s.Send("line")
	}
	if d := time.Since(start); d > 10*time.Millisecond {
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

func TestSenderFull(t *testing.T) {
	block := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(block) }) }
	s := newSender("t", func(string) error { <-block; return nil })
	defer func() { release(); s.Stop() }()

	done := make(chan struct{})
	go func() {
		for i := 0; i < sendQueueSize+10; i++ {
			s.Send("line")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Send blocked on a full queue")
	}
}

func TestSendWhileDisconnected(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	fail := true
	s := newSender("t", func(line string) error {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return errors.New("disconnected")
		}
		lines = append(lines, line)
		return nil
	})
	defer s.Stop()

	s.Send("lost")
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	fail = false
	mu.Unlock()
	s.Send("kept")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(lines)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || lines[0] != "kept" {
		t.Fatalf("lines = %v", lines)
	}
}
