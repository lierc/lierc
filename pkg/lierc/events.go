package lierc

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// effects is what handling one message publishes, sent after c is unlocked.
type effects struct {
	multi   *IRCClientMultiMessage // QUIT and NICK
	changes []*IRCConfigChange
}

var handlers = map[string]func(c *IRCClient, m *IRCMessage, e *effects){}

func init() {
	handlers["001"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 1 {
			return
		}
		c.Nick = m.Params[0]
		c.Registered = true
		c.Retries = 0
	}

	// The end of registration, when irc-go has the whole ISUPPORT map:
	// take PREFIX from it, then join, so every NAMES reply sees it.
	endOfRegistration := func(c *IRCClient, m *IRCMessage, e *effects) {
		if c.joined {
			return
		}
		c.joined = true
		c.setPrefix(c.isupport())
		c.Join()
	}
	handlers["376"] = endOfRegistration
	handlers["422"] = endOfRegistration

	// A nick we asked for after registration was refused: don't let
	// irc-go keep asking for it. It goes back to the nick it preferred
	// before the request: the configured nick, which it still wins back,
	// or a server's rename, which it doesn't fight. Refusals of other
	// nicks (irc-go's own attempts to win the configured nick back, or
	// registration's fallbacks) are irc-go's business.
	refusedNick := func(c *IRCClient, m *IRCMessage, e *effects) {
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
	handlers["432"] = refusedNick
	handlers["433"] = refusedNick
	handlers["437"] = refusedNick

	handlers["353"] = func(c *IRCClient, m *IRCMessage, e *effects) {
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

	handlers["366"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 2 {
			return
		}
		name := strings.ToLower(m.Params[1])
		if channel, ok := c.Channels[name]; ok {
			channel.Synced = true
		}
	}

	handlers["MODE"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 2 {
			return
		}
		name := strings.ToLower(m.Params[0])
		if m.Prefix.Name == name {
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

	handlers["JOIN"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 1 {
			return
		}
		name := strings.ToLower(m.Params[0])
		nick := m.Prefix.Name
		if nick == c.Nick {
			c.Channels[name] = &IRCChannel{
				Topic:  &IRCTopic{},
				Name:   m.Params[0], // preserve case
				Nicks:  make(map[string][]byte),
				Synced: false,
			}
			c.Host = m.Prefix.Server
			c.User = m.Prefix.User
			c.Send(fmt.Sprintf("MODE %s", m.Params[0]))
			if c.addConfigChannel(m.Params[0]) {
				c.configChanged(e, "Channels")
			}
		}
		if channel, ok := c.Channels[name]; ok {
			channel.Nicks[nick] = []byte{}
		}
	}

	// KICK <channel> <nick> [reason]: the kicked nick is a parameter, the
	// prefix is whoever kicked them
	handlers["KICK"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 2 {
			return
		}
		name := strings.ToLower(m.Params[0])
		nick := m.Params[1]
		if nick == c.Nick {
			delete(c.Channels, name)
			if c.removeConfigChannel(name) {
				c.configChanged(e, "Channels")
			}
		} else if channel, ok := c.Channels[name]; ok {
			delete(channel.Nicks, nick)
		}
	}

	handlers["PART"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 1 {
			return
		}
		name := strings.ToLower(m.Params[0])
		nick := m.Prefix.Name
		if nick == c.Nick {
			delete(c.Channels, name)
			if c.removeConfigChannel(name) {
				c.configChanged(e, "Channels")
			}
		} else if channel, ok := c.Channels[name]; ok {
			delete(channel.Nicks, nick)
		}
	}

	handlers["QUIT"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		channels := []string{}
		nick := m.Prefix.Name
		for _, channel := range c.Channels {
			if _, ok := channel.Nicks[nick]; ok {
				delete(channel.Nicks, nick)
				channels = append(channels, channel.Name)
			}
		}
		e.multi = &IRCClientMultiMessage{
			Message:  m,
			Id:       c.Id,
			Channels: channels,
		}
	}

	handlers["TOPIC"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 2 {
			return
		}
		nick := m.Prefix.Name
		name := strings.ToLower(m.Params[0])
		topic := m.Params[1]
		if channel, ok := c.Channels[name]; ok {
			channel.Topic.Topic = topic
			channel.Topic.Time = time.Now().Unix()
			channel.Topic.User = nick
		}
	}

	handlers["332"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 3 {
			return
		}
		name := strings.ToLower(m.Params[1])
		topic := m.Params[2]
		if channel, ok := c.Channels[name]; ok {
			channel.Topic.Topic = topic
		}
	}

	handlers["324"] = func(c *IRCClient, m *IRCMessage, e *effects) {
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

	handlers["333"] = func(c *IRCClient, m *IRCMessage, e *effects) {
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

	handlers["NOTICE"] = func(c *IRCClient, m *IRCMessage, e *effects) {}

	handlers["PRIVMSG"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 2 {
			return
		}
		text := m.Params[1]
		if len(text) >= 7 && text[0:7] == "\x01VERSION" {
			log.Printf("%v", m)
		}
	}

	handlers["NICK"] = func(c *IRCClient, m *IRCMessage, e *effects) {
		if len(m.Params) < 1 {
			return
		}
		nick := m.Prefix.Name
		new_nick := m.Params[0]
		channels := []string{}
		if nick == c.Nick {
			c.Nick = new_nick
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
		e.multi = &IRCClientMultiMessage{
			Message:  m,
			Id:       c.Id,
			Channels: channels,
		}
	}
}
