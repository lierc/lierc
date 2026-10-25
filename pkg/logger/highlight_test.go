package logger

import (
	"regexp"
	"testing"

	"github.com/lierc/lierc/pkg/lierc"
)

func privmsg(text string, self bool) *lierc.IRCMessage {
	return &lierc.IRCMessage{
		Command: "PRIVMSG",
		Params:  []string{"#chan", text},
		Prefix:  &lierc.IRCPrefix{Name: "bob", Self: self},
	}
}

func TestHighlightMatch(t *testing.T) {
	patterns, errs := compileHighlights(map[string]string{
		"c1": `["leedo", "c++", " "]`,
		"c2": `[" ", ""]`,
		"c3": `not json`,
	})

	if len(errs) != 1 {
		t.Errorf("errs = %v, want one for c3", errs)
	}
	if _, ok := patterns["c2"]; ok {
		t.Errorf("c2 has only blank words, but got a pattern")
	}

	h := &Highlighters{}
	h.Replace(patterns)

	tests := []struct {
		connection string
		m          *lierc.IRCMessage
		want       bool
	}{
		{"c1", privmsg("hey LeeDo, look", false), true},
		{"c1", privmsg("leedos are great", false), false},
		{"c1", privmsg("leedo", true), false},
		{"c1", &lierc.IRCMessage{Command: "NOTICE", Params: []string{"#chan", "leedo"}, Prefix: &lierc.IRCPrefix{}}, false},
		{"c1", &lierc.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan"}, Prefix: &lierc.IRCPrefix{}}, false},
		{"c2", privmsg("leedo", false), false},
		{"unknown", privmsg("leedo", false), false},
	}

	for _, tt := range tests {
		if got := h.Match(tt.connection, tt.m); got != tt.want {
			t.Errorf("Match(%s, %q, self=%v) = %v, want %v", tt.connection, tt.m.Params, tt.m.Prefix.Self, got, tt.want)
		}
	}
}

func TestHighlightReplaceDropsRemoved(t *testing.T) {
	h := &Highlighters{}
	h.Replace(map[string]*regexp.Regexp{"c1": regexp.MustCompile("leedo")})
	h.Replace(map[string]*regexp.Regexp{})

	if h.Match("c1", privmsg("leedo", false)) {
		t.Errorf("highlights removed from c1 still match")
	}
}
