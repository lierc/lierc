package liercd

import (
	"net/http"
	"net/http/httptest"
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

// Messages and nick changes wait for the connection to be set up; typing
// notifications and other lines don't.
func TestNeedsRegistration(t *testing.T) {
	for line, want := range map[string]bool{
		"PRIVMSG #a :hi":                 true,
		"@+draft/reply=x PRIVMSG #a :hi": true,
		"notice bob :hi":                 true,
		"NICK new":                       true,
		"@+typing=active TAGMSG #a":      false,
		"JOIN #a":                        false,
		"PART #a":                        false,
		"MODE #a":                        false,
		"":                               false,
	} {
		if got := needsRegistration(line); got != want {
			t.Errorf("%q: %v, want %v", line, got, want)
		}
	}
}

// Before the connection is set up, the raw action refuses a message with
// 409 and says why, instead of dropping or echoing it. Other lines go on
// to the client (which drops them while it isn't connected).
func TestRawRefusedUntilRegistered(t *testing.T) {
	m := NewClientManager()
	c := &lierc.IRCClient{Id: "c1", Config: &lierc.IRCConfig{Nick: "me"}}
	m.AddClient(c)

	for _, tc := range []struct {
		line string
		code int
	}{
		{"PRIVMSG #a :hi", http.StatusConflict},
		{"NICK new", http.StatusConflict},
		{"@+typing=active TAGMSG #a", http.StatusOK},
		{"JOIN #a", http.StatusOK},
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
}
