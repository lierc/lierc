package lierc

import (
	"reflect"
	"sort"
	"testing"
)

func copyChannels(in map[string]*IRCChannel) map[string]*IRCChannel {
	out := map[string]*IRCChannel{}
	for k, v := range in {
		ch := *v
		topic := *v.Topic
		ch.Topic = &topic
		ch.Nicks = map[string][]byte{}
		for n, modes := range v.Nicks {
			ch.Nicks[n] = append([]byte{}, modes...)
		}
		ch.Mode = append([]byte(nil), v.Mode...)
		out[k] = &ch
	}
	return out
}

// No handler may panic on malformed parameters, whatever the prefix; with
// none at all, it must leave the channels alone.
func TestHandlersShortParams(t *testing.T) {
	var cmds []string
	for cmd := range handlers {
		cmds = append(cmds, cmd)
	}
	sort.Strings(cmds)
	for _, cmd := range []string{"PRIVMSG", "NOTICE"} {
		if _, ok := handlers[cmd]; !ok {
			cmds = append(cmds, cmd)
		}
	}

	paramSets := [][]string{
		{}, {"#a"}, {"#a", "x"},
		{"*", "LS"}, {"*", "ACK"}, {"*", "NAK"}, {"*", "NEW"}, {"*", "DEL"},
		{"*", "LS", "*"}, {"*", "LS", "a b"}, {"*", "ACK", "a"}, {"*", "NAK", "a"},
		{"#a", ""}, {"#a", "+"}, {"#a", "+o"}, {"#a", "+o", "bob"}, {"#a", "", ""},
		{"me", "#a", ""}, {"me", "#a", "+"}, {"me", "#a", "x"}, {"me", "#a", "bob", ""},
		{"me", "#a", ""}, {"me", "=", "#a", ""}, {"me", "=", "#a", "@bob +me"},
		{"me", "#a", "bob", "notanumber"},
		{"", ""}, {"", "", "", ""},
	}
	prefixes := map[string]IRCPrefix{
		"full":  {Name: "me", User: "u", Server: "h"},
		"empty": {},
	}
	for _, cmd := range cmds {
		for pname, prefix := range prefixes {
			for _, params := range paramSets {
				c := configClient()
				before := copyChannels(c.Channels)
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("%s with %s prefix, params %q: panic: %v", cmd, pname, params, r)
						}
					}()
					p := prefix
					c.Message(&IRCMessage{Command: cmd, Prefix: &p, Params: params})
				}()
				// QUIT takes no params and removes its sender from every channel
				if len(params) == 0 && cmd != "QUIT" && !reflect.DeepEqual(c.Channels, before) {
					t.Errorf("%s with %s prefix, 0 params changed Channels", cmd, pname)
				}
			}
		}
	}
}

// PREFIX comes from the ISUPPORT map; a missing or malformed one means
// the default.
func TestSetPrefix(t *testing.T) {
	c := configClient()
	for _, tc := range []struct {
		isupport map[string]string
		want     [][]byte
	}{
		{nil, defaultPrefix},
		{map[string]string{"NETWORK": "x"}, defaultPrefix},
		{map[string]string{"PREFIX": "(ov)@"}, defaultPrefix},
		{map[string]string{"PREFIX": ""}, defaultPrefix},
		{map[string]string{"PREFIX": "(qov)~@+"}, [][]byte{{'~', 'q'}, {'@', 'o'}, {'+', 'v'}}},
	} {
		c.prefix = [][]byte{{'!', 'z'}}
		c.setPrefix(tc.isupport)
		if !reflect.DeepEqual(c.prefix, tc.want) {
			t.Errorf("%v: prefix %q, want %q", tc.isupport, c.prefix, tc.want)
		}
	}
}

// The end of registration joins the config's channels once, and with no
// connection uses the default prefix.
func TestEndOfRegistration(t *testing.T) {
	c := configClient()
	c.prefix = [][]byte{{'!', 'z'}}
	m, _ := ParseIRCMessage(":s 376 me :End of MOTD")
	c.Message(m)
	if !c.joined || !reflect.DeepEqual(c.prefix, defaultPrefix) {
		t.Errorf("joined %v prefix %q", c.joined, c.prefix)
	}
	c.prefix = [][]byte{{'!', 'z'}}
	m, _ = ParseIRCMessage(":s 422 me :No MOTD")
	c.Message(m)
	if !reflect.DeepEqual(c.prefix, [][]byte{{'!', 'z'}}) {
		t.Error("a second end of registration ran again")
	}
}

func TestMessageEffects(t *testing.T) {
	parse := func(line string) *IRCMessage {
		m, err := ParseIRCMessage(line)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	c := configClient()
	c.User = "u"

	m := parse(":bob!u@h PRIVMSG me :hi")
	c.Message(m)
	if !m.Direct || m.Prefix.Self {
		t.Errorf("PRIVMSG to us: Direct %v Self %v", m.Direct, m.Prefix.Self)
	}
	m = parse(":bob!u@h PRIVMSG #a :hi")
	c.Message(m)
	if m.Direct {
		t.Error("PRIVMSG to a channel is Direct")
	}
	m = parse(":me!u@h PRIVMSG #a :hi")
	c.Message(m)
	if !m.Prefix.Self {
		t.Error("own PRIVMSG not Self")
	}

	m = parse(":bob!u@h NOTICE me :hi")
	c.Message(m)
	if !m.Direct {
		t.Error("NOTICE to us not Direct")
	}
	m = parse(":me!u@h NOTICE #a :hi")
	c.Message(m)
	if m.Prefix.Self {
		t.Error("own NOTICE is Self")
	}

	// a line with no prefix is never Self, even for a client with no nick
	blank := configClient()
	blank.Nick, blank.User = "", ""
	m = parse("PRIVMSG #a :hi")
	blank.Message(m)
	if m.Prefix.Self {
		t.Error("prefix-less PRIVMSG is Self")
	}

	e := c.Message(parse(":bob!u@h QUIT :bye"))
	if e.multi == nil || e.multi.Id != "c1" || e.multi.Message.Command != "QUIT" {
		t.Fatalf("QUIT: multi %v", e.multi)
	}
	got := append([]string{}, e.multi.Channels...)
	sort.Strings(got)
	if want := []string{"#a", "#key"}; !reflect.DeepEqual(got, want) {
		t.Errorf("QUIT channels %q, want %q", got, want)
	}

	c = configClient()
	delete(c.Channels["#key"].Nicks, "bob")
	e = c.Message(parse(":bob!u@h NICK robert"))
	if e.multi == nil || !reflect.DeepEqual(e.multi.Channels, []string{"#a"}) {
		t.Errorf("NICK: multi %+v", e.multi)
	}
	if e := c.Message(parse(":bob!u@h PRIVMSG #a :hi")); e.multi != nil || e.changes != nil {
		t.Error("PRIVMSG has effects")
	}
}
