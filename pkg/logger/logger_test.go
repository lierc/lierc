package logger

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/lierc/lierc/pkg/lierc"
	"github.com/nsqio/go-nsq"
)

type fakeStore struct {
	messages []string // "connection channel command highlight"
	privates []string // "connection nick"
	err      error
	nextId   int
}

func (s *fakeStore) InsertMessage(ctx context.Context, connection string, m *lierc.IRCMessage, channel string, highlight bool) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.nextId++
	s.messages = append(s.messages, connection+" "+channel+" "+m.Command+map[bool]string{true: " highlight", false: ""}[highlight])
	return s.nextId, nil
}

func (s *fakeStore) InsertMessages(ctx context.Context, connection string, m *lierc.IRCMessage, channels []string) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	for _, channel := range channels {
		s.nextId++
		s.messages = append(s.messages, connection+" "+channel+" "+m.Command)
	}
	return s.nextId, nil
}

func (s *fakeStore) InsertPrivate(ctx context.Context, connection string, nick string, time float64) error {
	if s.err != nil {
		return s.err
	}
	s.privates = append(s.privates, connection+" "+nick)
	return nil
}

func (s *fakeStore) Highlights(ctx context.Context) (map[string]string, error) {
	return nil, nil
}

type fakePublisher struct {
	published []string // "topic id"
	err       error
}

func (p *fakePublisher) Publish(topic string, body []byte) error {
	var e LoggedMessage
	if err := json.Unmarshal(body, &e); err != nil {
		panic(err)
	}
	p.published = append(p.published, topic+" "+e.Message.Command+" "+string(rune('0'+e.MessageId)))
	return p.err
}

func newTestLogger() (*Logger, *fakeStore, *fakePublisher) {
	store := &fakeStore{}
	pub := &fakePublisher{}
	h := &Highlighters{}
	h.Replace(map[string]*regexp.Regexp{"c1": regexp.MustCompile(`(?i)\bleedo\b`)})
	return &Logger{store: store, pub: pub, highlights: h}, store, pub
}

func chat(t *testing.T, m *lierc.IRCMessage) *nsq.Message {
	body, err := json.Marshal(lierc.IRCClientMessage{Id: "c1", Message: m})
	if err != nil {
		t.Fatal(err)
	}
	return &nsq.Message{Body: body}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHandleChat(t *testing.T) {
	prefix := func(name string) *lierc.IRCPrefix { return &lierc.IRCPrefix{Name: name} }

	tests := []struct {
		name      string
		m         *lierc.IRCMessage
		stored    []string
		privates  []string
		published []string
	}{
		{"channel message",
			&lierc.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi"}, Prefix: prefix("bob")},
			[]string{"c1 #chan PRIVMSG"}, nil, []string{"logged PRIVMSG 1"}},
		{"highlight",
			&lierc.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi leedo"}, Prefix: prefix("bob")},
			[]string{"c1 #chan PRIVMSG highlight"}, nil, []string{"logged PRIVMSG 1", "highlight PRIVMSG 1"}},
		{"private message",
			&lierc.IRCMessage{Command: "PRIVMSG", Params: []string{"me", "psst"}, Prefix: prefix("bob"), Direct: true},
			[]string{"c1 bob PRIVMSG"}, []string{"c1 bob"}, []string{"logged PRIVMSG 1", "highlight PRIVMSG 1"}},
		{"typing",
			&lierc.IRCMessage{Command: "TAGMSG", Params: []string{"#chan"}, Tags: map[string]string{"+typing": "active"}, Prefix: prefix("bob")},
			nil, nil, []string{"logged TAGMSG 0"}},
		{"nick (comes from multi)",
			&lierc.IRCMessage{Command: "NICK", Params: []string{"robert"}, Prefix: prefix("bob")},
			nil, nil, nil},
	}

	for _, tt := range tests {
		l, store, pub := newTestLogger()
		if err := l.handleChat(chat(t, tt.m)); err != nil {
			t.Errorf("%s: handleChat returned %v", tt.name, err)
		}
		if !equal(store.messages, tt.stored) || !equal(store.privates, tt.privates) || !equal(pub.published, tt.published) {
			t.Errorf("%s:\n  stored %q, want %q\n  privates %q, want %q\n  published %q, want %q",
				tt.name, store.messages, tt.stored, store.privates, tt.privates, pub.published, tt.published)
		}
	}
}

func TestHandleChatErrors(t *testing.T) {
	m := &lierc.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi"}, Prefix: &lierc.IRCPrefix{Name: "bob"}}

	// unreadable messages are dropped, not retried
	l, store, pub := newTestLogger()
	if err := l.handleChat(&nsq.Message{Body: []byte("{not json")}); err != nil {
		t.Errorf("unreadable message: returned %v, want nil", err)
	}
	if err := l.handleChat(&nsq.Message{Body: []byte(`{"Id":"c1"}`)}); err != nil {
		t.Errorf("message without Message: returned %v, want nil", err)
	}
	if len(store.messages)+len(pub.published) != 0 {
		t.Errorf("unreadable messages were stored or published")
	}

	// database errors are returned so NSQ retries, and nothing is published
	l, store, pub = newTestLogger()
	store.err = errors.New("database is down")
	if err := l.handleChat(chat(t, m)); err == nil {
		t.Errorf("database error: returned nil, want an error")
	}
	if len(pub.published) != 0 {
		t.Errorf("published %q after a database error", pub.published)
	}

	// a missing connection is still published, with id 0, as before
	l, store, pub = newTestLogger()
	store.err = ErrNoConnection
	if err := l.handleChat(chat(t, m)); err != nil {
		t.Errorf("missing connection: returned %v, want nil", err)
	}
	if !equal(pub.published, []string{"logged PRIVMSG 0"}) {
		t.Errorf("missing connection: published %q", pub.published)
	}

	// publish failures are logged, not retried (it's already stored)
	l, store, pub = newTestLogger()
	pub.err = errors.New("nsqd is down")
	if err := l.handleChat(chat(t, m)); err != nil {
		t.Errorf("publish error: returned %v, want nil", err)
	}
	if len(store.messages) != 1 {
		t.Errorf("publish error: stored %q", store.messages)
	}
}

func TestHandleMulti(t *testing.T) {
	l, store, pub := newTestLogger()
	body, _ := json.Marshal(lierc.IRCClientMultiMessage{
		Id:       "c1",
		Message:  &lierc.IRCMessage{Command: "QUIT", Params: []string{"bye"}, Prefix: &lierc.IRCPrefix{Name: "bob"}},
		Channels: []string{"#a", "#b"},
	})

	if err := l.handleMulti(&nsq.Message{Body: body}); err != nil {
		t.Fatalf("handleMulti returned %v", err)
	}
	if !equal(store.messages, []string{"c1 #a QUIT", "c1 #b QUIT"}) {
		t.Errorf("stored %q", store.messages)
	}
	if !equal(pub.published, []string{"logged QUIT 2"}) {
		t.Errorf("published %q, want the last id once", pub.published)
	}

	store.err = errors.New("database is down")
	if err := l.handleMulti(&nsq.Message{Body: body}); err == nil {
		t.Errorf("database error: returned nil, want an error")
	}
}
