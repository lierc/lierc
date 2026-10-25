package logger

import (
	"github.com/lierc/lierc/pkg/lierc"
)

// logKind is how a message from the chats topic is handled.
type logKind int

const (
	// publish it live, but don't store it
	logPass logKind = iota
	// drop it entirely (NICK and QUIT arrive on the multi topic instead)
	logSkip
	// store it under the channel, or nick, it was sent to
	logTarget
	// store it under the connection's status panel
	logStatus
)

// for test failures and logging
func (k logKind) String() string {
	switch k {
	case logPass:
		return "pass"
	case logSkip:
		return "skip"
	case logTarget:
		return "target"
	case logStatus:
		return "status"
	}
	return "unknown"
}

// the channel name status messages are stored under
const statusChannel = "status"

func logType(m *lierc.IRCMessage) logKind {
	switch m.Command {
	case "TAGMSG":
		// also carries typing notifications, which are only useful
		// live; reactions are the only ones worth keeping
		if isReaction(m) {
			return logTarget
		}
		return logPass

	case "PRIVMSG", "JOIN", "PART", "TOPIC", "MODE":
		return logTarget

	case "NOTICE":
		if m.Direct {
			return logTarget
		}
		return logStatus

	// these arrive on the multi topic instead
	case "NICK", "QUIT":
		return logSkip

	case "ERROR", "CONNECT", "DISCONNECT",
		"001", "002", "003", "004", "005", // welcome
		"250", "251", "252", "253", "254", "255", "265", "266", // user counts
		"372", "375", "376": // MOTD
		return logStatus
	}

	// other errors (4xx, 5xx) and 9xx (SASL) numerics
	if c := m.Command[0]; c == '4' || c == '5' || c == '9' {
		return logStatus
	}
	return logPass
}

func isReaction(m *lierc.IRCMessage) bool {
	_, react := m.Tags["+draft/react"]
	_, unreact := m.Tags["+draft/unreact"]
	return react || unreact
}

// route is what to do with a message from the chats topic.
type route struct {
	kind logKind
	// the channel to store it under (lowercased when stored)
	channel string
	// it's a private message from someone else, so the conversation
	// with channel (their nick) is recorded too
	private bool
}

func routeChat(m *lierc.IRCMessage) route {
	switch kind := logType(m); kind {
	case logTarget:
		if m.Direct && !m.Prefix.Self {
			return route{kind: kind, channel: m.Prefix.Name, private: true}
		}
		return route{kind: kind, channel: m.Params[0]}
	case logStatus:
		return route{kind: kind, channel: statusChannel}
	default:
		return route{kind: kind}
	}
}
