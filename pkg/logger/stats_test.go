package logger

import (
	"encoding/json"
	"testing"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/lierc/lierc/pkg/ircwire"
	"github.com/nsqio/go-nsq"
)

// commandCount is logger_messages_total for one command label.
func commandCount(label string) uint64 {
	return vm.GetOrCreateCounter(`logger_messages_total{command="` + label + `"}`).Get()
}

func TestCommandLabel(t *testing.T) {
	for in, want := range map[string]string{
		"PRIVMSG": "PRIVMSG", "privmsg": "PRIVMSG", "TAGMSG": "TAGMSG", "QUIT": "QUIT",
		"000": "0xx", "001": "0xx", "372": "3xx", "433": "4xx", "903": "9xx",
		"MADEUP": "other", "": "other", "12": "other", "1234": "other", "4x3": "other",
		`PRIV"MSG`: "other",
	} {
		if got := commandLabel(in); got != want {
			t.Errorf("commandLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every chats message counts under its command label; stored ones also
// count as stored; highlights count separately. A NICK or QUIT with its
// channels counts once, under its command, and as stored.
func TestHandleCountsMessages(t *testing.T) {
	bob := &ircwire.IRCPrefix{Name: "bob"}
	for _, tt := range []struct {
		m          *ircwire.IRCMessage
		label      string
		stored     uint64
		highlights uint64
	}{
		{&ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi"}, Prefix: bob}, "PRIVMSG", 1, 0},
		{&ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi leedo"}, Prefix: bob}, "PRIVMSG", 1, 1},
		{&ircwire.IRCMessage{Command: "NOTICE", Params: []string{"*", "server notice"}, Prefix: bob}, "NOTICE", 1, 0},
		{&ircwire.IRCMessage{Command: "372", Params: []string{"me", "motd line"}, Prefix: bob}, "3xx", 1, 0},
		{&ircwire.IRCMessage{Command: "TAGMSG", Params: []string{"#chan"}, Tags: map[string]string{"+typing": "active"}, Prefix: bob}, "TAGMSG", 0, 0},
		{&ircwire.IRCMessage{Command: "NICK", Params: []string{"robert"}, Prefix: bob}, "NICK", 0, 0},
		{&ircwire.IRCMessage{Command: "MADEUP", Params: []string{"x"}, Prefix: bob}, "other", 0, 0},
	} {
		l, _, _ := newTestLogger()
		cmd, stored, hl := commandCount(tt.label), storedTotal.Get(), highlightsTotal.Get()
		if err := l.handleChat(chat(t, tt.m)); err != nil {
			t.Fatal(err)
		}
		if got := commandCount(tt.label) - cmd; got != 1 {
			t.Errorf("%s: command %s counted %d, want 1", tt.m.Command, tt.label, got)
		}
		if got := storedTotal.Get() - stored; got != tt.stored {
			t.Errorf("%s: stored counted %d, want %d", tt.m.Command, got, tt.stored)
		}
		if got := highlightsTotal.Get() - hl; got != tt.highlights {
			t.Errorf("%s: highlights counted %d, want %d", tt.m.Command, got, tt.highlights)
		}
	}

	l, _, _ := newTestLogger()
	cmd, stored := commandCount("QUIT"), storedTotal.Get()
	body, _ := json.Marshal(ircwire.IRCClientMessage{Id: "c1", Message: &ircwire.IRCMessage{Command: "QUIT", Params: []string{"bye"}, Prefix: bob}, Channels: []string{"#a", "#b"}})
	if err := l.handleChat(&nsq.Message{Body: body}); err != nil {
		t.Fatal(err)
	}
	if got := commandCount("QUIT") - cmd; got != 1 {
		t.Errorf("QUIT with channels: counted %d, want 1", got)
	}
	if got := storedTotal.Get() - stored; got != 1 {
		t.Errorf("QUIT with channels: stored counted %d, want 1", got)
	}
}
