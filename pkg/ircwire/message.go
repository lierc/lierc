// Package ircwire is liercd's wire format, the IRC messages it publishes
// to NSQ, the addresses it refuses to connect to, and the numerics the
// logger sorts: what liercd and the programs reading its messages
// (lierc-api's logger) share. It needs only
// irc-go's ircmsg, so it builds without lierc's irc-go fork.
//
// lierc-api pins this package by pseudo-version in its go.mod, not by a
// release, so a change here reaches its logger only when lierc-api bumps
// that pin (go get github.com/lierc/lierc@<commit>). Since liercd and the
// logger deploy separately, a change to the wire format must work with
// the other side still on the older version.
package ircwire

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

// NewIRCMessage builds an IRCMessage from a parsed line.
func NewIRCMessage(m ircmsg.Message) *IRCMessage {
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
	return NewIRCMessage(m), nil
}

// IRCClientMessage is a line from a connection, published on "chats".
// Channels is set (non-nil, even if empty) on NICK and QUIT: the
// channels the line belongs in. Ordinary lines leave it nil, which
// marshals as "Channels":null.
type IRCClientMessage struct {
	Id       string
	Message  *IRCMessage
	Channels []string
}
