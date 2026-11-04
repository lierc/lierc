package liercd

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lierc/lierc/pkg/lierc"
)

func TestRequestedNick(t *testing.T) {
	for line, want := range map[string]string{
		"NICK new":           "new",
		"nick new":           "new",
		"NICK :new":          "new",
		"NICK  new ":         "new",
		"@x=y NICK new":      "new",
		":me!u@h NICK new":   "new",
		"NICK":               "",
		"PRIVMSG #a :NICK x": "",
		"NICKX y":            "",
		"":                   "",
	} {
		if got, ok := requestedNick(line); got != want || ok != (want != "") {
			t.Errorf("%q: %q %v, want %q", line, got, ok, want)
		}
	}
}

// Short raw lines used to panic slicing line[:7].
func TestIsMessage(t *testing.T) {
	for line, want := range map[string]bool{
		"PRIVMSG #a :hi": true,
		"privmsg #a :hi": true,
		"NOTICE #a :hi":  true,
		"NICK a":         false,
		"JOIN":           false,
		"":               false,
		"PRIVMSGX":       false,
		// tags and a prefix come before the command
		"@+draft/reply=x PRIVMSG #a :hi": true,
		":me!u@h NOTICE #a :hi":          true,
		"@+typing=active TAGMSG #a":      false,
	} {
		if got := isMessage(line); got != want {
			t.Errorf("%q: %v, want %v", line, got, want)
		}
	}
}

// Only a message that was queued is echoed, and only when the server
// won't echo it: one dropped while disconnected was never sent.
func TestShouldEcho(t *testing.T) {
	for _, tc := range []struct {
		line              string
		queued, echoAcked bool
		want              bool
	}{
		{"PRIVMSG #a :hi", true, false, true},
		{"PRIVMSG #a :hi", false, false, false},
		{"PRIVMSG #a :hi", true, true, false},
		{"PRIVMSG #a :hi", false, true, false},
		{"NOTICE #a :hi", true, false, true},
		{"JOIN #a", true, false, false},
	} {
		if got := shouldEcho(tc.line, tc.queued, tc.echoAcked); got != tc.want {
			t.Errorf("%q queued %v acked %v: %v, want %v", tc.line, tc.queued, tc.echoAcked, got, tc.want)
		}
	}
}

// Before the connection is set up, the raw action refuses every line with
// 409 and says why, instead of sending, dropping or echoing it.
func TestRawRefusedUntilRegistered(t *testing.T) {
	m := NewClientManager(discard{})
	c := &lierc.IRCClient{Id: "c1", Config: &lierc.IRCConfig{Nick: "me"}}
	m.AddClient(c)

	for _, tc := range []struct {
		line string
		code int
	}{
		{"PRIVMSG #a :hi", http.StatusConflict},
		{"NICK new", http.StatusConflict},
		{"@+typing=active TAGMSG #a", http.StatusConflict},
		{"JOIN #a", http.StatusConflict},
		{"MODE #a", http.StatusConflict},
	} {
		w := httptest.NewRecorder()
		m.HandleCommand(w, httptest.NewRequest("POST", "/c1/raw", strings.NewReader(tc.line)))
		if w.Code != tc.code {
			t.Errorf("%q: status %d, want %d", tc.line, w.Code, tc.code)
		}
		if tc.code == http.StatusConflict && !strings.Contains(w.Body.String(), notRegistered) {
			t.Errorf("%q: body %q", tc.line, w.Body.String())
		}
	}
	// once it's set up they're let through
	c.Lock()
	c.Registered = true
	c.Unlock()
	w := httptest.NewRecorder()
	m.HandleCommand(w, httptest.NewRequest("POST", "/c1/raw", strings.NewReader("JOIN #a")))
	if w.Code != http.StatusOK {
		t.Errorf("registered: status %d, want 200", w.Code)
	}
}

func TestRawLine(t *testing.T) {
	for _, tc := range []struct {
		body, line string
		ok         bool
	}{
		{"PRIVMSG #a :hi", "PRIVMSG #a :hi", true},
		{"PRIVMSG #a :hi\r\n", "PRIVMSG #a :hi", true},
		{"PRIVMSG #a :hi\n", "PRIVMSG #a :hi", true},
		{"PRIVMSG #a :hi\r\nJOIN #b", "", false},
		{"PRIVMSG #a :hi\nJOIN #b", "", false},
		{"PRIVMSG #a :hi\rJOIN #b", "", false},
	} {
		line, ok := rawLine([]byte(tc.body))
		if ok != tc.ok || (ok && line != tc.line) {
			t.Errorf("rawLine(%q) = %q, %v; want %q, %v", tc.body, line, ok, tc.line, tc.ok)
		}
	}
}

// A raw body with a line break inside is refused with 400, whatever the
// connection's state: it would go out as more than one line, and the
// lines after the first would skip the checks.
func TestRawLineBreakRefused(t *testing.T) {
	m := NewClientManager(discard{})
	c := &lierc.IRCClient{Id: "c1", Config: &lierc.IRCConfig{Nick: "me"}}
	m.AddClient(c)

	w := httptest.NewRecorder()
	m.HandleCommand(w, httptest.NewRequest("POST", "/c1/raw", strings.NewReader("JOIN #a\r\nPRIVMSG #a :hi")))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
	// a trailing line ending is just dropped: this one goes on to the
	// registration check
	w = httptest.NewRecorder()
	m.HandleCommand(w, httptest.NewRequest("POST", "/c1/raw", strings.NewReader("JOIN #a\r\n")))
	if w.Code != http.StatusConflict {
		t.Errorf("trailing line ending: status %d, want 409", w.Code)
	}
}

// discard is a Queue that drops everything.
type discard struct{}

func (discard) Enqueue(string, interface{}) {}

// recorded is a Queue that keeps the topics it was given, in order.
type recorded struct{ topics []string }

func (r *recorded) Enqueue(topic string, _ interface{}) { r.topics = append(r.topics, topic) }

// The manager, as its clients' Sink, queues each kind on its topic; a
// status goes on chats as a CONNECT or DISCONNECT.
func TestManagerSinkTopics(t *testing.T) {
	q := &recorded{}
	m := NewClientManager(q)
	m.Message(&lierc.IRCClientMessage{Id: "c1"})
	m.Multi(&lierc.IRCClientMultiMessage{Id: "c1"})
	m.Status(&lierc.IRCClientStatus{Id: "c1", Host: "h", Connected: true, Message: "Connected"})
	m.ConfigChange(&lierc.IRCConfigChange{Id: "c1"})
	want := []string{"chats", "multi", "chats", "config"}
	if !reflect.DeepEqual(q.topics, want) {
		t.Errorf("topics %q, want %q", q.topics, want)
	}
}
