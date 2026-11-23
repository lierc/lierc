package liercd

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lierc/lierc/pkg/ircwire"
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

// checkRaw lets ordinary lines through, at IRC's limits, and refuses
// ones a server would cut short or reject, and liercd's own commands.
func TestCheckRaw(t *testing.T) {
	x := func(n int) string { return strings.Repeat("x", n) }
	// a PRIVMSG of n bytes, and a TAGMSG with n bytes of tag data
	body := func(n int) string { return "PRIVMSG #a :" + x(n-len("PRIVMSG #a :")) }
	tags := func(n int) string { return "@+x=" + x(n-len("+x=")) + " TAGMSG #a" }
	for _, tc := range []struct {
		name, line string
		ok         bool
	}{
		{"message", "PRIVMSG #a :hi", true},
		{"reaction", "@+reply=abc;+draft/react=👍 TAGMSG #a", true},
		{"lowercase", "join #a", true},
		{"prefix", ":me!u@h PRIVMSG #a :hi", true},
		{"utf-8", "PRIVMSG #a :héllo 😀", true},
		{"QUIT", "QUIT :bye", true},
		{"510 bytes", body(510), true},
		{"511 bytes", body(511), false},
		{"510 bytes of é", "PRIVMSG #a :" + strings.Repeat("é", (510-12)/2), true},
		{"over 510 bytes of é", "PRIVMSG #a :" + strings.Repeat("é", 250), false},
		{"tags beside a 510-byte body", "@+x=" + x(1000) + " " + body(510), true},
		{"4094 bytes of tags", tags(4094), true},
		{"4095 bytes of tags", tags(4095), false},
		{"NUL", "PRIVMSG #a :a\x00b", false},
		{"empty", "", false},
		{"spaces", "   ", false},
		{"tags only", "@+x=y", false},
		{"tags, no command", "@+x=y ", false},
		{"prefix only", ":me!u@h", false},
		{"non-ASCII command", "PRÏVMSG #a :hi", false},
		{"CAP", "CAP REQ :-message-tags", false},
		{"lowercase cap", "cap end", false},
		{"tagged CAP", "@+x=y CAP LS", false},
		{"AUTHENTICATE", "AUTHENTICATE PLAIN", false},
		{"PASS", "PASS secret", false},
		{"USER", "USER u 0 * :real", false},
	} {
		if err := checkRaw(tc.line); (err == nil) != tc.ok {
			t.Errorf("%s: %.40q: %v, want ok %v", tc.name, tc.line, err, tc.ok)
		}
	}
}

// A line checkRaw refuses gets a 400 with its reason, whether or not the
// connection is set up.
func TestRawRefusesInvalid(t *testing.T) {
	m := NewClientManager(discard{})
	c := &lierc.IRCClient{Id: "c1", Config: &lierc.IRCConfig{Nick: "me"}}
	m.AddClient(c)
	for _, registered := range []bool{false, true} {
		c.Lock()
		c.Registered = registered
		c.Unlock()
		for line, reason := range map[string]string{
			"PRIVMSG #a :a\x00b":                      "doesn't allow",
			"PRIVMSG #a :" + strings.Repeat("x", 600): "too long",
			"CAP REQ :-message-tags":                  "relaychat handles it",
			"@+x=y":                                   "isn't a valid IRC line",
		} {
			w := httptest.NewRecorder()
			m.HandleCommand(w, httptest.NewRequest("POST", "/c1/raw", strings.NewReader(line)))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), reason) {
				t.Errorf("registered %v, %.30q: %d %q, want 400 with %q", registered, line, w.Code, w.Body.String(), reason)
			}
		}
	}
}

// discard is a Queue that drops everything.
type discard struct{}

func (discard) Enqueue(string, interface{}) {}

// recorded is a Queue that keeps what it was given, in order.
type recorded struct {
	topics []string
	items  []interface{}
}

func (r *recorded) Enqueue(topic string, item interface{}) {
	r.topics = append(r.topics, topic)
	r.items = append(r.items, item)
}

// The manager, as its clients' Sink, queues each kind on its topic, in
// the order its clients publish: a NICK or QUIT, with its channels, goes
// on chats like any other line, and a status goes on chats as a 000 line,
// its state as the Status: nothing goes on the status or config topics.
func TestManagerSinkTopics(t *testing.T) {
	q := &recorded{}
	m := NewClientManager(q)
	quit := &ircwire.IRCClientMessage{Id: "c1", Message: &ircwire.IRCMessage{Command: "QUIT"}, Channels: []string{"#a"}}
	msg := &ircwire.IRCClientMessage{Id: "c1", Message: &ircwire.IRCMessage{Command: "PRIVMSG"}}
	m.Message(quit)
	m.Message(msg)
	m.Status(&lierc.IRCClientStatus{Id: "c1", Host: "h", Connected: true, Message: "Connected"})
	want := []string{"chats", "chats", "chats"}
	if !reflect.DeepEqual(q.topics, want) {
		t.Fatalf("topics %q, want %q", q.topics, want)
	}
	if q.items[0] != quit || q.items[1] != msg {
		t.Errorf("chats order %v, want the QUIT then the PRIVMSG", q.items[:2])
	}
	line, ok := q.items[2].(*ircwire.IRCClientMessage)
	if !ok || line.Message.Command != "000" || line.Channels != nil || line.Status == nil || !line.Status.Connected {
		t.Errorf("status line %+v, want a 000 line, Connected, without Channels", q.items[2])
	}
}
