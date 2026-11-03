package lierc

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ergochat/irc-go/ircmsg"
)

// a client in #a and #key (joined with a key) as "me", with no connection
// or sender, so what its handlers send is dropped
func configClient() *IRCClient {
	c := &IRCClient{
		Id:       "c1",
		Config:   &IRCConfig{Nick: "me", Channels: []string{"#a", "#key secret"}},
		Channels: map[string]*IRCChannel{},
	}
	for _, name := range []string{"#a", "#key"} {
		c.Channels[name] = &IRCChannel{Name: name, Topic: &IRCTopic{}, Nicks: map[string][]byte{"me": {}, "bob": {}}}
	}
	return c
}

// handleLine runs line through its handler, if it has one, as irc-go's
// callback would, and returns what the handler wants published.
func handleLine(t *testing.T, c *IRCClient, line string) effects {
	t.Helper()
	m, err := ircmsg.ParseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	return handleMessage(c, m)
}

func handleMessage(c *IRCClient, m ircmsg.Message) effects {
	h, ok := handlers[m.Command]
	if !ok {
		return effects{}
	}
	return c.run(c.conn, h, m)
}

// handle runs line through its handler and returns the config changes it
// published.
func handle(t *testing.T, c *IRCClient, line string) []*IRCConfigChange {
	t.Helper()
	return handleLine(t, c, line).changes
}

func changed(t *testing.T, changes []*IRCConfigChange) string {
	t.Helper()
	if len(changes) == 0 {
		return ""
	}
	if len(changes) > 1 {
		t.Fatalf("%d changes published", len(changes))
	}
	b, _ := json.Marshal(changes[0])
	return string(b)
}

// Our own JOIN and PART, and a KICK of us, change the config's channel
// list, and the whole new list is published. Names compare
// case-insensitively and keep a channel's key.
func TestChannelChangesUpdateConfig(t *testing.T) {
	for _, tc := range []struct {
		line     string
		channels []string
		want     string // the published change, or "" for none
	}{
		{":me!u@h JOIN #B", []string{"#a", "#key secret", "#B"}, `{"Id":"c1","Config":{"Channels":["#a","#key secret","#B"]}}`},
		{":me!u@h JOIN #A", []string{"#a", "#key secret"}, ""},
		{":me!u@h JOIN #key", []string{"#a", "#key secret"}, ""},
		{":bob!u@h JOIN #B", []string{"#a", "#key secret"}, ""},
		{":me!u@h PART #a", []string{"#key secret"}, `{"Id":"c1","Config":{"Channels":["#key secret"]}}`},
		{":me!u@h PART #KEY :bye", []string{"#a"}, `{"Id":"c1","Config":{"Channels":["#a"]}}`},
		{":me!u@h PART #elsewhere", []string{"#a", "#key secret"}, ""},
		{":bob!u@h PART #a", []string{"#a", "#key secret"}, ""},
		{":op!u@h KICK #a me :out", []string{"#key secret"}, `{"Id":"c1","Config":{"Channels":["#key secret"]}}`},
		{":me!u@h KICK #a bob :out", []string{"#a", "#key secret"}, ""},
		{":bob!u@h PRIVMSG #a :hi", []string{"#a", "#key secret"}, ""},
	} {
		c := configClient()
		got := changed(t, handle(t, c, tc.line))
		if got != tc.want {
			t.Errorf("%s: published %s, want %s", tc.line, got, tc.want)
		}
		if !reflect.DeepEqual(c.Config.Channels, tc.channels) {
			t.Errorf("%s: Config.Channels %q, want %q", tc.line, c.Config.Channels, tc.channels)
		}
	}
}

// Parting the last channel publishes an empty list, not a missing one.
func TestPartLastChannel(t *testing.T) {
	c := configClient()
	handle(t, c, ":me!u@h PART #a")
	got := changed(t, handle(t, c, ":me!u@h PART #key"))
	if want := `{"Id":"c1","Config":{"Channels":[]}}`; got != want {
		t.Errorf("published %s, want %s", got, want)
	}
}

// A kick removes the kicked nick from the channel, not the kicker.
func TestKickRemovesKickedNick(t *testing.T) {
	c := configClient()
	handle(t, c, ":me!u@h KICK #a bob :out")
	nicks := c.Channels["#a"].Nicks
	if _, ok := nicks["bob"]; ok {
		t.Error("bob still in #a")
	}
	if _, ok := nicks["me"]; !ok {
		t.Error("the kicker was removed")
	}
}
