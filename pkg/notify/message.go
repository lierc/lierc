package notify

import (
	"github.com/lierc/lierc/pkg/lierc"
)

type LoggedMessage struct {
	Message      *lierc.IRCMessage
	ConnectionId string
	MessageId    int
	Self         bool
	Highlight    bool
	Direct       bool
}
