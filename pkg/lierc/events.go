package lierc

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var handlers = map[string]func(*IRCClient, *IRCMessage){}

func init() {
	handlers["001"] = func(c *IRCClient, m *IRCMessage) {
		c.Nick = m.Params[0]
		c.Welcome()
	}

	var prefix, _ = regexp.Compile("^PREFIX=\\(([^)]+)\\)(.+)$")

	handlers["005"] = func(c *IRCClient, m *IRCMessage) {
		for i := 1; i < len(m.Params)-1; i++ {
			c.Isupport = append(c.Isupport, m.Params[i])
			if res := prefix.FindStringSubmatch(m.Params[i]); res != nil {
				c.prefix = make([][]byte, 0)
				for i, _ := range res[1] {
					if len(res[2]) >= i {
						c.prefix = append(c.prefix, []byte{res[2][i], res[1][i]})
					}
				}
			}
		}
	}

	handlers["376"] = func(c *IRCClient, m *IRCMessage) {
		c.Welcome()
	}

	handlers["421"] = func(c *IRCClient, m *IRCMessage) {
		if strings.HasPrefix(m.Command, "CAP LS") {
			c.CapNotSupported()
		}
	}

	handlers["422"] = func(c *IRCClient, m *IRCMessage) {
		c.Welcome()
	}

	handlers["433"] = func(c *IRCClient, m *IRCMessage) {
		c.NickCollision(m)
	}

	handlers["437"] = func(c *IRCClient, m *IRCMessage) {
		c.NickCollision(m)
	}

	handlers["353"] = func(c *IRCClient, m *IRCMessage) {
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

	handlers["366"] = func(c *IRCClient, m *IRCMessage) {
		name := strings.ToLower(m.Params[1])
		if channel, ok := c.Channels[name]; ok {
			channel.Synced = true
		}
	}

	handlers["MODE"] = func(c *IRCClient, m *IRCMessage) {
		name := strings.ToLower(m.Params[0])
		if m.Prefix.Name == name {
			// global user mode hmm
		} else if channel, ok := c.Channels[name]; ok {
			action := m.Params[1][0]
			modes := []byte(m.Params[1][1:])

			for _, mode := range modes {
				if c.IsNickMode(mode) {
					channel.SetNickMode(action, mode, m.Params[2])
				} else if len(m.Params) == 2 {
					channel.SetMode(action, mode)
				}
			}
		}
	}

	handlers["JOIN"] = func(c *IRCClient, m *IRCMessage) {
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
		}
		if channel, ok := c.Channels[name]; ok {
			channel.Nicks[nick] = []byte{}
		}
	}

	handlers["KICK"] = func(c *IRCClient, m *IRCMessage) {
		name := strings.ToLower(m.Params[0])
		nick := m.Prefix.Name
		if nick == c.Nick {
			delete(c.Channels, name)
		} else if channel, ok := c.Channels[name]; ok {
			delete(channel.Nicks, nick)
		}
	}

	handlers["PART"] = func(c *IRCClient, m *IRCMessage) {
		name := strings.ToLower(m.Params[0])
		nick := m.Prefix.Name
		if nick == c.Nick {
			delete(c.Channels, name)
		} else if channel, ok := c.Channels[name]; ok {
			delete(channel.Nicks, nick)
		}
	}

	handlers["QUIT"] = func(c *IRCClient, m *IRCMessage) {
		channels := []string{}
		nick := m.Prefix.Name
		for _, channel := range c.Channels {
			if _, ok := channel.Nicks[nick]; ok {
				delete(channel.Nicks, nick)
				channels = append(channels, channel.Name)
			}
		}
		Multi <- &IRCClientMultiMessage{
			Message:  m,
			Id:       c.Id,
			Channels: channels,
		}
	}

	handlers["TOPIC"] = func(c *IRCClient, m *IRCMessage) {
		nick := m.Prefix.Name
		name := strings.ToLower(m.Params[0])
		topic := m.Params[1]
		if channel, ok := c.Channels[name]; ok {
			channel.Topic.Topic = topic
			channel.Topic.Time = time.Now().Unix()
			channel.Topic.User = nick
		}
	}

	handlers["332"] = func(c *IRCClient, m *IRCMessage) {
		name := strings.ToLower(m.Params[1])
		topic := m.Params[2]
		if channel, ok := c.Channels[name]; ok {
			channel.Topic.Topic = topic
		}
	}

	handlers["324"] = func(c *IRCClient, m *IRCMessage) {
		name := strings.ToLower(m.Params[1])
		if channel, ok := c.Channels[name]; ok {
			channel.Mode = []byte(m.Params[2][1:])
		}
	}

	handlers["333"] = func(c *IRCClient, m *IRCMessage) {
		name := strings.ToLower(m.Params[1])
		if channel, ok := c.Channels[name]; ok {
			time, _ := strconv.ParseInt(m.Params[3], 10, 64)
			channel.Topic.Time = time
			channel.Topic.User = m.Params[2]
		}
	}

	handlers["PING"] = func(c *IRCClient, m *IRCMessage) {
		c.Send(fmt.Sprintf("PONG %s", m.Params[0]))
	}

	handlers["NOTICE"] = func(c *IRCClient, m *IRCMessage) {
		m.Direct = m.Params[0] == c.Nick
	}

	handlers["PRIVMSG"] = func(c *IRCClient, m *IRCMessage) {
		m.Direct = m.Params[0] == c.Nick
		text := m.Params[1]
		if len(text) >= 7 && text[0:7] == "\x01VERSION" {
			log.Printf("%v", m)
		}
	}

	handlers["NICK"] = func(c *IRCClient, m *IRCMessage) {
		nick := m.Prefix.Name
		new_nick := m.Params[0]
		channels := []string{}
		if nick == c.Nick {
			c.Nick = new_nick
		}
		for _, channel := range c.Channels {
			if _, ok := channel.Nicks[nick]; ok {
				channel.Nicks[new_nick] = channel.Nicks[nick]
				delete(channel.Nicks, nick)
				channels = append(channels, channel.Name)
			}
		}
		Multi <- &IRCClientMultiMessage{
			Message:  m,
			Id:       c.Id,
			Channels: channels,
		}
	}

	handlers["CAP"] = func(c *IRCClient, m *IRCMessage) {
		if len(m.Params) < 2 {
			return
		}

		switch m.Params[1] {
		case "LS":
			if m.Params[2] == "*" {
				if len(m.Params) > 2 {
					c.CapAdd(strings.Split(m.Params[3], " "))
				}
			} else {
				c.CapAdd(strings.Split(m.Params[2], " "))
				c.CapListDone()
			}
		case "ACK":
			c.CapAck(strings.Split(m.Params[2], " "))
		case "NAK":
			c.CapNak(strings.Split(m.Params[2], " "))
		case "NEW":
			c.CapAdd(strings.Split(m.Params[3], " "))
		case "DEL":
			c.CapDel(strings.Split(m.Params[3], " "))
		}
	}

	handlers["AUTHENTICATE"] = func(c *IRCClient, m *IRCMessage) {
		c.SASLAuth(m.Params[0])
	}

	e := []string{"902", "904", "905", "906"}
	for _, i := range e {
		handlers[i] = func(c *IRCClient, m *IRCMessage) {
			c.SASLAuthFailed(m.Params[0])
		}
	}

	handlers["903"] = func(c *IRCClient, m *IRCMessage) {
		c.SASLAuthSuccess()
	}
}
