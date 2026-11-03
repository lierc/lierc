package lierc

import (
	"encoding/json"
	"testing"
)

func TestParseIRCMessageJSON(t *testing.T) {
	tests := []struct{ line, want string }{
		{
			`:lee!leedo@blolol.irc PRIVMSG #c :hi there`,
			`{"Prefix":{"Name":"lee","User":"leedo","Server":"blolol.irc","Self":false},"Tags":{},"Command":"PRIVMSG","Raw":":lee!leedo@blolol.irc PRIVMSG #c :hi there","Time":0,"Params":["#c","hi there"],"Direct":false}`,
		},
		{
			// tag values are unescaped: a deliberate difference
			`@msgid=yzvb;+draft/react=thumbs\sup :n!u@h TAGMSG #c`,
			`{"Prefix":{"Name":"n","User":"u","Server":"h","Self":false},"Tags":{"+draft/react":"thumbs up","msgid":"yzvb"},"Command":"TAGMSG","Raw":"@msgid=yzvb;+draft/react=thumbs\\sup :n!u@h TAGMSG #c","Time":0,"Params":["#c"],"Direct":false}`,
		},
		{
			`:irc.example.com 001 me :Welcome`,
			`{"Prefix":{"Name":"irc.example.com","User":"","Server":"","Self":false},"Tags":{},"Command":"001","Raw":":irc.example.com 001 me :Welcome","Time":0,"Params":["me","Welcome"],"Direct":false}`,
		},
		{
			`PING :abc`,
			`{"Prefix":{"Name":"","User":"","Server":"","Self":false},"Tags":{},"Command":"PING","Raw":"PING :abc","Time":0,"Params":["abc"],"Direct":false}`,
		},
		{
			// trailing keeps its leading colon and inner ": "
			`:n!u@h PRIVMSG #c ::-) a: b`,
			`{"Prefix":{"Name":"n","User":"u","Server":"h","Self":false},"Tags":{},"Command":"PRIVMSG","Raw":":n!u@h PRIVMSG #c ::-) a: b","Time":0,"Params":["#c",":-) a: b"],"Direct":false}`,
		},
		{
			// runs of spaces collapse: a deliberate difference
			`:n!u@h   PRIVMSG  #c   :x`,
			`{"Prefix":{"Name":"n","User":"u","Server":"h","Self":false},"Tags":{},"Command":"PRIVMSG","Raw":":n!u@h   PRIVMSG  #c   :x","Time":0,"Params":["#c","x"],"Direct":false}`,
		},
		{
			// command is uppercased: a deliberate difference
			`:n!u@h privmsg #c :x`,
			`{"Prefix":{"Name":"n","User":"u","Server":"h","Self":false},"Tags":{},"Command":"PRIVMSG","Raw":":n!u@h privmsg #c :x","Time":0,"Params":["#c","x"],"Direct":false}`,
		},
		{
			// irc-go splits at the first "!": a deliberate difference
			`:AHHHH!ron!aaron@blolol.irc fart`,
			`{"Prefix":{"Name":"AHHHH","User":"ron!aaron","Server":"blolol.irc","Self":false},"Tags":{},"Command":"FART","Raw":":AHHHH!ron!aaron@blolol.irc fart","Time":0,"Params":null,"Direct":false}`,
		},
	}

	for _, test := range tests {
		m, err := ParseIRCMessage(test.line)
		if err != nil {
			t.Errorf("%q: %v", test.line, err)
			continue
		}
		if m.Raw != test.line {
			t.Errorf("Raw %q != %q", m.Raw, test.line)
		}
		if m.Time == 0 {
			t.Errorf("%q: Time not set", test.line)
		}
		m.Time = 0
		got, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != test.want {
			t.Errorf("%q:\n got %s\nwant %s", test.line, got, test.want)
		}
	}
}

func TestParseIRCMessageErrors(t *testing.T) {
	for _, line := range []string{"", "   "} {
		m, err := ParseIRCMessage(line)
		if err == nil || m != nil {
			t.Errorf("%q: got (%v, %v), want nil message and an error", line, m, err)
		}
	}
}

// The shapes manager.go builds for its own events must parse.
// ircmsg accepts ": CONNECT :Connected" (the source is empty), so the
// manager needs no special case for it.
func TestParseIRCMessageSynthetic(t *testing.T) {
	for _, line := range []string{
		": CONNECT :Connected",
		":me!@ PRIVMSG #c :hi",
		"DELETE c1",
		"CREATE me :net",
	} {
		m, err := ParseIRCMessage(line)
		if err != nil || m == nil {
			t.Errorf("%q: got (%v, %v)", line, m, err)
		}
	}
}
