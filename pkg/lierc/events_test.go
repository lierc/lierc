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
				c.Caps, c.CapsAcked = map[string]string{}, map[string]struct{}{}
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

// A malformed PREFIX must neither panic nor replace the one in use.
func TestIsupportPrefix(t *testing.T) {
	c := configClient()
	c.prefix = defaultPrefix
	m, _ := ParseIRCMessage(":s 005 me PREFIX=(ov)@ :are supported")
	c.Message(m)
	if !reflect.DeepEqual(c.prefix, defaultPrefix) {
		t.Errorf("malformed PREFIX replaced prefix: %q", c.prefix)
	}
	m, _ = ParseIRCMessage(":s 005 me PREFIX=(qov)~@+ :are supported")
	c.Message(m)
	if want := [][]byte{{'~', 'q'}, {'@', 'o'}, {'+', 'v'}}; !reflect.DeepEqual(c.prefix, want) {
		t.Errorf("prefix %q, want %q", c.prefix, want)
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
