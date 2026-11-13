package logger

import (
	"encoding/json"
	"testing"

	"github.com/lierc/lierc/pkg/lierc"
	"github.com/nsqio/go-nsq"
)

func counts() map[string]uint64 {
	c := map[string]uint64{"highlights": highlightsTotal.Get()}
	for k, v := range messagesTotal {
		c[k] = v.Get()
	}
	return c
}

// Every chats message counts under its kind; highlights count separately;
// multi messages (NICK, QUIT) count as multi.
func TestHandleCountsMessages(t *testing.T) {
	bob := &lierc.IRCPrefix{Name: "bob"}
	for _, tt := range []struct {
		m          *lierc.IRCMessage
		kind       string
		highlights uint64
	}{
		{&lierc.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi"}, Prefix: bob}, "target", 0},
		{&lierc.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi leedo"}, Prefix: bob}, "target", 1},
		{&lierc.IRCMessage{Command: "NOTICE", Params: []string{"*", "server notice"}, Prefix: bob}, "status", 0},
		{&lierc.IRCMessage{Command: "TAGMSG", Params: []string{"#chan"}, Tags: map[string]string{"+typing": "active"}, Prefix: bob}, "pass", 0},
		{&lierc.IRCMessage{Command: "NICK", Params: []string{"robert"}, Prefix: bob}, "skip", 0},
	} {
		l, _, _ := newTestLogger()
		before := counts()
		if err := l.handleChat(chat(t, tt.m)); err != nil {
			t.Fatal(err)
		}
		after := counts()
		for k := range after {
			want := before[k]
			switch k {
			case tt.kind:
				want++
			case "highlights":
				want += tt.highlights
			}
			if after[k] != want {
				t.Errorf("%s: %s = %d, want %d", tt.m.Command, k, after[k], want)
			}
		}
	}

	l, _, _ := newTestLogger()
	before := counts()
	body, _ := json.Marshal(lierc.IRCClientMessage{Id: "c1", Message: &lierc.IRCMessage{Command: "QUIT", Params: []string{"bye"}, Prefix: bob}, Channels: []string{"#a", "#b"}})
	if err := l.handleMulti(&nsq.Message{Body: body}); err != nil {
		t.Fatal(err)
	}
	if got := counts()["multi"]; got != before["multi"]+1 {
		t.Errorf("multi = %d, want %d", got, before["multi"]+1)
	}

	// the same message on chats counts as multi, and as nothing else
	before = counts()
	if err := l.handleChat(&nsq.Message{Body: body}); err != nil {
		t.Fatal(err)
	}
	after := counts()
	for k := range after {
		want := before[k]
		if k == "multi" {
			want++
		}
		if after[k] != want {
			t.Errorf("multi on chats: %s = %d, want %d", k, after[k], want)
		}
	}
}
