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
