package lierc

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		ircLogOutput = io.Discard
	}
	os.Exit(m.Run())
}

// fakeServer is an IRC server on loopback that a test drives line by line.
type fakeServer struct {
	ln    net.Listener
	port  int
	conns chan *fakeConn
}

// newFakeServer listens on 127.0.0.1 and makes it the default network, so
// the dial guard lets clients connect to it.
func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, conns: make(chan *fakeConn, 16)}
	t.Setenv("DEFAULT_NETWORK_HOST", "127.0.0.1")
	t.Setenv("DEFAULT_NETWORK_PORT", strconv.Itoa(s.port))

	var mu sync.Mutex
	var all []*fakeConn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			fc := &fakeConn{Conn: conn, r: bufio.NewReader(conn)}
			mu.Lock()
			all = append(all, fc)
			mu.Unlock()
			s.conns <- fc
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, fc := range all {
			fc.close()
		}
	})
	return s
}

func (s *fakeServer) config(nick string, channels ...string) *IRCConfig {
	return &IRCConfig{Host: "127.0.0.1", Port: s.port, Nick: nick, User: nick, Channels: channels}
}

// accept returns the next connection, failing after 5s.
func (s *fakeServer) accept(t *testing.T) *fakeConn {
	t.Helper()
	select {
	case fc := <-s.conns:
		return fc
	case <-time.After(5 * time.Second):
		t.Fatal("no connection")
		return nil
	}
}

// noAccept fails if a connection arrives within d.
func (s *fakeServer) noAccept(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-s.conns:
		t.Fatal("unexpected connection")
	case <-time.After(d):
	}
}

type fakeConn struct {
	net.Conn
	r *bufio.Reader
}

// readLine reads the next line, without its line ending, answering
// irc-go's keepalive PINGs on the way (an unanswered one is fatal, and
// tests that shorten ircKeepAlive see them).
func (fc *fakeConn) readLine() (string, error) {
	for {
		line, err := fc.r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if token, ok := strings.CutPrefix(line, "PING "); ok {
			fc.send(":s PONG s " + token)
			continue
		}
		return line, nil
	}
}

// expect reads lines until one starts with prefix and returns it, failing
// after 5s.
func (fc *fakeConn) expect(t *testing.T, prefix string) string {
	t.Helper()
	return fc.expectWithin(t, prefix, 5*time.Second)
}

// expectWithin is expect, failing after d.
func (fc *fakeConn) expectWithin(t *testing.T, prefix string, d time.Duration) string {
	t.Helper()
	fc.SetReadDeadline(time.Now().Add(d))
	defer fc.SetReadDeadline(time.Time{})
	for {
		line, err := fc.readLine()
		if err != nil {
			t.Fatalf("waiting for %q: %v", prefix, err)
		}
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
}

func (fc *fakeConn) send(line string) {
	fc.Write([]byte(line + "\r\n"))
}

func (fc *fakeConn) sendf(format string, args ...interface{}) {
	fc.send(fmt.Sprintf(format, args...))
}

// register answers CAP LS, ACKs each REQ, reads NICK/USER/CAP END and
// sends 001, 005 with the isupport tokens, and 376.
func (fc *fakeConn) register(t *testing.T, nick string, isupport ...string) {
	t.Helper()
	fc.expect(t, "CAP LS")
	fc.send(":s CAP * LS :echo-message message-tags")
	fc.SetReadDeadline(time.Now().Add(5 * time.Second))
	var sawNick, sawUser bool
	for {
		line, err := fc.readLine()
		if err != nil {
			t.Fatalf("registering: %v", err)
		}
		switch {
		case strings.HasPrefix(line, "CAP REQ "):
			caps := strings.TrimPrefix(strings.TrimPrefix(line, "CAP REQ "), ":")
			fc.sendf(":s CAP * ACK :%s", caps)
		case strings.HasPrefix(line, "NICK "):
			sawNick = true
		case strings.HasPrefix(line, "USER "):
			sawUser = true
		}
		if line == "CAP END" {
			break
		}
	}
	fc.SetReadDeadline(time.Time{})
	if !sawNick || !sawUser {
		t.Fatalf("CAP END before NICK (%v) and USER (%v)", sawNick, sawUser)
	}
	fc.welcome(nick, isupport...)
}

// welcome sends 001, 005 and 376.
func (fc *fakeConn) welcome(nick string, isupport ...string) {
	fc.sendf(":s 001 %s :Welcome", nick)
	if len(isupport) > 0 {
		fc.sendf(":s 005 %s %s :are supported by this server", nick, strings.Join(isupport, " "))
	}
	fc.sendf(":s 376 %s :End of MOTD", nick)
}

func (fc *fakeConn) close() {
	fc.Conn.Close()
}

// drained is what a test's client published on the global channels.
type drained struct {
	mu       sync.Mutex
	statuses []*IRCClientStatus
	events   []*IRCClientMessage
	multi    []*IRCClientMultiMessage
	changes  []*IRCConfigChange
	cursor   int // statuses already matched by next
}

// drain collects Status, Events, Multi and ConfigChanges for the test's
// duration. Call it before creating the client, so the client is destroyed
// (a later cleanup) while the channels are still drained.
func drain(t *testing.T) *drained {
	d := &drained{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case s := <-Status:
				d.mu.Lock()
				d.statuses = append(d.statuses, s)
				d.mu.Unlock()
			case e := <-Events:
				d.mu.Lock()
				d.events = append(d.events, e)
				d.mu.Unlock()
			case m := <-Multi:
				d.mu.Lock()
				d.multi = append(d.multi, m)
				d.mu.Unlock()
			case c := <-ConfigChanges:
				d.mu.Lock()
				d.changes = append(d.changes, c)
				d.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
	})
	return d
}

// next waits for the next status after the last one matched that satisfies
// match, failing after 5s.
func (d *drained) next(t *testing.T, what string, match func(*IRCClientStatus) bool) *IRCClientStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		for i := d.cursor; i < len(d.statuses); i++ {
			if match(d.statuses[i]) {
				d.cursor = i + 1
				s := d.statuses[i]
				d.mu.Unlock()
				return s
			}
		}
		d.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no status %s; got %s", what, d.messages())
	return nil
}

// nextMessage waits for the next status with message msg.
func (d *drained) nextMessage(t *testing.T, msg string) *IRCClientStatus {
	t.Helper()
	return d.next(t, strconv.Quote(msg), func(s *IRCClientStatus) bool { return s.Message == msg })
}

func (d *drained) messages() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, s := range d.statuses {
		out = append(out, s.Message)
	}
	return out
}

// newTestClient starts a client and destroys it when the test ends.
func newTestClient(t *testing.T, config *IRCConfig) *IRCClient {
	t.Helper()
	c := NewIRCClient(config, "c1")
	t.Cleanup(c.Destroy)
	return c
}

// fastRetries shortens the reconnect backoff and Destroy's wait for the
// test's duration.
func fastRetries(t *testing.T) {
	oldRetry, oldDestroy := retryUnit, destroyTimeout
	retryUnit = 20 * time.Millisecond
	destroyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { retryUnit, destroyTimeout = oldRetry, oldDestroy })
}

// eventually polls cond for up to 5s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
