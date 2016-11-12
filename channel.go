package lierc

import (
	"strings"
)

type IRCTopic struct {
	Topic string
	User  string
	Time  int64
}

type IRCChannel struct {
	Topic *IRCTopic
	Nicks map[string]string
	Name  string
	Mode  string
}

func (channel *IRCChannel) SetMode(action byte, mode rune) {
	if action == 43 && strings.IndexRune(channel.Mode, mode) == -1 {
		channel.Mode += string(mode)
	}
	if action == 45 && strings.IndexRune(channel.Mode, mode) != -1 {
		channel.Mode = strings.Replace(channel.Mode, string(mode), "", 1)
	}
}

func (channel *IRCChannel) SetNickMode(action byte, mode rune, nick string) {
	if action == 43 && strings.IndexRune(channel.Nicks[nick], mode) == -1 {
		channel.Nicks[nick] += string(mode)
	}
	if action == 45 && strings.IndexRune(channel.Nicks[nick], mode) != -1 {
		channel.Nicks[nick] = strings.Replace(channel.Nicks[nick], string(mode), "", 1)
	}
}
