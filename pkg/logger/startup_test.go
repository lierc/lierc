package logger

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lierc/lierc/pkg/lierc"
)

// highlightStore fails Highlights a number of times before it works, like
// a database whose schema lierc-api hasn't migrated yet.
type highlightStore struct {
	fakeStore
	failures int
	calls    int
}

func (s *highlightStore) Highlights(ctx context.Context) (map[string]string, error) {
	s.calls++
	if s.calls <= s.failures {
		return nil, errors.New(`relation "connections" does not exist`)
	}
	return map[string]string{"c1": `["leedo"]`}, nil
}

func fastRetries(t *testing.T) {
	old := retryInterval
	retryInterval = time.Millisecond
	t.Cleanup(func() { retryInterval = old })
}

// At start, highlights that can't be loaded yet are retried, not left
// empty: an empty set would silently stop highlights and notifications.
func TestLoadHighlightsRetriesAtStart(t *testing.T) {
	fastRetries(t)
	store := &highlightStore{failures: 2}
	l := &Logger{store: store, highlights: &Highlighters{}}
	if err := l.loadHighlights(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.calls != 3 {
		t.Errorf("Highlights called %d times, want 3", store.calls)
	}
	m := &lierc.IRCMessage{Command: "PRIVMSG", Params: []string{"#a", "hi leedo"}, Prefix: &lierc.IRCPrefix{Name: "bob"}}
	if !l.highlights.Match("c1", m) {
		t.Error("c1's highlight wasn't loaded")
	}
}

func TestLoadHighlightsStopsOnCancel(t *testing.T) {
	fastRetries(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := &Logger{store: &highlightStore{failures: 1 << 30}, highlights: &Highlighters{}}
	if err := l.loadHighlights(ctx); err == nil {
		t.Fatal("no error after the context was cancelled")
	}
}
