package notify

import "github.com/lierc/lierc/pkg/ircwire"

type LoggedMessage struct {
	Message      *ircwire.IRCMessage
	ConnectionId string
	MessageId    int
	Highlight    bool
}
