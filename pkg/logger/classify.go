package logger

import (
	"github.com/lierc/lierc/pkg/lierc"
)

// How each command from the chats topic is logged: "#" under the
// channel (or nick) it was sent to, "status" under the connection's
// status panel, "skip" not at all (these arrive on the multi topic
// instead). Anything not listed is passed through without logging.
var loggable = map[string]string{
	"PRIVMSG":    "#",
	"TAGMSG":     "#",
	"JOIN":       "#",
	"PART":       "#",
	"TOPIC":      "#",
	"MODE":       "#",
	"NICK":       "skip",
	"QUIT":       "skip",
	"ERROR":      "status",
	"CONNECT":    "status",
	"DISCONNECT": "status",
	"001":        "status",
	"002":        "status",
	"003":        "status",
	"004":        "status",
	"005":        "status",
	"251":        "status",
	"252":        "status",
	"253":        "status",
	"254":        "status",
	"255":        "status",
	"265":        "status",
	"266":        "status",
	"250":        "status",
	"375":        "status",
	"372":        "status",
	"376":        "status",
}

func logType(m *lierc.IRCMessage) string {
	// TAGMSG also carries typing notifications, which are only
	// useful live; reactions are the only ones worth keeping
	if m.Command == "TAGMSG" && !isReaction(m) {
		return "pass"
	}

	t, ok := loggable[m.Command]

	if ok {
		return t
	}

	if m.Command == "NOTICE" {
		if m.Direct {
			return "#"
		} else {
			return "status"
		}
	}

	if m.Command[0] == '4' || m.Command[0] == '5' || m.Command[0] == '9' {
		return "status"
	}

	return "pass"
}

func isReaction(m *lierc.IRCMessage) bool {
	_, react := m.Tags["+draft/react"]
	_, unreact := m.Tags["+draft/unreact"]
	return react || unreact
}

type action int

const (
	// drop the message entirely
	actionSkip action = iota
	// publish it live, but don't store it
	actionPass
	// store it, then publish it with its id
	actionStore
)

// route is what to do with a message from the chats topic.
type route struct {
	action action
	// the channel to store it under (lowercased when stored)
	channel string
	// it's a private message from someone else, so the conversation
	// with channel (their nick) is recorded too
	private bool
}

func routeChat(m *lierc.IRCMessage) route {
	switch t := logType(m); t {
	case "pass":
		return route{action: actionPass}
	case "skip":
		return route{action: actionSkip}
	case "#":
		if m.Direct && !m.Prefix.Self {
			return route{action: actionStore, channel: m.Prefix.Name, private: true}
		}
		return route{action: actionStore, channel: m.Params[0]}
	default:
		return route{action: actionStore, channel: t}
	}
}
