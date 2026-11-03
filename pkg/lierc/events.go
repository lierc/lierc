package lierc

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ergochat/irc-go/ircmsg"
)

// effects is what handling one message publishes besides the line itself
// (which the raw callback publishes), sent after c is unlocked.
type effects struct {
	multi   *IRCClientMultiMessage // QUIT and NICK
	changes []*IRCConfigChange
}

// A handler updates c's state for one message from the server, with c
// locked. Each is registered as an irc-go callback for its command on
// every connection (see connect); irc-go runs them on the connection's
// read goroutine, after its own callbacks and before the raw callback.
type handler func(c *IRCClient, m ircmsg.Message, e *effects)

// eachHandler calls add with each command we handle and its handler.
func eachHandler(add func(command string, h handler)) {
	add("001", (*IRCClient).onWelcome)
	add("432", (*IRCClient).onNickRefused)
	add("433", (*IRCClient).onNickRefused)
	add("437", (*IRCClient).onNickRefused)
	add("353", (*IRCClient).onNames)
	add("366", (*IRCClient).onEndOfNames)
	add("MODE", (*IRCClient).onMode)
	add("JOIN", (*IRCClient).onJoin)
	add("KICK", (*IRCClient).onKick)
	add("PART", (*IRCClient).onPart)
	add("QUIT", (*IRCClient).onQuit)
	add("TOPIC", (*IRCClient).onTopic)
	add("332", (*IRCClient).onTopicReply)
	add("324", (*IRCClient).onChannelModes)
	add("333", (*IRCClient).onTopicWhoTime)
	add("PRIVMSG", (*IRCClient).onPrivmsg)
	add("NICK", (*IRCClient).onNick)
}

// multiMessage is m sent to the channels it affected.
func multiMessage(c *IRCClient, m ircmsg.Message, channels []string) *IRCClientMultiMessage {
	return &IRCClientMultiMessage{
		Message:  newIRCMessage(m),
		Id:       c.Id,
		Channels: channels,
	}
}

// onWelcome handles 001. irc-go takes our nick from it; the rest of setup
// waits for Connect to return (finishSetup).
func (c *IRCClient) onWelcome(m ircmsg.Message, e *effects) {
	c.Retries = 0
}

// onNickRefused handles 432, 433 and 437 after registration: a nick we
// asked for was refused, so don't let irc-go keep asking for it. It goes
// back to the nick it preferred before the request: the configured nick,
// which it still wins back, or a server's rename, which it doesn't fight.
// Refusals of other nicks (irc-go's own attempts to win the configured
// nick back, or registration's fallbacks) are irc-go's business.
func (c *IRCClient) onNickRefused(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 || !c.Registered || c.conn == nil {
		return
	}
	if c.requestedNick != "" && strings.EqualFold(m.Params[1], c.requestedNick) {
		prev := c.nickBeforeRequest
		if prev == "" {
			prev = c.Config.Nick
		}
		c.requestedNick = ""
		c.nickBeforeRequest = ""
		c.setPreferredNick(prev)
	}
}

// onNames handles 353, a NAMES reply: the channel's nicks and their modes.
func (c *IRCClient) onNames(m ircmsg.Message, e *effects) {
	if len(m.Params) < 4 {
		return
	}
	name := strings.ToLower(m.Params[2])
	if channel, ok := c.Channels[name]; ok {
		if channel.Synced {
			channel.Synced = false
			channel.Nicks = make(map[string][]byte, 0)
		}

		nicks := strings.Split(m.Params[3], " ")
		for _, nick := range nicks {
			if len(nick) > 0 {
				if mode, ok := c.NickPrefixMode(nick[0]); ok {
					channel.Nicks[nick[1:]] = []byte{mode}
				} else {
					channel.Nicks[nick] = []byte{}
				}
			}
		}
	}
}

// onEndOfNames handles 366, the end of a NAMES reply.
func (c *IRCClient) onEndOfNames(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	name := strings.ToLower(m.Params[1])
	if channel, ok := c.Channels[name]; ok {
		channel.Synced = true
	}
}

// onMode handles a channel's MODE change.
func (c *IRCClient) onMode(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	name := strings.ToLower(m.Params[0])
	if m.Nick() == name {
		// global user mode hmm
	} else if channel, ok := c.Channels[name]; ok {
		if m.Params[1] == "" {
			return
		}
		action := m.Params[1][0]
		modes := []byte(m.Params[1][1:])

		for _, mode := range modes {
			if c.IsNickMode(mode) {
				if len(m.Params) > 2 {
					channel.SetNickMode(action, mode, m.Params[2])
				}
			} else if len(m.Params) == 2 {
				channel.SetMode(action, mode)
			}
		}
	}
}

// onJoin handles JOIN: ours adds the channel (and saves it to the config).
func (c *IRCClient) onJoin(m ircmsg.Message, e *effects) {
	if len(m.Params) < 1 {
		return
	}
	name := strings.ToLower(m.Params[0])
	nick := m.Nick()
	if nick == c.currentNick() {
		c.Channels[name] = &IRCChannel{
			Topic:  &IRCTopic{},
			Name:   m.Params[0], // preserve case
			Nicks:  make(map[string][]byte),
			Synced: false,
		}
		if nuh, err := m.NUH(); err == nil {
			c.Host = nuh.Host
			c.User = nuh.User
		}
		c.Send(fmt.Sprintf("MODE %s", m.Params[0]))
		if c.addConfigChannel(m.Params[0]) {
			c.configChanged(e, "Channels")
		}
	}
	if channel, ok := c.Channels[name]; ok {
		channel.Nicks[nick] = []byte{}
	}
}

// onKick handles KICK <channel> <nick> [reason]: the kicked nick is a
// parameter, the prefix is whoever kicked them.
func (c *IRCClient) onKick(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	name := strings.ToLower(m.Params[0])
	nick := m.Params[1]
	if nick == c.currentNick() {
		delete(c.Channels, name)
		if c.removeConfigChannel(name) {
			c.configChanged(e, "Channels")
		}
	} else if channel, ok := c.Channels[name]; ok {
		delete(channel.Nicks, nick)
	}
}

// onPart handles PART: ours removes the channel (and from the config).
func (c *IRCClient) onPart(m ircmsg.Message, e *effects) {
	if len(m.Params) < 1 {
		return
	}
	name := strings.ToLower(m.Params[0])
	nick := m.Nick()
	if nick == c.currentNick() {
		delete(c.Channels, name)
		if c.removeConfigChannel(name) {
			c.configChanged(e, "Channels")
		}
	} else if channel, ok := c.Channels[name]; ok {
		delete(channel.Nicks, nick)
	}
}

// onQuit handles QUIT, published to the channels the nick was in.
func (c *IRCClient) onQuit(m ircmsg.Message, e *effects) {
	channels := []string{}
	nick := m.Nick()
	for _, channel := range c.Channels {
		if _, ok := channel.Nicks[nick]; ok {
			delete(channel.Nicks, nick)
			channels = append(channels, channel.Name)
		}
	}
	e.multi = multiMessage(c, m, channels)
}

// onTopic handles a TOPIC change.
func (c *IRCClient) onTopic(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	nick := m.Nick()
	name := strings.ToLower(m.Params[0])
	topic := m.Params[1]
	if channel, ok := c.Channels[name]; ok {
		channel.Topic.Topic = topic
		channel.Topic.Time = time.Now().Unix()
		channel.Topic.User = nick
	}
}

// onTopicReply handles 332, a channel's topic on join.
func (c *IRCClient) onTopicReply(m ircmsg.Message, e *effects) {
	if len(m.Params) < 3 {
		return
	}
	name := strings.ToLower(m.Params[1])
	topic := m.Params[2]
	if channel, ok := c.Channels[name]; ok {
		channel.Topic.Topic = topic
	}
}

// onChannelModes handles 324, a channel's modes.
func (c *IRCClient) onChannelModes(m ircmsg.Message, e *effects) {
	if len(m.Params) < 3 {
		return
	}
	name := strings.ToLower(m.Params[1])
	if channel, ok := c.Channels[name]; ok {
		if m.Params[2] != "" {
			channel.Mode = []byte(m.Params[2][1:])
		}
	}
}

// onTopicWhoTime handles 333, who set a channel's topic, and when.
func (c *IRCClient) onTopicWhoTime(m ircmsg.Message, e *effects) {
	if len(m.Params) < 4 {
		return
	}
	name := strings.ToLower(m.Params[1])
	if channel, ok := c.Channels[name]; ok {
		time, _ := strconv.ParseInt(m.Params[3], 10, 64)
		channel.Topic.Time = time
		channel.Topic.User = m.Params[2]
	}
}

// onPrivmsg handles PRIVMSG; it only logs CTCP VERSION requests.
func (c *IRCClient) onPrivmsg(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	text := m.Params[1]
	if len(text) >= 7 && text[0:7] == "\x01VERSION" {
		log.Printf("%v", m)
	}
}

// onNick handles NICK, published to the channels the nick was in. Ours
// is saved to the config only when it's the nick we asked for.
func (c *IRCClient) onNick(m ircmsg.Message, e *effects) {
	if len(m.Params) < 1 {
		return
	}
	nick := m.Nick()
	new_nick := m.Params[0]
	channels := []string{}
	// irc-go has already followed our own change, so it's ours when
	// the new nick is irc-go's current one
	if nick != new_nick && new_nick == c.currentNick() {
		// save only a nick we asked for, not a server's rename
		if c.requestedNick != "" && strings.EqualFold(new_nick, c.requestedNick) {
			c.requestedNick = ""
			c.nickBeforeRequest = ""
			c.Config.Nick = new_nick
			c.configChanged(e, "Nick")
		} else if c.conn != nil && !strings.EqualFold(new_nick, c.conn.PreferredNick()) {
			// a server or services rename: keep it, rather than
			// have irc-go fight to change back. (A match is irc-go
			// winning its preferred nick back.)
			c.requestedNick = ""
			c.nickBeforeRequest = ""
			c.setPreferredNick(new_nick)
		}
	}
	for _, channel := range c.Channels {
		if _, ok := channel.Nicks[nick]; ok {
			channel.Nicks[new_nick] = channel.Nicks[nick]
			delete(channel.Nicks, nick)
			channels = append(channels, channel.Name)
		}
	}
	e.multi = multiMessage(c, m, channels)
}
