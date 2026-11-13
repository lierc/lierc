package lierc

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ergochat/irc-go/ircmsg"
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
	eachHandler(func(cmd string, h handler) { cmds = append(cmds, cmd) })

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
	prefixes := map[string]string{
		"full":  "me!u@h",
		"empty": "",
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
					handleMessage(c, ircmsg.Message{Source: prefix, Command: cmd, Params: params})
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
func TestPrefixesFrom(t *testing.T) {
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
		if got := prefixesFrom(tc.isupport); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v: prefix %q, want %q", tc.isupport, got, tc.want)
		}
	}
}

func TestMessageEffects(t *testing.T) {
	// parse runs line through annotate, as the raw callback does
	parse := func(c *IRCClient, line string) *IRCMessage {
		m, err := ParseIRCMessage(line)
		if err != nil {
			t.Fatal(err)
		}
		c.annotate(m)
		return m
	}

	c := configClient()
	c.User = "u"

	m := parse(c, ":bob!u@h PRIVMSG me :hi")
	if !m.Direct || m.Prefix.Self {
		t.Errorf("PRIVMSG to us: Direct %v Self %v", m.Direct, m.Prefix.Self)
	}
	m = parse(c, ":bob!u@h PRIVMSG #a :hi")
	if m.Direct {
		t.Error("PRIVMSG to a channel is Direct")
	}
	m = parse(c, ":me!u@h PRIVMSG #a :hi")
	if !m.Prefix.Self {
		t.Error("own PRIVMSG not Self")
	}

	m = parse(c, ":bob!u@h NOTICE me :hi")
	if !m.Direct {
		t.Error("NOTICE to us not Direct")
	}
	m = parse(c, ":me!u@h NOTICE #a :hi")
	if m.Prefix.Self {
		t.Error("own NOTICE is Self")
	}

	// a line with no prefix is never Self, even for a client with no nick
	blank := configClient()
	blank.Config.Nick, blank.User = "", ""
	m = parse(blank, "PRIVMSG #a :hi")
	if m.Prefix.Self {
		t.Error("prefix-less PRIVMSG is Self")
	}

	e := handleLine(t, c, ":bob!u@h QUIT :bye")
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
	e = handleLine(t, c, ":bob!u@h NICK robert")
	if e.multi == nil || !reflect.DeepEqual(e.multi.Channels, []string{"#a"}) {
		t.Errorf("NICK: multi %+v", e.multi)
	}
	if e := handleLine(t, c, ":bob!u@h PRIVMSG #a :hi"); e.multi != nil || e.changes != nil {
		t.Error("PRIVMSG has effects")
	}
}

// A QUIT from a nick in none of our channels is still a multi message:
// Channels is present but empty, so the logger publishes it unstored.
func TestQuitNoChannelsIsMulti(t *testing.T) {
	c := configClient()
	e := handleLine(t, c, ":nobody!u@h QUIT :bye")
	if e.multi == nil || e.multi.Channels == nil || len(e.multi.Channels) != 0 {
		t.Fatalf("multi %+v, want empty non-nil Channels", e.multi)
	}
	body, err := json.Marshal(e.multi)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"Channels":[]`) {
		t.Errorf("marshalled %s, want \"Channels\":[]", body)
	}
}
