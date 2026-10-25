package logger

import (
	"testing"

	"github.com/lierc/lierc/pkg/lierc"
)

func TestLogType(t *testing.T) {
	tests := []struct {
		name    string
		command string
		tags    map[string]string
		want    string
	}{
		{"privmsg", "PRIVMSG", nil, "#"},
		{"reaction", "TAGMSG", map[string]string{"+draft/reply": "m1", "+draft/react": "👍"}, "#"},
		{"unreaction", "TAGMSG", map[string]string{"+draft/reply": "m1", "+draft/unreact": "👍"}, "#"},
		{"typing", "TAGMSG", map[string]string{"+typing": "active"}, "pass"},
		{"draft typing", "TAGMSG", map[string]string{"+draft/typing": "paused"}, "pass"},
		{"no tags", "TAGMSG", nil, "pass"},
		{"nick", "NICK", nil, "skip"},
		{"motd", "372", nil, "status"},
		{"unknown", "PONG", nil, "pass"},
	}

	for _, tt := range tests {
		m := &lierc.IRCMessage{Command: tt.command, Tags: tt.tags, Params: []string{"#chan"}}
		if got := logType(m); got != tt.want {
			t.Errorf("%s: logType = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestRouteChat(t *testing.T) {
	msg := func(command, from string, direct, self bool, params ...string) *lierc.IRCMessage {
		return &lierc.IRCMessage{
			Command: command,
			Params:  params,
			Direct:  direct,
			Prefix:  &lierc.IRCPrefix{Name: from, Self: self},
		}
	}

	tests := []struct {
		name string
		m    *lierc.IRCMessage
		want route
	}{
		{"channel message", msg("PRIVMSG", "bob", false, false, "#Chan", "hi"), route{actionStore, "#Chan", false}},
		{"private message to me", msg("PRIVMSG", "bob", true, false, "me", "hi"), route{actionStore, "bob", true}},
		{"my own private message", msg("PRIVMSG", "me", false, true, "bob", "hi"), route{actionStore, "bob", false}},
		{"private notice", msg("NOTICE", "NickServ", true, false, "me", "hello"), route{actionStore, "NickServ", true}},
		{"server notice", msg("NOTICE", "server", false, false, "*", "hello"), route{actionStore, "status", false}},
		{"motd", msg("372", "server", false, false, "me", "welcome"), route{actionStore, "status", false}},
		{"error numeric", msg("433", "server", false, false, "me", "nick", "in use"), route{actionStore, "status", false}},
		{"join", msg("JOIN", "bob", false, false, "#chan"), route{actionStore, "#chan", false}},
		{"nick", msg("NICK", "bob", false, false, "robert"), route{action: actionSkip}},
		{"typing", &lierc.IRCMessage{Command: "TAGMSG", Params: []string{"#chan"}, Tags: map[string]string{"+typing": "active"}, Prefix: &lierc.IRCPrefix{Name: "bob"}}, route{action: actionPass}},
		{"unknown", msg("PONG", "server", false, false, "x"), route{action: actionPass}},
	}

	for _, tt := range tests {
		if got := routeChat(tt.m); got != tt.want {
			t.Errorf("%s: routeChat = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}
