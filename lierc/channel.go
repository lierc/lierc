package lierc

import (
	"bytes"
)

type IRCTopic struct {
	Topic string
	User  string
	Time  int64
}

type IRCChannel struct {
	Topic  *IRCTopic
	Nicks  map[string][]byte
	Name   string
	Mode   []byte
	Synced bool
}

func (channel *IRCChannel) SetMode(action byte, mode byte) {
	switch action {
	case '+':
		channel.Mode = AddMode(channel.Mode, mode)
	case '-':
		channel.Mode = RemoveMode(channel.Mode, mode)
	}
}

func (channel *IRCChannel) SetNickMode(action byte, mode byte, nick string) {
	if modes, ok := channel.Nicks[nick]; ok {
		switch action {
		case '+':
			channel.Nicks[nick] = AddMode(modes, mode)
		case '-':
			channel.Nicks[nick] = RemoveMode(modes, mode)
		}
	}
}

func ModeContains(modes []byte, mode byte) bool {
	return bytes.IndexByte(modes, mode) != -1
}

func AddMode(modes []byte, mode byte) []byte {
	if !ModeContains(modes, mode) {
		modes = append(modes, mode)
	}
	return modes
}

func RemoveMode(modes []byte, mode byte) []byte {
	if ModeContains(modes, mode) {
		modes = bytes.Replace(modes, []byte{mode}, []byte{}, 1)
	}
	return modes
}
