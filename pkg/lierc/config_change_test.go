package lierc

import (
	"encoding/json"
	"reflect"
	"testing"
)

// a client in #a and #key (joined with a key) as "me", that sends nothing
func configClient() *IRCClient {
	c := &IRCClient{
		Id:       "c1",
		Nick:     "me",
		Config:   &IRCConfig{Nick: "me", Channels: []string{"#a", "#key secret"}},
		Channels: map[string]*IRCChannel{},
		irc:      &IRCConn{},
	}
	for _, name := range []string{"#a", "#key"} {
		c.Channels[name] = &IRCChannel{Name: name, Topic: &IRCTopic{}, Nicks: map[string][]byte{"me": {}, "bob": {}}}
	}
	return c
}

// handle runs line through c's handlers and returns the config changes
// they published.
func handle(t *testing.T, c *IRCClient, line string) []*IRCConfigChange {
	t.Helper()
	m, err := ParseIRCMessage(line)
	if err != nil {
		t.Fatal(err)
	}
	saved, savedMulti := ConfigChanges, Multi
	ConfigChanges, Multi = make(chan *IRCConfigChange, 10), make(chan *IRCClientMultiMessage, 10)
	defer func() { ConfigChanges, Multi = saved, savedMulti }()
	c.Message(m)
	var changes []*IRCConfigChange
	for {
		select {
		case ch := <-ConfigChanges:
			changes = append(changes, ch)
		default:
			return changes
		}
	}
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

// Only a nick change we asked for is saved: not a server's rename, nor a
// different nick than the one requested.
func TestNickChangeUpdatesConfig(t *testing.T) {
	c := configClient()
	c.RequestNick("new")
	if got, want := changed(t, handle(t, c, ":me!u@h NICK new")), `{"Id":"c1","Config":{"Nick":"new"}}`; got != want {
		t.Errorf("requested nick: published %s, want %s", got, want)
	}
	if c.Config.Nick != "new" || c.Nick != "new" {
		t.Errorf("Config.Nick %q Nick %q", c.Config.Nick, c.Nick)
	}

	// the request is used up
	if got := changed(t, handle(t, c, ":new!u@h NICK Guest1")); got != "" {
		t.Errorf("server rename published %s", got)
	}
	if c.Config.Nick != "new" || c.Nick != "Guest1" {
		t.Errorf("after rename: Config.Nick %q Nick %q", c.Config.Nick, c.Nick)
	}

	c = configClient()
	c.RequestNick("wanted")
	if got := changed(t, handle(t, c, ":me!u@h NICK other")); got != "" {
		t.Errorf("different nick published %s", got)
	}
	c.RequestNick("Mixed")
	if got, want := changed(t, handle(t, c, ":other!u@h NICK mixed")), `{"Id":"c1","Config":{"Nick":"mixed"}}`; got != want {
		t.Errorf("case-insensitive match: published %s, want %s", got, want)
	}

	c = configClient()
	c.RequestNick("bob2")
	if got := changed(t, handle(t, c, ":bob!u@h NICK bob2")); got != "" {
		t.Errorf("someone else's nick change published %s", got)
	}
}
