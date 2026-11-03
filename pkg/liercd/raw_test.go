package liercd

import "testing"

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
