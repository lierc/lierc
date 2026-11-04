package lierc

// The connection lifecycle: building an irc-go connection for each
// attempt, finishing setup when it registers, publishing statuses,
// reconnecting with backoff, and Destroy.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"
	"github.com/ergochat/irc-go/ircreader"
)

// Tunables, package vars so tests can shorten them.
var (
	retryUnit      = 15 * time.Second // backoff step; the cap is 20 × retryUnit
	ircTimeout     = 2 * time.Minute
	ircKeepAlive   = 4 * time.Minute
	destroyTimeout = 2 * time.Second

	ircLogOutput io.Writer = os.Stderr
)

// ircMaxLineLen is irc-go's line length; its reader gives up with
// ircreader.ErrReadQ after this plus 8192 bytes (for tags) without a
// newline.
const (
	ircMaxLineLen = 512
	readQLimit    = ircMaxLineLen + 8192
)

// backoff is how long to wait before reconnect attempt number retries.
func backoff(retries int) time.Duration {
	if retries > 20 {
		retries = 20
	}
	return retryUnit * time.Duration(retries)
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
		MaxLineLen: ircMaxLineLen,
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

	s := newSender(c.log, c.debug, conn.SendRaw)

	// Destroy cancels a dial still in progress
	dialCtx, cancel := context.WithCancel(context.Background())
	var socket *trackedConn
	dial := dialer(Exempt(config), config.Ssl, func(sock net.Conn) {
		socket = &trackedConn{Conn: sock}
		c.onConnected(conn, s, socket)
	})
	var addHandlers sync.Once
	conn.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		// irc-go has installed its own callbacks by now (Connect does
		// that just before dialing), so ours can replace its nick-in-use
		// handling
		addHandlers.Do(func() { c.addHandlers(conn) })
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
		msg, detail := "Disconnected", "no socket error (ping timeout, readQ or server ERROR)"
		if err := socket.err(); err != nil {
			msg, detail = err.Error(), err.Error()
		}
		c.endAttempt(conn, s, msg, "disconnected: "+detail)
	})

	c.conn = conn
	c.socket = nil
	c.attemptDone = make(chan struct{})
	c.cancelDial = cancel
	c.requestedNick = ""
	c.regNick = config.Nick
	c.regTries = 0
	c.gaveUp = ""
	c.Registered = false
	c.Unlock()

	if c.debug > 0 {
		c.log.Printf("Connecting to %s", conn.Server)
	}

	err := conn.Connect()
	cancel()
	if err != nil {
		// a socket error (readQ, say) says more than "Disconnected by server"
		if err == ircevent.ServerDisconnected && socket != nil && socket.err() != nil {
			err = socket.err()
		}
		c.endAttempt(conn, s, err.Error(), "connection failed: "+err.Error())
		return
	}

	c.Lock()
	if c.conn == conn {
		c.finishSetup()
	}
	c.Unlock()
}

// addHandlers registers our handlers as irc-go callbacks on conn. It
// runs from conn's dial, after irc-go has installed its own callbacks:
// irc-go's 433 and 437 handlers (which try nick_0, nick_1, ... during
// registration) are removed first, so ours (onNickRefused) are the only
// ones.
func (c *IRCClient) addHandlers(conn *ircevent.Connection) {
	conn.ClearCallback("433")
	conn.ClearCallback("437")
	eachHandler(func(cmd string, h handler) {
		conn.AddCallback(cmd, func(m ircmsg.Message) {
			e := c.run(conn, h, m)
			// with c unlocked: SetNick can block on irc-go's write
			// queue, and the API needs c meanwhile
			if e.preferNick != "" {
				conn.SetNick(e.preferNick)
			}
			c.publishEffects(e)
		})
	})
}

// finishSetup runs once per connection, when Connect has returned: irc-go
// has registered, and its acked caps and ISUPPORT are final. c is locked
// and c.conn is the connection. Everything that waits for registration
// happens here together, so the API is never let in before we know
// whether the server echoes our messages.
func (c *IRCClient) finishSetup() {
	c.copyAckedCaps()
	c.setPrefix(c.isupport())
	c.Registered = true
	c.Join()
}

// copyAckedCaps copies the connection's acked caps. c is locked and
// c.conn is set. Call it only after Connect has returned: AcknowledgedCaps
// returns irc-go's own map, which the Connect goroutine writes once all
// CAP REQs are answered or at 376/422, whichever is first. A server that
// sends 001 early can make that write overlap our reading of 001, 005 or
// 376, and concurrent map access is fatal to the whole process.
func (c *IRCClient) copyAckedCaps() {
	caps := make(map[string]string)
	for k, v := range c.conn.AcknowledgedCaps() {
		caps[k] = v
	}
	c.ackedCaps = caps
}

// onConnected runs once the socket (and TLS) is up, before registration.
func (c *IRCClient) onConnected(conn *ircevent.Connection, s *sender, sock net.Conn) {
	// decide and publish under statusMu, like endAttempt: Destroy either
	// comes first (we don't register) or publishes after "Connected"
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.Lock()
	if c.conn != conn || c.quitting {
		// Destroy came first: end the attempt rather than register
		c.Unlock()
		sock.Close()
		return
	}
	c.socket = sock
	c.Connected = true
	c.StatusMessage = "Connected"
	status := c.Status()
	c.Unlock()
	c.sender.Store(s)
	if testHookConnected != nil {
		testHookConnected()
	}
	c.sendStatus(status)
}

// testHookConnected, when set by a test, runs in onConnected after it has
// decided to publish "Connected" and before it does.
var testHookConnected func()

// endAttempt ends the attempt conn belongs to, once: Connect's error and
// the disconnect callback can both report the same end. It publishes
// the disconnected status (the stop reason, if stopped) and, unless
// quitting or stopped, schedules the next attempt, then closes
// attemptDone.
// logMsg is logged at the default level.
func (c *IRCClient) endAttempt(conn *ircevent.Connection, s *sender, msg, logMsg string) {
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
	c.Registered = false
	if c.stopped != "" {
		msg, logMsg = c.stopped, "stopped: "+c.stopped
	} else if c.gaveUp != "" {
		msg, logMsg = c.gaveUp, "gave up: "+c.gaveUp
	}
	c.Unlock()

	c.log.Print(logMsg)
	if sock != nil {
		sock.Close()
	}
	c.setStatus(false, msg)

	// decide and publish under statusMu: Destroy, setting quitting, either
	// comes first (no reconnect) or publishes after this
	c.statusMu.Lock()
	c.Lock()
	if c.quitting || c.stopped != "" {
		c.Unlock()
		c.statusMu.Unlock()
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
	c.Connected = false
	c.StatusMessage = fmt.Sprintf("Reconnecting in %s", delay)
	status := c.Status()
	c.Unlock()
	c.sendStatus(status)
	c.statusMu.Unlock()
	close(done)
}

// setStatus updates the status and publishes it; c must be unlocked.
func (c *IRCClient) setStatus(connected bool, msg string) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.Lock()
	c.Connected = connected
	c.StatusMessage = msg
	status := c.Status()
	c.Unlock()
	c.sendStatus(status)
}

// sendStatus publishes status; statusMu is held and c is unlocked.
func (c *IRCClient) sendStatus(status *IRCClientStatus) {
	if c.debug > 0 {
		c.log.Print(status.Message)
	}
	Status <- status
}

// Destroy quits the connection, or stops waiting to reconnect, and
// returns once the connection has ended. A server that ignores QUIT has
// its socket closed after destroyTimeout.
func (c *IRCClient) Destroy() {
	c.statusMu.Lock()
	c.Lock()
	c.quitting = true
	if c.timer != nil {
		c.timer.Stop()
	}
	conn, done, cancel := c.conn, c.attemptDone, c.cancelDial
	c.StatusMessage = "Closing connection"
	status := c.Status()
	c.Unlock()
	c.sendStatus(status)
	c.statusMu.Unlock()

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

// Send queues line for the server and reports whether it did: it's
// dropped when not connected or when the queue is full.
func (c *IRCClient) Send(line string) bool {
	if c.debug > 1 {
		c.log.Printf("---> %s", line)
	}
	if s := c.sender.Load(); s != nil {
		return s.Send(line)
	}
	return false
}

// PortMap is the connection's entry in liercd's /portmap, for identd: the
// ident user (the configured username, or the nick without one) and the
// socket's local and remote ports. ok is false while it isn't connected.
func (c *IRCClient) PortMap() (user, local, remote string, ok bool) {
	c.RLock()
	sock := c.socket
	user = c.Config.User
	if user == "" {
		user = c.Config.Nick
	}
	c.RUnlock()
	if sock == nil {
		return "", "", "", false
	}
	_, local, _ = net.SplitHostPort(sock.LocalAddr().String())
	_, remote, _ = net.SplitHostPort(sock.RemoteAddr().String())
	return user, local, remote, true
}

// trackedConn records the first read or write error on the socket, for
// the disconnected status (irc-go doesn't expose its last error). It
// also records ircreader.ErrReadQ when readQLimit bytes arrive without a
// newline, which irc-go's reader fails on without a socket error.
type trackedConn struct {
	net.Conn
	mu      sync.Mutex
	first   error
	pending int // bytes since the last newline; only the reader touches it
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
	if i := bytes.LastIndexByte(b[:n], '\n'); i != -1 {
		t.pending = n - i - 1
	} else {
		t.pending += n
	}
	if t.pending >= readQLimit {
		t.record(ircreader.ErrReadQ)
	}
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
