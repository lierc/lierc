package main

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
