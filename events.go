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
	handlers["001"] = func(client *IRCClient, message *IRCMessage) {
		client.Nick = message.Params[0]
		client.Welcome()
	}

	var prefix, _ = regexp.Compile("^PREFIX=\\(([^)]+)\\)(.+)$")

	handlers["005"] = func(client *IRCClient, message *IRCMessage) {
		for i := 1; i < len(message.Params)-1; i++ {
			client.Isupport = append(client.Isupport, message.Params[i])
			if res := prefix.FindStringSubmatch(message.Params[i]); res != nil {
				for i, _ := range res[1] {
					if len(res[2]) >= i {
						client.prefixmap[res[2][i]] = res[1][i]
					}
				}
			}
		}
	}

	handlers["376"] = func(client *IRCClient, message *IRCMessage) {
		client.Welcome()
	}

	handlers["422"] = func(client *IRCClient, message *IRCMessage) {
		client.Welcome()
	}

	handlers["433"] = func(client *IRCClient, message *IRCMessage) {
		client.NickCollision(message)
	}

	handlers["437"] = func(client *IRCClient, message *IRCMessage) {
		client.NickCollision(message)
	}

	handlers["353"] = func(client *IRCClient, message *IRCMessage) {
		channel := message.Params[2]
		nicks := strings.Split(message.Params[3], " ")
		if _, ok := client.nickbuff[channel]; !ok {
			client.nickbuff[channel] = make([]string, 0)
		}
		for _, nick := range nicks {
			client.nickbuff[channel] = append(client.nickbuff[channel], nick)
		}
	}

	handlers["366"] = func(client *IRCClient, message *IRCMessage) {
		name := message.Params[1]
		if buff, ok := client.nickbuff[name]; ok {
			nicks := make(map[string]string)
			for _, nick := range buff {
				if len(nick) == 0 {
					continue
				}

				if mode, ok := client.prefixmap[nick[0]]; ok {
					nicks[nick[1:]] = string(mode)
				} else {
					nicks[nick] = ""
				}
			}
			if channel, ok := client.Channels[name]; ok {
				channel.Nicks = nicks
			}
			delete(client.nickbuff, name)
		}
	}

	handlers["MODE"] = func(client *IRCClient, message *IRCMessage) {
		name := message.Params[0]
		if message.Prefix.Name == name {
			// global user mode hmm
		} else if channel, ok := client.Channels[name]; ok {
			action := message.Params[1][0]
			modes := message.Params[1][1:]

			for _, mode := range modes {
				switch mode {
				case 104: // h halfop
					channel.SetNickMode(action, mode, message.Params[2])
				case 111: // o op
					channel.SetNickMode(action, mode, message.Params[2])
				case 118: // v voice
					channel.SetNickMode(action, mode, message.Params[2])
				default:
					if len(message.Params) == 2 {
						channel.SetMode(action, mode)
					}
				}
			}
		}
	}

	handlers["JOIN"] = func(client *IRCClient, message *IRCMessage) {
		name := message.Params[0]
		nick := message.Prefix.Name
		if nick == client.Nick {
			client.Channels[name] = &IRCChannel{
				Topic: &IRCTopic{},
				Name:  name,
				Nicks: make(map[string]string),
			}
			client.Send(fmt.Sprintf("MODE %s", name))
		}
		if channel, ok := client.Channels[name]; ok {
			channel.Nicks[nick] = ""
		}
	}

	handlers["KICK"] = func(client *IRCClient, message *IRCMessage) {
		name := message.Params[0]
		nick := message.Prefix.Name
		if nick == client.Nick {
			delete(client.Channels, name)
		} else if channel, ok := client.Channels[name]; ok {
			delete(channel.Nicks, nick)
		}
	}

	handlers["PART"] = func(client *IRCClient, message *IRCMessage) {
		name := message.Params[0]
		nick := message.Prefix.Name
		if nick == client.Nick {
			delete(client.Channels, name)
		} else if channel, ok := client.Channels[name]; ok {
			delete(channel.Nicks, nick)
		}
	}

	handlers["QUIT"] = func(client *IRCClient, message *IRCMessage) {
		channels := []string{}
		nick := message.Prefix.Name
		for name, channel := range client.Channels {
			if _, ok := channel.Nicks[nick]; ok {
				delete(channel.Nicks, nick)
				channels = append(channels, name)
			}
		}
		Multi <- &IRCClientMultiMessage{
			Message:  message,
			Id:       client.Id,
			Channels: channels,
		}
	}

	handlers["TOPIC"] = func(client *IRCClient, message *IRCMessage) {
		nick := message.Prefix.Name
		name := message.Params[0]
		topic := message.Params[1]
		if channel, ok := client.Channels[name]; ok {
			channel.Topic.Topic = topic
			channel.Topic.Time = time.Now().Unix()
			channel.Topic.User = nick
		}
	}

	handlers["332"] = func(client *IRCClient, message *IRCMessage) {
		name := message.Params[1]
		topic := message.Params[2]
		if channel, ok := client.Channels[name]; ok {
			channel.Topic.Topic = topic
		}
	}

	handlers["324"] = func(client *IRCClient, message *IRCMessage) {
		if channel, ok := client.Channels[message.Params[1]]; ok {
			channel.Mode = string(message.Params[2][1:])
		}
	}

	handlers["333"] = func(client *IRCClient, message *IRCMessage) {
		if channel, ok := client.Channels[message.Params[1]]; ok {
			time, _ := strconv.ParseInt(message.Params[3], 10, 64)
			channel.Topic.Time = time
			channel.Topic.User = message.Params[2]
		}
	}

	handlers["PING"] = func(client *IRCClient, message *IRCMessage) {
		client.Send(fmt.Sprintf("PONG %s", message.Params[0]))
	}

	handlers["PRIVMSG"] = func(client *IRCClient, message *IRCMessage) {
		text := message.Params[1]
		if len(text) >= 7 && text[0:7] == "\x01VERSION" {
			log.Printf("%v", message)
		}
	}

	handlers["NICK"] = func(client *IRCClient, message *IRCMessage) {
		nick := message.Prefix.Name
		new_nick := message.Params[0]
		channels := []string{}
		if nick == client.Nick {
			client.Nick = new_nick
		}
		for name, channel := range client.Channels {
			if _, ok := channel.Nicks[nick]; ok {
				channel.Nicks[new_nick] = channel.Nicks[nick]
				delete(channel.Nicks, nick)
				channels = append(channels, name)
			}
		}
		Multi <- &IRCClientMultiMessage{
			Message:  message,
			Id:       client.Id,
			Channels: channels,
		}
	}
}
