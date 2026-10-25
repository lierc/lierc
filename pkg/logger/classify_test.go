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
		want    logKind
	}{
		{"privmsg", "PRIVMSG", nil, logTarget},
		{"reaction", "TAGMSG", map[string]string{"+draft/reply": "m1", "+draft/react": "👍"}, logTarget},
		{"unreaction", "TAGMSG", map[string]string{"+draft/reply": "m1", "+draft/unreact": "👍"}, logTarget},
		{"typing", "TAGMSG", map[string]string{"+typing": "active"}, logPass},
		{"draft typing", "TAGMSG", map[string]string{"+draft/typing": "paused"}, logPass},
		{"no tags", "TAGMSG", nil, logPass},
		{"nick", "NICK", nil, logSkip},
		{"motd", "372", nil, logStatus},
		{"unknown", "PONG", nil, logPass},
	}

	for _, tt := range tests {
		m := &lierc.IRCMessage{Command: tt.command, Tags: tt.tags, Params: []string{"#chan"}}
		if got := logType(m); got != tt.want {
			t.Errorf("%s: logType = %v, want %v", tt.name, got, tt.want)
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
		{"channel message", msg("PRIVMSG", "bob", false, false, "#Chan", "hi"), route{logTarget, "#Chan", false}},
		{"private message to me", msg("PRIVMSG", "bob", true, false, "me", "hi"), route{logTarget, "bob", true}},
		{"my own private message", msg("PRIVMSG", "me", false, true, "bob", "hi"), route{logTarget, "bob", false}},
		{"private notice", msg("NOTICE", "NickServ", true, false, "me", "hello"), route{logTarget, "NickServ", true}},
		{"server notice", msg("NOTICE", "server", false, false, "*", "hello"), route{logStatus, "status", false}},
		{"motd", msg("372", "server", false, false, "me", "welcome"), route{logStatus, "status", false}},
		{"error numeric", msg("433", "server", false, false, "me", "nick", "in use"), route{logStatus, "status", false}},
		{"join", msg("JOIN", "bob", false, false, "#chan"), route{logTarget, "#chan", false}},
		{"nick", msg("NICK", "bob", false, false, "robert"), route{kind: logSkip}},
		{"typing", &lierc.IRCMessage{Command: "TAGMSG", Params: []string{"#chan"}, Tags: map[string]string{"+typing": "active"}, Prefix: &lierc.IRCPrefix{Name: "bob"}}, route{kind: logPass}},
		{"unknown", msg("PONG", "server", false, false, "x"), route{kind: logPass}},
	}

	for _, tt := range tests {
		if got := routeChat(tt.m); got != tt.want {
			t.Errorf("%s: routeChat = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}
