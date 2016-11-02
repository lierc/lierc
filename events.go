package lierc

import (
	"fmt"
	"log"
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

	handlers["005"] = func(client *IRCClient, message *IRCMessage) {
		for i := 1; i < len(message.Params)-1; i++ {
			client.Isupport = append(client.Isupport, message.Params[i])
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
			nicks := make(map[string]bool)
			for _, nick := range buff {
				nicks[nick] = true
			}
			if channel, ok := client.Channels[name]; ok {
				channel.Nicks = nicks
			}
			delete(client.nickbuff, name)
		}
	}

	handlers["JOIN"] = func(client *IRCClient, message *IRCMessage) {
		name := message.Params[0]
		nick := message.Prefix.Name
		if nick == client.Nick {
			client.Channels[name] = &IRCChannel{
				Topic: &IRCTopic{},
				Name:  name,
				Nicks: make(map[string]bool),
			}
		}
		if channel, ok := client.Channels[name]; ok {
			channel.Nicks[nick] = true
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

	handlers["333"] = func(client *IRCClient, message *IRCMessage) {
		channel := message.Params[1]
		if client.Channels[channel] != nil {
			time, _ := strconv.ParseInt(message.Params[3], 10, 64)
			client.Channels[channel].Topic.Time = time
			client.Channels[channel].Topic.User = message.Params[2]
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
				delete(channel.Nicks, nick)
				channel.Nicks[new_nick] = true
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
