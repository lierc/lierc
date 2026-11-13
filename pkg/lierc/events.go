package lierc

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"
)

// effects is what handling one message publishes besides the line itself
// (which the raw callback publishes), sent after c is unlocked.
type effects struct {
	multi   *IRCClientMessage // QUIT and NICK, with their channels
	changes []*IRCConfigChange
}

// A handler updates c's state for one message from the server, with c
// locked. Each is registered as an irc-go callback for its command on
// every connection (see connect); irc-go runs them on the connection's
// read goroutine, after its own callbacks and before the raw callback.
type handler func(c *IRCClient, m ircmsg.Message, e *effects)

// eachHandler calls add with each command we handle and its handler.
func eachHandler(add func(command string, h handler)) {
	add(ircevent.RPL_WELCOME, (*IRCClient).onWelcome)
	add(ircevent.ERR_ERRONEUSNICKNAME, (*IRCClient).onNickRefused)
	add(ircevent.ERR_NICKNAMEINUSE, (*IRCClient).onNickRefused)
	add(ircevent.ERR_UNAVAILRESOURCE, (*IRCClient).onNickRefused)
	add(errBanNickChange, (*IRCClient).onNickChangeRefused)
	add(errNickTooFast, (*IRCClient).onNickChangeRefused)
	add(ircevent.RPL_NAMREPLY, (*IRCClient).onNames)
	add(ircevent.RPL_ENDOFNAMES, (*IRCClient).onEndOfNames)
	add("MODE", (*IRCClient).onMode)
	add("JOIN", (*IRCClient).onJoin)
	add("KICK", (*IRCClient).onKick)
	add("PART", (*IRCClient).onPart)
	add("QUIT", (*IRCClient).onQuit)
	add("TOPIC", (*IRCClient).onTopic)
	add(ircevent.RPL_TOPIC, (*IRCClient).onTopicReply)
	add(ircevent.RPL_CHANNELMODEIS, (*IRCClient).onChannelModes)
	add(ircevent.RPL_TOPICTIME, (*IRCClient).onTopicWhoTime)
	add("PRIVMSG", (*IRCClient).onPrivmsg)
	add("NICK", (*IRCClient).onNick)
}

// multiMessage is m sent to the channels it affected. channels must be
// non-nil, even if empty: that's what makes it a multi message.
func multiMessage(c *IRCClient, m ircmsg.Message, channels []string) *IRCClientMessage {
	return &IRCClientMessage{
		Message:  newIRCMessage(m),
		Id:       c.Id,
		Channels: channels,
	}
}

// onWelcome handles 001. irc-go takes our nick from it, and with KeepNick
// never tries to win the configured one back after we registered under a
// fallback. The rest of setup waits for Connect to return (finishSetup).
// The backoff is reset when the connection ends, if it lasted (stableAfter).
func (c *IRCClient) onWelcome(m ircmsg.Message, e *effects) {
	c.welcomedAt = time.Now()
}

// onNickRefused handles 432, 433 and 437, a nick refused. During
// registration (before 001) it picks the next nick to try (see
// registrationNickRefused); with KeepNick irc-go has no handler of its
// own for these. Afterwards, a refusal of the nick the user asked for ends
// the request; nothing else needs undoing, since the request was sent as a
// plain NICK and irc-go never asks for a nick itself.
func (c *IRCClient) onNickRefused(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 || c.conn == nil {
		return
	}
	if c.conn.CurrentNick() == "" {
		c.registrationNickRefused(m)
		return
	}
	c.endNickRequest(m)
}

// Nick change refusals irc-go has no names for, since they aren't in the
// RFCs; most servers (solanum, hybrid, ircu, Unreal) send them.
const (
	errBanNickChange = "435" // can't change nick while banned in a channel
	errNickTooFast   = "438" // nick changed too recently
)

// onNickChangeRefused handles 435 and 438, a nick change refused after
// registration: a refusal of the nick the user asked for ends the
// request, as for 433. Where the nick appears varies by server; a reply
// that doesn't have it second leaves the request until the next NICK or
// reconnect.
func (c *IRCClient) onNickChangeRefused(m ircmsg.Message, e *effects) {
	if c.conn == nil || c.conn.CurrentNick() == "" {
		return
	}
	c.endNickRequest(m)
}

// endNickRequest ends the user's nick request if m, a refusal, names the
// requested nick. A short reply ("433 * :reason") doesn't say which
// nick; it can't be the request's.
func (c *IRCClient) endNickRequest(m ircmsg.Message) {
	if len(m.Params) >= 3 && c.requestedNick != "" && strings.EqualFold(m.Params[1], c.requestedNick) {
		c.requestedNick = ""
	}
}

// maxNickTries is how many refused nicks one registration attempt takes
// before it's given up; liercd reconnects after it as usual.
const maxNickTries = 10

// registrationNickRefused picks the next nick to register with after the
// server refused one, or stops the connection when there's none left:
//
//   - in use (433, 437): the nick with "_" appended, so me, me_, me__.
//     A server that cuts what we sent to its nick length refuses the cut
//     nick instead (verylongni for verylongni_); then the last character
//     before the "_"s becomes one, keeping the length (verylongn_).
//   - not allowed (432), the configured nick: stop, since trying it again
//     can't work; the user changes it in the connection's settings
//   - not allowed (432), one of our fallbacks (most likely too long): drop
//     a character from what it was built on, so verylongnick_ becomes
//     verylongnic_, and stop once there's nothing left to drop
//
// After maxNickTries the attempt is given up, and liercd reconnects
// later. c is locked.
func (c *IRCClient) registrationNickRefused(m ircmsg.Message) {
	// a short reply ("433 * :reason") doesn't say which nick; it's the
	// one we sent last
	refused := c.regNick
	if len(m.Params) >= 3 {
		refused = m.Params[1]
	}
	reason := m.Params[len(m.Params)-1]
	if m.Command == ircevent.ERR_ERRONEUSNICKNAME && strings.EqualFold(refused, c.Config.Nick) {
		c.stop(fmt.Sprintf("Nickname %q isn't allowed by the server: %s", refused, reason))
		return
	}

	c.regTries++
	if c.regTries > maxNickTries {
		c.giveUp("Couldn't find a free nickname: " + reason)
		return
	}

	var next string
	switch {
	case m.Command == ircevent.ERR_ERRONEUSNICKNAME:
		base := []rune(strings.TrimRight(refused, "_"))
		if len(base) <= 1 {
			c.stop("Couldn't find a nickname the server allows: " + reason)
			return
		}
		next = string(base[:len(base)-1]) + "_"
	case len([]rune(refused)) < len([]rune(c.regNick)):
		next = underscoreLast(refused)
		if next == "" {
			c.giveUp("Couldn't find a free nickname: " + reason)
			return
		}
	default:
		next = refused + "_"
	}
	c.regNick = next
	c.Send("NICK " + next)
}

// underscoreLast is nick with its last character before any trailing "_"s
// made a "_" too, so it keeps its length: nick_ becomes nic__. It's ""
// when only one such character is left.
func underscoreLast(nick string) string {
	r := []rune(nick)
	base := len([]rune(strings.TrimRight(nick, "_")))
	if base <= 1 {
		return ""
	}
	r[base-1] = '_'
	return string(r)
}

// giveUp ends the current attempt, with reason as its status; unlike
// stop, liercd reconnects after it as usual. c is locked.
func (c *IRCClient) giveUp(reason string) {
	c.gaveUp = reason
	if c.socket != nil {
		c.socket.Close()
	}
}

// stop ends the current attempt and keeps liercd from reconnecting: the
// connection waits, showing reason as its status, until it's destroyed
// and created again (its settings edited) or liercd restarts. c is
// locked.
func (c *IRCClient) stop(reason string) {
	c.stopped = reason
	if c.socket != nil {
		c.socket.Close()
	}
}

// onNames handles 353, a NAMES reply: the channel's nicks and their modes.
func (c *IRCClient) onNames(m ircmsg.Message, e *effects) {
	if len(m.Params) < 4 {
		return
	}
	name := strings.ToLower(m.Params[2])
	if channel, ok := c.Channels[name]; ok {
		if channel.Synced {
			channel.Synced = false
			channel.Nicks = make(map[string][]byte, 0)
		}

		nicks := strings.Split(m.Params[3], " ")
		for _, nick := range nicks {
			if len(nick) > 0 {
				if mode, ok := c.NickPrefixMode(nick[0]); ok {
					channel.Nicks[nick[1:]] = []byte{mode}
				} else {
					channel.Nicks[nick] = []byte{}
				}
			}
		}
	}
}

// onEndOfNames handles 366, the end of a NAMES reply.
func (c *IRCClient) onEndOfNames(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	name := strings.ToLower(m.Params[1])
	if channel, ok := c.Channels[name]; ok {
		channel.Synced = true
	}
}

// onMode handles a channel's MODE change.
func (c *IRCClient) onMode(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	name := strings.ToLower(m.Params[0])
	if m.Nick() == name {
		// global user mode hmm
	} else if channel, ok := c.Channels[name]; ok {
		if m.Params[1] == "" {
			return
		}
		action := m.Params[1][0]
		modes := []byte(m.Params[1][1:])

		for _, mode := range modes {
			if c.IsNickMode(mode) {
				if len(m.Params) > 2 {
					channel.SetNickMode(action, mode, m.Params[2])
				}
			} else if len(m.Params) == 2 {
				channel.SetMode(action, mode)
			}
		}
	}
}

// onJoin handles JOIN: ours adds the channel (and saves it to the config).
func (c *IRCClient) onJoin(m ircmsg.Message, e *effects) {
	if len(m.Params) < 1 {
		return
	}
	name := strings.ToLower(m.Params[0])
	nick := m.Nick()
	if nick == c.currentNick() {
		c.Channels[name] = &IRCChannel{
			Topic:  &IRCTopic{},
			Name:   m.Params[0], // preserve case
			Nicks:  make(map[string][]byte),
			Synced: false,
		}
		if nuh, err := m.NUH(); err == nil {
			c.Host = nuh.Host
			c.User = nuh.User
		}
		c.Send(fmt.Sprintf("MODE %s", m.Params[0]))
		if c.addConfigChannel(m.Params[0]) {
			c.configChanged(e, "Channels")
		}
	}
	if channel, ok := c.Channels[name]; ok {
		channel.Nicks[nick] = []byte{}
	}
}

// onKick handles KICK <channel> <nick> [reason]: the kicked nick is a
// parameter, the prefix is whoever kicked them.
func (c *IRCClient) onKick(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	name := strings.ToLower(m.Params[0])
	nick := m.Params[1]
	if nick == c.currentNick() {
		delete(c.Channels, name)
		if c.removeConfigChannel(name) {
			c.configChanged(e, "Channels")
		}
	} else if channel, ok := c.Channels[name]; ok {
		delete(channel.Nicks, nick)
	}
}

// onPart handles PART: ours removes the channel (and from the config).
func (c *IRCClient) onPart(m ircmsg.Message, e *effects) {
	if len(m.Params) < 1 {
		return
	}
	name := strings.ToLower(m.Params[0])
	nick := m.Nick()
	if nick == c.currentNick() {
		delete(c.Channels, name)
		if c.removeConfigChannel(name) {
			c.configChanged(e, "Channels")
		}
	} else if channel, ok := c.Channels[name]; ok {
		delete(channel.Nicks, nick)
	}
}

// onQuit handles QUIT, published to the channels the nick was in.
func (c *IRCClient) onQuit(m ircmsg.Message, e *effects) {
	channels := []string{}
	nick := m.Nick()
	for _, channel := range c.Channels {
		if _, ok := channel.Nicks[nick]; ok {
			delete(channel.Nicks, nick)
			channels = append(channels, channel.Name)
		}
	}
	e.multi = multiMessage(c, m, channels)
}

// onTopic handles a TOPIC change.
func (c *IRCClient) onTopic(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	nick := m.Nick()
	name := strings.ToLower(m.Params[0])
	topic := m.Params[1]
	if channel, ok := c.Channels[name]; ok {
		channel.Topic.Topic = topic
		channel.Topic.Time = time.Now().Unix()
		channel.Topic.User = nick
	}
}

// onTopicReply handles 332, a channel's topic on join.
func (c *IRCClient) onTopicReply(m ircmsg.Message, e *effects) {
	if len(m.Params) < 3 {
		return
	}
	name := strings.ToLower(m.Params[1])
	topic := m.Params[2]
	if channel, ok := c.Channels[name]; ok {
		channel.Topic.Topic = topic
	}
}

// onChannelModes handles 324, a channel's modes.
func (c *IRCClient) onChannelModes(m ircmsg.Message, e *effects) {
	if len(m.Params) < 3 {
		return
	}
	name := strings.ToLower(m.Params[1])
	if channel, ok := c.Channels[name]; ok {
		if m.Params[2] != "" {
			channel.Mode = []byte(m.Params[2][1:])
		}
	}
}

// onTopicWhoTime handles 333, who set a channel's topic, and when.
func (c *IRCClient) onTopicWhoTime(m ircmsg.Message, e *effects) {
	if len(m.Params) < 4 {
		return
	}
	name := strings.ToLower(m.Params[1])
	if channel, ok := c.Channels[name]; ok {
		time, _ := strconv.ParseInt(m.Params[3], 10, 64)
		channel.Topic.Time = time
		channel.Topic.User = m.Params[2]
	}
}

// onPrivmsg handles PRIVMSG; it only logs CTCP VERSION requests.
func (c *IRCClient) onPrivmsg(m ircmsg.Message, e *effects) {
	if len(m.Params) < 2 {
		return
	}
	text := m.Params[1]
	if len(text) >= 7 && text[0:7] == "\x01VERSION" {
		log.Printf("%v", m)
	}
}

// onNick handles NICK, published to the channels the nick was in. Ours
// is saved to the config only when it's the nick we asked for.
func (c *IRCClient) onNick(m ircmsg.Message, e *effects) {
	if len(m.Params) < 1 {
		return
	}
	nick := m.Nick()
	new_nick := m.Params[0]
	channels := []string{}
	// irc-go has already followed our own change, so it's ours when
	// the new nick is irc-go's current one
	if nick != new_nick && new_nick == c.currentNick() {
		// save only a nick we asked for, as the server spells it, not a
		// server's rename
		if c.requestedNick != "" && strings.EqualFold(new_nick, c.requestedNick) {
			c.requestedNick = ""
			c.Config.Nick = new_nick
			c.configChanged(e, "Nick")
		}
	}
	for _, channel := range c.Channels {
		if _, ok := channel.Nicks[nick]; ok {
			channel.Nicks[new_nick] = channel.Nicks[nick]
			delete(channel.Nicks, nick)
			channels = append(channels, channel.Name)
		}
	}
	e.multi = multiMessage(c, m, channels)
}

// onLine gets every line from the server, after irc-go's own handlers.
func (c *IRCClient) onLine(raw string, m ircmsg.Message, err error) {
	if err != nil {
		// irc-go has logged it already
		if c.debug > 0 {
			c.log.Printf("unparseable line %q: %v", raw, err)
		}
		return
	}
	msg := newIRCMessage(m)
	c.Lock()
	c.annotate(msg)
	c.Unlock()
	c.sink.Message(&IRCClientMessage{Id: c.Id, Message: msg})
}

// run runs h for m with c locked, and returns what h wants published;
// pass it to publishEffects once c is unlocked. A message from a
// connection that's no longer c's is ignored.
func (c *IRCClient) run(conn *ircevent.Connection, h handler, m ircmsg.Message) effects {
	c.Lock()
	// deferred, so a handler that panics (irc-go recovers it) can't
	// leave c locked
	defer c.Unlock()

	var e effects
	if c.conn == conn {
		h(c, m, &e)
	}
	return e
}

// annotate marks a PRIVMSG or NOTICE sent to our nick as Direct, and a
// PRIVMSG from us as Self. c is locked.
func (c *IRCClient) annotate(m *IRCMessage) {
	if m.Prefix == nil {
		m.Prefix = &IRCPrefix{}
	}
	if (m.Command == "PRIVMSG" || m.Command == "NOTICE") && len(m.Params) > 0 {
		nick := c.currentNick()
		m.Direct = m.Params[0] == nick
		if m.Command == "PRIVMSG" {
			m.Prefix.Self = m.Prefix.Name != "" && m.Prefix.User == c.User && m.Prefix.Name == nick
		}
	}
}

// publishEffects publishes the multi message and config changes e holds.
// c must be unlocked (see Sink).
func (c *IRCClient) publishEffects(e effects) {
	if e.multi != nil {
		c.sink.Message(e.multi)
	}
	for _, change := range e.changes {
		c.sink.ConfigChange(change)
	}
}
