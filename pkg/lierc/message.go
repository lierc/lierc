package lierc

import (
	"time"

	"github.com/ergochat/irc-go/ircmsg"
)

type IRCPrefix struct {
	Name   string
	User   string
	Server string
	Self   bool
}

type IRCMessage struct {
	Prefix  *IRCPrefix
	Tags    map[string]string
	Command string
	Time    float64
	Params  []string
	Direct  bool
}

// newIRCMessage builds an IRCMessage from a parsed line.
func newIRCMessage(m ircmsg.Message) *IRCMessage {
	prefix := &IRCPrefix{}
	if m.Source != "" {
		// ParseNUH only fails on an empty source
		nuh, _ := ircmsg.ParseNUH(m.Source)
		prefix.Name, prefix.User, prefix.Server = nuh.Name, nuh.User, nuh.Host
	}

	return &IRCMessage{
		Prefix:  prefix,
		Tags:    m.AllTags(),
		Command: m.Command,
		Params:  m.Params,
		Time:    float64(time.Now().UnixNano()) / float64(time.Second),
	}
}

func ParseIRCMessage(line string) (*IRCMessage, error) {
	m, err := ircmsg.ParseLine(line)
	if err != nil {
		return nil, err
	}
	return newIRCMessage(m), nil
}
