package logger

import (
	"github.com/ergochat/irc-go/ircevent"

	"github.com/lierc/lierc/pkg/lierc"
)

// rplStatsConn is 250, the highest connection count, which irc-go has no
// name for.
const rplStatsConn = "250"

// logKind is how a message from the chats topic is handled.
type logKind int

const (
	// publish it live, but don't store it
	logPass logKind = iota
	// drop it entirely (NICK and QUIT without their channels, from older liercd)
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

	// older liercd also sent these plainly; they come with their
	// channels too, and are logged from those (logMulti)
	case "NICK", "QUIT":
		return logSkip

	// 000 is liercd's connection status line (Connected, Reconnecting
	// in 30s, …); CONNECT and DISCONNECT are the older form
	case "000", "ERROR", "CONNECT", "DISCONNECT",
		// welcome
		ircevent.RPL_WELCOME, ircevent.RPL_YOURHOST, ircevent.RPL_CREATED,
		ircevent.RPL_MYINFO, ircevent.RPL_ISUPPORT,
		// user counts
		rplStatsConn, ircevent.RPL_LUSERCLIENT, ircevent.RPL_LUSEROP,
		ircevent.RPL_LUSERUNKNOWN, ircevent.RPL_LUSERCHANNELS, ircevent.RPL_LUSERME,
		ircevent.RPL_LOCALUSERS, ircevent.RPL_GLOBALUSERS,
		// MOTD
		ircevent.RPL_MOTD, ircevent.RPL_MOTDSTART, ircevent.RPL_ENDOFMOTD:
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
