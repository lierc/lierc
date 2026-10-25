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

// How each command is logged. Anything not listed, apart from the
// NOTICE and numeric rules in logType, is passed through.
var loggable = map[string]logKind{
	"PRIVMSG":    logTarget,
	"TAGMSG":     logTarget,
	"JOIN":       logTarget,
	"PART":       logTarget,
	"TOPIC":      logTarget,
	"MODE":       logTarget,
	"NICK":       logSkip,
	"QUIT":       logSkip,
	"ERROR":      logStatus,
	"CONNECT":    logStatus,
	"DISCONNECT": logStatus,
	"001":        logStatus,
	"002":        logStatus,
	"003":        logStatus,
	"004":        logStatus,
	"005":        logStatus,
	"251":        logStatus,
	"252":        logStatus,
	"253":        logStatus,
	"254":        logStatus,
	"255":        logStatus,
	"265":        logStatus,
	"266":        logStatus,
	"250":        logStatus,
	"375":        logStatus,
	"372":        logStatus,
	"376":        logStatus,
}

func logType(m *lierc.IRCMessage) logKind {
	// TAGMSG also carries typing notifications, which are only
	// useful live; reactions are the only ones worth keeping
	if m.Command == "TAGMSG" && !isReaction(m) {
		return logPass
	}

	if kind, ok := loggable[m.Command]; ok {
		return kind
	}

	if m.Command == "NOTICE" {
		if m.Direct {
			return logTarget
		}
		return logStatus
	}

	if m.Command[0] == '4' || m.Command[0] == '5' || m.Command[0] == '9' {
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
