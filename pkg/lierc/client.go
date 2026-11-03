package lierc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"
)

var hostname, _ = os.Hostname()

var Multi = make(chan *IRCClientMultiMessage)
var Events = make(chan *IRCClientMessage)
var Status = make(chan *IRCClientStatus)

// ConfigChanges gets the fields of a connection's config that liercd
// changed itself (its channels as it joins and parts, a nick it was asked
// to change to), for the logger to save.
var ConfigChanges = make(chan *IRCConfigChange)

// IRCConfigChange is the changed fields of one connection's config.
type IRCConfigChange struct {
	Id     string
	Config map[string]interface{}
}

const (
	CAP_ECHO_MESSAGE = "echo-message"
	CAP_MESSAGE_TAGS = "message-tags"
)

// Tunables, package vars so tests can shorten them.
var (
	retryUnit      = 15 * time.Second // backoff step; the cap is 20 × retryUnit
	ircTimeout     = 2 * time.Minute
	ircKeepAlive   = 4 * time.Minute
	destroyTimeout = 2 * time.Second

	ircLogOutput io.Writer = os.Stderr
)

// backoff is how long to wait before reconnect attempt number retries.
func backoff(retries int) time.Duration {
	if retries > 20 {
		retries = 20
	}
	return retryUnit * time.Duration(retries)
}

type IRCClient struct {
	sync.RWMutex
	Id            string
	Config        *IRCConfig
	Channels      map[string]*IRCChannel
	Nick          string
	User          string
	Host          string
	Registered    bool
	Connected     bool
	StatusMessage string
	Retries       int
	prefix        [][]byte
	quitting      bool
	debug         int64
	timer         *time.Timer
	requestedNick string // the nick last sent with NICK, until it's taken
	// irc-go's preferred nick before requestedNick, restored if the
	// request is refused: the configured nick, or a server's rename
	nickBeforeRequest string
	log               *log.Logger

	// The current connection attempt. conn and socket are nil between
	// attempts; attemptDone is closed once an attempt has ended and
	// published its statuses, and is replaced when the next one starts.
	conn        *ircevent.Connection
	socket      net.Conn
	attemptDone chan struct{}
	cancelDial  context.CancelFunc
	ackedCaps   map[string]string // conn.AcknowledgedCaps(), once Connect has returned
	joined      bool              // config channels joined on this attempt

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

func NewIRCClient(config *IRCConfig, Id string) *IRCClient {
	c := &IRCClient{
		Config:        config,
		Channels:      make(map[string]*IRCChannel),
		StatusMessage: "Connecting",
		debug:         LogLevel(os.Getenv("LIERC_DEBUG")),
		Nick:          config.Nick,
		User:          config.User,
		Host:          hostname,
		Id:            Id,
		prefix:        defaultPrefix,
		log:           log.New(ircLogOutput, Id+" ", log.LstdFlags),
	}

	Status <- c.Status()
	go c.connect()

	return c
}

// connect runs one connection attempt: it blocks until registration
// completes or the attempt fails. A failure, or the later end of a
// registered connection, schedules the next attempt (see endAttempt).
func (c *IRCClient) connect() {
	c.Lock()
	if c.quitting {
		c.Unlock()
		return
	}

	config := c.Config
	user := config.User
	if user == "" {
		user = config.Nick
	}
	conn := &ircevent.Connection{
		Server:      config.Server(),
		Nick:        config.Nick,
		User:        user,
		RealName:    user,
		RequestCaps: []string{CAP_ECHO_MESSAGE, CAP_MESSAGE_TAGS},
		Timeout:     ircTimeout,
		KeepAlive:   ircKeepAlive,
		QuitMessage: "bye",
		// set so Connect doesn't fill it in while Destroy's Quit reads it
		MaxLineLen: 512,
		Log:        c.log,
		Debug:      c.debug > 1,
	}
	if config.SASL {
		conn.UseSASL = true
		conn.SASLLogin = config.User
		conn.SASLPassword = config.Pass
		conn.SASLMech = "PLAIN"
	} else {
		conn.Password = config.Pass
	}

	s := newSender(c.Id, conn.SendRaw)

	// Destroy cancels a dial still in progress
	dialCtx, cancel := context.WithCancel(context.Background())
	var socket *trackedConn
	dial := dialer(Exempt(config), config.Ssl, func(sock net.Conn) {
		socket = &trackedConn{Conn: sock}
		c.onConnected(conn, s, socket)
	})
	conn.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		ctx, cancelCtx := context.WithCancel(ctx)
		defer cancelCtx()
		defer context.AfterFunc(dialCtx, cancelCtx)()
		if _, err := dial(ctx, network, addr); err != nil {
			return nil, err
		}
		return socket, nil
	}
	conn.AddRawCallback(c.onLine)
	conn.AddDisconnectCallback(func(ircmsg.Message) {
		// irc-go's own reconnect loop (Loop) is never started, but
		// marking the connection quit keeps it from ever reconnecting
		go conn.Quit()
		msg := "Disconnected"
		if err := socket.err(); err != nil {
			msg = err.Error()
		}
		c.endAttempt(conn, s, msg)
	})

	c.conn = conn
	c.socket = nil
	// until 001 says otherwise, we're the configured nick
	c.Nick = config.Nick
	c.attemptDone = make(chan struct{})
	c.cancelDial = cancel
	c.ackedCaps = nil
	c.Registered = false
	c.joined = false
	c.prefix = defaultPrefix
	c.Unlock()

	if c.debug > 0 {
		c.log.Printf("Connecting to %s", conn.Server)
	}

	err := conn.Connect()
	cancel()
	if err != nil {
		c.endAttempt(conn, s, err.Error())
		return
	}

	// irc-go writes the acked caps during the handshake, which has
	// finished now; it doesn't change them after
	c.Lock()
	if c.conn == conn {
		c.ackedCaps = make(map[string]string)
		for k, v := range conn.AcknowledgedCaps() {
			c.ackedCaps[k] = v
		}
	}
	c.Unlock()
}

// onConnected runs once the socket (and TLS) is up, before registration.
func (c *IRCClient) onConnected(conn *ircevent.Connection, s *sender, sock net.Conn) {
	c.Lock()
	if c.conn != conn || c.quitting {
		// Destroy came first: end the attempt rather than register
		c.Unlock()
		sock.Close()
		return
	}
	c.socket = sock
	c.Unlock()
	c.sender.Store(s)
	c.setStatus(true, "Connected")
}

// endAttempt ends the attempt conn belongs to, once: Connect's error and
// the disconnect callback can both report the same end. It publishes
// the disconnected status and, unless quitting, schedules the next
// attempt, then closes attemptDone.
func (c *IRCClient) endAttempt(conn *ircevent.Connection, s *sender, msg string) {
	c.sender.CompareAndSwap(s, nil)
	s.Stop()

	c.Lock()
	if c.conn != conn {
		c.Unlock()
		return
	}
	sock, done := c.socket, c.attemptDone
	c.conn = nil
	c.socket = nil
	c.cancelDial = nil
	c.ackedCaps = nil
	c.Registered = false
	c.Unlock()

	if sock != nil {
		sock.Close()
	}
	c.setStatus(false, msg)

	c.Lock()
	if c.quitting {
		c.Unlock()
		close(done)
		return
	}
	c.Retries++
	delay := backoff(c.Retries)
	// the next attempt waits for this one's statuses to be published
	c.timer = time.AfterFunc(delay, func() {
		<-done
		c.connect()
	})
	c.Unlock()

	c.setStatus(false, fmt.Sprintf("Reconnecting in %s", delay))
	close(done)
}

// setStatus updates the status and publishes it; c must be unlocked.
func (c *IRCClient) setStatus(connected bool, msg string) {
	c.Lock()
	c.Connected = connected
	c.StatusMessage = msg
	status := c.Status()
	c.Unlock()

	if c.debug > 0 {
		c.log.Print(msg)
	}
	Status <- status
}

// onLine gets every line from the server, after irc-go's own handlers.
func (c *IRCClient) onLine(raw string, m ircmsg.Message, err error) {
	if err != nil {
		c.log.Printf("unparseable line %q: %v", raw, err)
		return
	}
	msg := newIRCMessage(raw, m)
	e := c.Message(msg)
	c.publish(msg, e)
}

// Destroy quits the connection, or stops waiting to reconnect, and
// returns once the connection has ended. A server that ignores QUIT has
// its socket closed after destroyTimeout.
func (c *IRCClient) Destroy() {
	c.Lock()
	c.quitting = true
	if c.timer != nil {
		c.timer.Stop()
	}
	conn, done, cancel, connected := c.conn, c.attemptDone, c.cancelDial, c.Connected
	c.Unlock()

	c.setStatus(connected, "Closing connection")

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		// in a goroutine: Quit can block behind a stalled write
		go conn.Quit()
	}
	if done != nil {
		timer := time.NewTimer(destroyTimeout)
		select {
		case <-done:
		case <-timer.C:
			c.Lock()
			sock := c.socket
			c.Unlock()
			if sock != nil {
				sock.Close()
			}
			<-done
		}
		timer.Stop()
	}

	if s := c.sender.Swap(nil); s != nil {
		s.Stop()
	}
}

func (c *IRCClient) Status() *IRCClientStatus {
	return &IRCClientStatus{
		Id:        c.Id,
		Message:   c.StatusMessage,
		Connected: c.Connected,
		Host:      c.Config.Host,
	}
}

// Send queues line for the server; it's dropped when not connected.
func (c *IRCClient) Send(line string) {
	if c.debug > 1 {
		c.log.Printf("---> %s", line)
	}
	if s := c.sender.Load(); s != nil {
		s.Send(line)
	}
}

func (c *IRCClient) PortMap() (error, string, string) {
	c.RLock()
	sock := c.socket
	c.RUnlock()
	if sock == nil {
		return fmt.Errorf("Not connected"), "", ""
	}
	_, local, _ := net.SplitHostPort(sock.LocalAddr().String())
	_, remote, _ := net.SplitHostPort(sock.RemoteAddr().String())
	return nil, local, remote
}

// Message runs m through its handler with c locked and returns what the
// handler wants published; pass it to publish once c is unlocked.
func (c *IRCClient) Message(m *IRCMessage) effects {
	c.Lock()
	defer c.Unlock()

	var e effects
	if m.Prefix == nil {
		m.Prefix = &IRCPrefix{}
	}
	if (m.Command == "PRIVMSG" || m.Command == "NOTICE") && len(m.Params) > 0 {
		m.Direct = m.Params[0] == c.Nick
		if m.Command == "PRIVMSG" {
			m.Prefix.Self = m.Prefix.Name != "" && m.Prefix.User == c.User && m.Prefix.Name == c.Nick
		}
	}
	if handler, ok := handlers[m.Command]; ok {
		handler(c, m, &e)
	}

	return e
}

// publish sends m to Events, then the multi message and config changes e
// holds. c must be unlocked: the channels may block.
func (c *IRCClient) publish(m *IRCMessage, e effects) {
	Events <- &IRCClientMessage{Id: c.Id, Message: m}
	if e.multi != nil {
		Multi <- e.multi
	}
	for _, change := range e.changes {
		ConfigChanges <- change
	}
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

// setPrefix takes the nick prefixes from isupport's PREFIX, or the
// default when it's missing or malformed. c is locked.
func (c *IRCClient) setPrefix(isupport map[string]string) {
	if prefix, ok := parsePrefix(isupport["PREFIX"]); ok {
		c.prefix = prefix
	} else {
		c.prefix = defaultPrefix
	}
}

func (c *IRCClient) CapEnabled(name string) bool {
	c.RLock()
	defer c.RUnlock()
	_, ok := c.ackedCaps[name]
	return ok
}

// trackedConn records the first read or write error on the socket, for
// the disconnected status (irc-go doesn't expose its last error).
type trackedConn struct {
	net.Conn
	mu    sync.Mutex
	first error
}

func (t *trackedConn) record(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.first == nil {
		t.first = err
	}
}

func (t *trackedConn) err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.first
}

func (t *trackedConn) Read(b []byte) (int, error) {
	n, err := t.Conn.Read(b)
	if err != nil {
		t.record(err)
	}
	return n, err
}

func (t *trackedConn) Write(b []byte) (int, error) {
	n, err := t.Conn.Write(b)
	if err != nil {
		t.record(err)
	}
	return n, err
}

func (c *IRCClient) NicksWithPrefix(channel *IRCChannel) []string {
	names := make([]string, 0)
	for nick, mode := range channel.Nicks {
		names = append(names, c.NickPrefix(mode)+nick)
	}
	return names
}

func (c *IRCClient) NickPrefix(mode []byte) string {
	for _, mapping := range c.prefix {
		if bytes.IndexByte(mode, mapping[1]) != -1 {
			return string(mapping[0])
		}
	}
	return ""
}

func (c *IRCClient) NickPrefixMode(prefix byte) (byte, bool) {
	for _, mapping := range c.prefix {
		if prefix == mapping[0] {
			return mapping[1], true
		}
	}
	return 0, false
}

func (c *IRCClient) IsNickMode(mode byte) bool {
	for _, mapping := range c.prefix {
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

	nick := c.Nick
	if nick == "" {
		nick = c.Config.Nick
	}

	return &IRCClientData{
		Id:         c.Id,
		Config:     c.Config,
		Nick:       nick,
		Channels:   channels,
		Registered: c.Registered,
		Status:     c.Status(),
		Isupport:   isupportTokens(c.isupport()),
		CapsAcked:  c.capsAcked(),
	}
}

// capsAcked is the caps the server acknowledged, nil when there's no
// connection.
func (c *IRCClient) capsAcked() []string {
	var caps []string
	for k := range c.ackedCaps {
		caps = append(caps, k)
	}
	return caps
}

// ChangeNick asks the server for nick: records it as requested and calls
// conn.SetNick (not a raw NICK, which irc-go would later revert).
// Without a connection it only records the request.
func (c *IRCClient) ChangeNick(nick string) {
	c.Lock()
	defer c.Unlock()
	// a request replacing one still pending keeps the nick from before
	// both, rather than the pending request's
	if c.requestedNick == "" {
		if c.conn != nil {
			c.nickBeforeRequest = c.conn.PreferredNick()
		} else {
			c.nickBeforeRequest = c.Config.Nick
		}
	}
	c.requestedNick = nick
	c.setPreferredNick(nick)
}

// setPreferredNick makes nick the one irc-go keeps (or tries to win
// back on its keepalive ticks), and sends NICK nick, which the server
// ignores when it's our current nick. c is locked, and stays locked
// while irc-go queues the line: its write queue is buffered and its
// writer never needs c, so at worst this waits for a stalled socket's
// write deadline or the connection's end. Keeping the lock orders the
// handlers' calls with ChangeNick's.
func (c *IRCClient) setPreferredNick(nick string) {
	if c.conn != nil {
		c.conn.SetNick(nick)
	}
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
