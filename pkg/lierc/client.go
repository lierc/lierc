package lierc

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ergochat/irc-go/ircevent"
)

var hostname, _ = os.Hostname()

// A Sink gets what a client publishes. It must be safe for concurrent
// use: a client calls it from several goroutines (statuses in order, under
// statusMu), never with the client locked. It shouldn't block for long,
// since the client waits.
type Sink interface {
	// Message is a line from the server, or one we sent that it won't
	// echo.
	Message(*IRCClientMessage)
	// Multi is a NICK or QUIT, with the channels it affected.
	Multi(*IRCClientMultiMessage)
	// Status is the connection's state whenever it changes.
	Status(*IRCClientStatus)
	// ConfigChange is the fields of a connection's config that liercd
	// changed itself (its channels as it joins and parts, a nick it was
	// asked to change to), for the logger to save.
	ConfigChange(*IRCConfigChange)
}

// IRCConfigChange is the changed fields of one connection's config.
type IRCConfigChange struct {
	Id     string
	Config map[string]interface{}
}

const (
	CAP_ECHO_MESSAGE = "echo-message"
	CAP_MESSAGE_TAGS = "message-tags"
)

type IRCClient struct {
	sync.RWMutex
	sink Sink
	// statusMu orders status publishing: held (before c's lock, never
	// inside it) from deciding a status until it's sent, so a "Reconnecting
	// in" can't follow Destroy's "Closing connection"
	statusMu sync.Mutex
	Id       string
	Config   *IRCConfig
	Channels map[string]*IRCChannel
	User     string
	Host     string
	// Registered is set once irc-go has finished registering (Connect has
	// returned) and the connection is set up; the API's messages are
	// refused until then
	Registered    bool
	Connected     bool
	StatusMessage string
	Retries       int
	quitting      bool
	// stopped is why liercd stopped reconnecting (the server refused
	// every nick it could try), or "" while it reconnects as usual
	stopped       string
	debug         int64
	timer         *time.Timer
	requestedNick string // the nick last sent with NICK, until it's taken
	log           *log.Logger

	// The current connection attempt. conn and socket are nil between
	// attempts; attemptDone is closed once an attempt has ended and
	// published its statuses, and is replaced when the next one starts.
	conn        *ircevent.Connection
	socket      net.Conn
	attemptDone chan struct{}
	cancelDial  context.CancelFunc
	// registering: the nick last sent and how many the server has
	// refused (see registrationNickRefused), and why the attempt was
	// given up, if it was; liercd still reconnects after it
	regNick  string
	regTries int
	gaveUp   string
	// when the server sent 001, or zero before it; see stableAfter
	welcomedAt time.Time

	// sender is the current attempt's send queue, set only while
	// connected, so Send drops lines while disconnected. It's atomic
	// because handlers call Send with c locked and the API without.
	sender atomic.Pointer[sender]
}

type IRCClientStatus struct {
	Connected bool
	Message   string
	Id        string
	Host      string
}

type IRCClientMultiMessage struct {
	Id       string
	Message  *IRCMessage
	Channels []string
}

type IRCClientMessage struct {
	Id      string
	Message *IRCMessage
}

func LogLevel(env string) int64 {
	if env == "" {
		return 0
	} else {
		i, err := strconv.ParseInt(env, 10, 0)
		if err != nil {
			panic(err)
		}
		return i
	}
}

// NewIRCClient makes a client for config, publishing to sink. It doesn't
// connect until Start.
func NewIRCClient(config *IRCConfig, Id string, sink Sink) *IRCClient {
	c := &IRCClient{
		sink:          sink,
		Config:        config,
		Channels:      make(map[string]*IRCChannel),
		StatusMessage: "Connecting",
		debug:         LogLevel(os.Getenv("LIERC_DEBUG")),
		User:          config.User,
		Host:          hostname,
		Id:            Id,
		log:           log.New(ircLogOutput, Id+" ", log.LstdFlags),
	}

	sink.Status(c.Status())

	return c
}

// Start begins connecting, in the background. Call it once the client
// can be found (liercd lists it first): the server may look the
// connection up, through identd, as soon as it accepts it. A client
// destroyed before Start never connects.
func (c *IRCClient) Start() {
	go c.connect()
}

// currentNick is our nick on the server, as irc-go has it (set at 001,
// following our NICK changes), or the configured nick before 001 and
// between connections. c is locked.
func (c *IRCClient) currentNick() string {
	if c.conn != nil {
		if nick := c.conn.CurrentNick(); nick != "" {
			return nick
		}
	}
	return c.Config.Nick
}

// IsRegistered reports whether the connection is set up (see
// Registered). Don't call it with c locked.
func (c *IRCClient) IsRegistered() bool {
	c.RLock()
	defer c.RUnlock()
	return c.Registered
}

// CurrentNick is our nick on the server, or the configured nick when
// there isn't one yet. Don't call it with c locked.
func (c *IRCClient) CurrentNick() string {
	c.RLock()
	defer c.RUnlock()
	return c.currentNick()
}

// Source is our nick!user@host, for messages we echo ourselves. Don't
// call it with c locked.
func (c *IRCClient) Source() string {
	c.RLock()
	defer c.RUnlock()
	return c.currentNick() + "!" + c.User + "@" + c.Host
}

// Join joins the config's channels. c is locked.
func (c *IRCClient) Join() {
	for _, channel := range c.Config.Channels {
		if channel != "" {
			c.Send(fmt.Sprintf("JOIN %s", channel))
		}
	}
}

// isupport is the current connection's ISUPPORT map, nil before
// registration. c is locked. The map is irc-go's: never write to it.
func (c *IRCClient) isupport() map[string]string {
	if c.conn == nil {
		return nil
	}
	return c.conn.ISupport()
}

// prefixes is the current connection's nick prefixes, from its ISUPPORT
// PREFIX; the default before registration and between attempts. c is
// locked.
func (c *IRCClient) prefixes() [][]byte {
	return prefixesFrom(c.isupport())
}

// ackedCaps is the current connection's acked caps, nil until it has
// registered. c is locked. The map is irc-go's: never write to it. It's
// read only once Connect has returned (Registered, set by finishSetup):
// until then irc-go's Connect goroutine may still be writing it, which can
// overlap our handling of 001, 005 or 376 when a server welcomes us
// early, and concurrent map access is fatal to the whole process.
func (c *IRCClient) ackedCaps() map[string]string {
	if !c.Registered || c.conn == nil {
		return nil
	}
	return c.conn.AcknowledgedCaps()
}

// CapEnabled reports whether the server acked the cap on the current
// connection, false until it has registered. It takes c's read lock:
// don't call it with c locked (from a handler, say).
func (c *IRCClient) CapEnabled(name string) bool {
	c.RLock()
	defer c.RUnlock()
	_, ok := c.ackedCaps()[name]
	return ok
}

func (c *IRCClient) NicksWithPrefix(channel *IRCChannel) []string {
	prefixes := c.prefixes()
	names := make([]string, 0)
	for nick, mode := range channel.Nicks {
		names = append(names, nickPrefix(prefixes, mode)+nick)
	}
	return names
}

func (c *IRCClient) NickPrefix(mode []byte) string {
	return nickPrefix(c.prefixes(), mode)
}

// nickPrefix is the symbol for the highest of a nick's modes, in
// prefixes' order, or "" for none.
func nickPrefix(prefixes [][]byte, mode []byte) string {
	for _, mapping := range prefixes {
		if bytes.IndexByte(mode, mapping[1]) != -1 {
			return string(mapping[0])
		}
	}
	return ""
}

func (c *IRCClient) NickPrefixMode(prefix byte) (byte, bool) {
	for _, mapping := range c.prefixes() {
		if prefix == mapping[0] {
			return mapping[1], true
		}
	}
	return 0, false
}

func (c *IRCClient) IsNickMode(mode byte) bool {
	for _, mapping := range c.prefixes() {
		if mode == mapping[1] {
			return true
		}
	}
	return false
}

type IRCClientData struct {
	Id         string
	Config     *IRCConfig
	Nick       string
	Channels   []*IRCChannelData
	Registered bool
	Status     *IRCClientStatus
	Isupport   []string
	CapsAcked  []string
}

type IRCChannelData struct {
	Name  string
	Nicks []string
	Topic *IRCTopic
	Mode  string
}

// ClientData is the status JSON; the caller holds c's RLock.
func (c *IRCClient) ClientData() *IRCClientData {
	channels := make([]*IRCChannelData, 0)

	for _, channel := range c.Channels {
		data := &IRCChannelData{
			Name:  channel.Name,
			Nicks: c.NicksWithPrefix(channel),
			Topic: channel.Topic,
			Mode:  "+" + string(channel.Mode),
		}
		channels = append(channels, data)
	}

	return &IRCClientData{
		Id:         c.Id,
		Config:     c.Config,
		Nick:       c.currentNick(),
		Channels:   channels,
		Registered: c.Registered,
		Status:     c.Status(),
		Isupport:   isupportTokens(c.isupport()),
		CapsAcked:  c.capsAcked(),
	}
}

// capsAcked is the caps the server acknowledged on the current
// connection, nil until it has registered.
func (c *IRCClient) capsAcked() []string {
	var caps []string
	for k := range c.ackedCaps() {
		caps = append(caps, k)
	}
	return caps
}

// RequestNick records that the user asked the server for nick, so taking
// it updates the config. Call it before sending the NICK, so the server's
// reply can't arrive first.
func (c *IRCClient) RequestNick(nick string) {
	c.Lock()
	defer c.Unlock()
	c.requestedNick = nick
}

// configChanged publishes the current value of the given config fields.
// Called with c locked, from the handlers; the change is sent by publish.
func (c *IRCClient) configChanged(e *effects, fields ...string) {
	change := &IRCConfigChange{Id: c.Id, Config: map[string]interface{}{}}
	for _, f := range fields {
		switch f {
		case "Channels":
			change.Config[f] = append([]string{}, c.Config.Channels...)
		case "Nick":
			change.Config[f] = c.Config.Nick
		}
	}
	e.changes = append(e.changes, change)
}

// channelName is the channel a config entry names: "#chan key" is #chan,
// lowercased.
func channelName(entry string) string {
	if f := strings.Fields(entry); len(f) > 0 {
		return strings.ToLower(f[0])
	}
	return ""
}

// addConfigChannel adds name to the config's channels unless it's there,
// and reports whether it was added.
func (c *IRCClient) addConfigChannel(name string) bool {
	for _, entry := range c.Config.Channels {
		if channelName(entry) == strings.ToLower(name) {
			return false
		}
	}
	c.Config.Channels = append(c.Config.Channels, name)
	return true
}

// removeConfigChannel removes name (and its key) from the config's
// channels, and reports whether it was there.
func (c *IRCClient) removeConfigChannel(name string) bool {
	kept := make([]string, 0, len(c.Config.Channels))
	for _, entry := range c.Config.Channels {
		if channelName(entry) != strings.ToLower(name) {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(c.Config.Channels) {
		return false
	}
	c.Config.Channels = kept
	return true
}
