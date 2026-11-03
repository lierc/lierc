package lierc

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	old := retryUnit
	retryUnit = 15 * time.Second
	t.Cleanup(func() { retryUnit = old })
	for retries, want := range map[int]time.Duration{
		1: 15 * time.Second, 2: 30 * time.Second, 20: 300 * time.Second, 25: 300 * time.Second,
	} {
		if got := backoff(retries); got != want {
			t.Errorf("backoff(%d) = %s, want %s", retries, got, want)
		}
	}
}

func registered(c *IRCClient) bool {
	c.RLock()
	defer c.RUnlock()
	return c.Registered
}

func waitRegistered(t *testing.T, c *IRCClient) {
	t.Helper()
	eventually(t, "registration", func() bool { return registered(c) })
}

// disconnected matches a status for a lost connection: not connected, with
// an error, and not one of the messages liercd itself sets.
func disconnected(s *IRCClientStatus) bool {
	return !s.Connected && s.Message != "" && s.Message != "Connecting" &&
		s.Message != "Closing connection" && !strings.HasPrefix(s.Message, "Reconnecting in")
}

// expectNo fails if a line starting with prefix arrives within d.
func (fc *fakeConn) expectNo(t *testing.T, prefix string, d time.Duration) {
	t.Helper()
	fc.SetReadDeadline(time.Now().Add(d))
	defer fc.SetReadDeadline(time.Time{})
	for {
		line, err := fc.readLine()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return
			}
			t.Fatal(err)
		}
		if strings.HasPrefix(line, prefix) {
			t.Fatalf("unexpected %q", line)
		}
	}
}

func TestRegisterAndJoin(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me", "#a", "#b key"))

	fc := srv.accept(t)
	fc.expect(t, "CAP LS")
	fc.send(":s CAP * LS :echo-message message-tags")
	fc.send(":s CAP * ACK :echo-message")
	fc.send(":s CAP * ACK :message-tags")
	fc.expect(t, "CAP END")
	fc.send(":s 001 me :Welcome")
	fc.send(":s 005 me PREFIX=(qov)~@+ NETWORK=Test :are supported")
	// joins wait for the end of registration, when PREFIX is known
	fc.expectNo(t, "JOIN", 100*time.Millisecond)
	fc.send(":s 376 me :End of MOTD")
	fc.expect(t, "JOIN #a")
	fc.expect(t, "JOIN #b key")

	d.nextMessage(t, "Connected")
	if got := d.messages()[:2]; !reflect.DeepEqual(got, []string{"Connecting", "Connected"}) {
		t.Errorf("statuses %q", got)
	}

	fc.send(":me!u@h JOIN #a")
	fc.send(":s 353 me = #a :~alice @bob carol")
	fc.send(":s 366 me #a :End of NAMES")

	var data *IRCClientData
	eventually(t, "#a's nicks", func() bool {
		c.RLock()
		defer c.RUnlock()
		data = c.ClientData()
		for _, ch := range data.Channels {
			if ch.Name == "#a" && len(ch.Nicks) == 4 {
				return true
			}
		}
		return false
	})
	nicks := data.Channels[0].Nicks
	sort.Strings(nicks)
	if want := []string{"@bob", "carol", "me", "~alice"}; !reflect.DeepEqual(nicks, want) {
		t.Errorf("#a nicks %q, want %q", nicks, want)
	}
	found := false
	for _, tok := range data.Isupport {
		found = found || tok == "PREFIX=(qov)~@+"
	}
	if !found {
		t.Errorf("Isupport %q lacks PREFIX", data.Isupport)
	}
	caps := append([]string{}, data.CapsAcked...)
	sort.Strings(caps)
	if want := []string{"echo-message", "message-tags"}; !reflect.DeepEqual(caps, want) {
		t.Errorf("CapsAcked %q, want %q", caps, want)
	}
	if !c.CapEnabled("echo-message") || c.CapEnabled("sasl") {
		t.Errorf("CapEnabled: echo-message %v, sasl %v", c.CapEnabled("echo-message"), c.CapEnabled("sasl"))
	}
	if data.Nick != "me" || !data.Registered {
		t.Errorf("Nick %q Registered %v", data.Nick, data.Registered)
	}
}

// saslStart answers CAP with sasl, ACKs the REQs, reads AUTHENTICATE PLAIN,
// asks for the payload and checks it.
func saslStart(t *testing.T, fc *fakeConn) {
	t.Helper()
	fc.expect(t, "CAP LS")
	fc.send(":s CAP * LS :echo-message message-tags sasl")
	for {
		line := fc.expect(t, "")
		if caps, ok := strings.CutPrefix(line, "CAP REQ "); ok {
			fc.sendf(":s CAP * ACK :%s", strings.TrimPrefix(caps, ":"))
		}
		if line == "AUTHENTICATE PLAIN" {
			break
		}
	}
	fc.send("AUTHENTICATE +")
	payload := strings.TrimPrefix(fc.expect(t, "AUTHENTICATE "), "AUTHENTICATE ")
	got, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user\x00user\x00pass" {
		t.Fatalf("SASL payload %q", got)
	}
}

func TestSASL(t *testing.T) {
	config := func(srv *fakeServer) *IRCConfig {
		cfg := srv.config("user")
		cfg.Pass, cfg.SASL = "pass", true
		return cfg
	}

	t.Run("success", func(t *testing.T) {
		fastRetries(t)
		srv := newFakeServer(t)
		drain(t)
		c := newTestClient(t, config(srv))
		fc := srv.accept(t)
		saslStart(t, fc)
		fc.send(":s 903 user :SASL authentication successful")
		fc.expect(t, "CAP END")
		fc.welcome("user")
		waitRegistered(t, c)
	})

	t.Run("failure", func(t *testing.T) {
		fastRetries(t)
		srv := newFakeServer(t)
		d := drain(t)
		newTestClient(t, config(srv))
		fc := srv.accept(t)
		saslStart(t, fc)
		fc.send(":s 904 user :SASL authentication failed")
		d.nextMessage(t, "Connected")
		d.next(t, "disconnected", disconnected)
		d.nextMessage(t, "Reconnecting in 20ms")
	})
}

func TestNickInUseAtRegistration(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.expect(t, "NICK me")
	fc.send(":s 433 * me :in use")
	if got := fc.expect(t, "NICK "); got != "NICK me_0" {
		t.Fatalf("after 433: %q", got)
	}
	fc.send(":s CAP * ACK :echo-message")
	fc.send(":s CAP * ACK :message-tags")
	fc.expect(t, "CAP END")
	fc.welcome("me_0")
	waitRegistered(t, c)
	c.RLock()
	nick := c.ClientData().Nick
	c.RUnlock()
	if nick != "me_0" {
		t.Errorf("Nick %q", nick)
	}
}

func TestDisconnectReconnects(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))

	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)
	fc.close()
	d.next(t, "disconnected", disconnected)
	d.nextMessage(t, "Reconnecting in 20ms")

	fc = srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)
	// 001 reset the retries, so this is the first retry again
	fc.close()
	d.next(t, "disconnected", disconnected)
	d.nextMessage(t, "Reconnecting in 20ms")

	// without registering, the retries grow
	fc = srv.accept(t)
	fc.expect(t, "NICK")
	fc.close()
	d.nextMessage(t, "Reconnecting in 40ms")
	srv.accept(t)
}

func TestDropDuringRegistrationReconnects(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.expect(t, "NICK")
	fc.close()
	d.nextMessage(t, "Reconnecting in 20ms")
	srv.accept(t)
}

func TestDestroyWhileSleeping(t *testing.T) {
	fastRetries(t)
	retryUnit = time.Hour
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)
	fc.close()
	d.nextMessage(t, "Reconnecting in 1h0m0s")

	start := time.Now()
	c.Destroy()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("Destroy took %s", elapsed)
	}
	srv.noAccept(t, 100*time.Millisecond)
}

func TestDestroyDuringRegistration(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	srv.accept(t)
	d.nextMessage(t, "Connected")

	start := time.Now()
	c.Destroy()
	if elapsed := time.Since(start); elapsed > destroyTimeout+time.Second {
		t.Errorf("Destroy took %s", elapsed)
	}
}

func TestClientDataConcurrent(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	fc.send(":me!u@h JOIN #a")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			c.RLock()
			json.Marshal(c.ClientData())
			c.RUnlock()
		}
	}()

	for i := 0; i < 100; i++ {
		fc.sendf(":n%d!u@h JOIN #a", i)
		fc.sendf(":n%d!u@h PART #a", i)
	}
	fc.send(":s PRIVMSG me :done")
	eventually(t, "the last line", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, e := range d.events {
			if e.Message.Command == "PRIVMSG" {
				return true
			}
		}
		return false
	})
	close(stop)
	wg.Wait()
}

// Handlers run as irc-go callbacks: a QUIT from the server updates the
// channels and is published to Multi, with the channels it left, as well
// as to Events with the server's exact line.
func TestQuitThroughCallbacks(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me", "#a"))
	fc := srv.accept(t)
	fc.register(t, "me")
	fc.expect(t, "JOIN #a")
	fc.send(":me!u@h JOIN #a")
	fc.send(":s 353 me = #a :me bob")
	fc.send(":s 366 me #a :End of NAMES")
	line := "@time=2026-11-03T00:00:00.000Z :bob!b@x QUIT :gone"
	fc.send(line)

	var multi *IRCClientMultiMessage
	eventually(t, "QUIT on Multi", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, m := range d.multi {
			if m.Message.Command == "QUIT" {
				multi = m
				return true
			}
		}
		return false
	})
	if multi.Id != "c1" || !reflect.DeepEqual(multi.Channels, []string{"#a"}) || multi.Message.Prefix.Name != "bob" {
		t.Errorf("multi %+v, message %+v", multi, multi.Message)
	}
	if multi.Message.Tags["time"] != "2026-11-03T00:00:00.000Z" {
		t.Errorf("multi tags %v", multi.Message.Tags)
	}
	d.waitEvent(t, "QUIT")
	d.mu.Lock()
	for _, e := range d.events {
		if e.Message.Command == "QUIT" && e.Message.Raw != line {
			t.Errorf("Events Raw %q, want %q", e.Message.Raw, line)
		}
	}
	d.mu.Unlock()
	c.RLock()
	_, inChannel := c.Channels["#a"].Nicks["bob"]
	c.RUnlock()
	if inChannel {
		t.Error("bob still in #a")
	}
}

func TestPortMap(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)

	err, local, remote := c.PortMap()
	if err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(fc.RemoteAddr().(*net.TCPAddr).Port); local != want {
		t.Errorf("local port %s, want %s", local, want)
	}
	if want := strconv.Itoa(srv.port); remote != want {
		t.Errorf("remote port %s, want %s", remote, want)
	}
}

// fastKeepAlive makes irc-go PING (and try to regain its preferred nick)
// every 200ms for the test's duration. irc-go needs KeepAlive ≥ Timeout,
// and an unanswered PING is fatal at the next Timeout tick, which the fake
// server's readLine answers.
func fastKeepAlive(t *testing.T) {
	oldTimeout, oldKeepAlive := ircTimeout, ircKeepAlive
	ircTimeout = 100 * time.Millisecond
	ircKeepAlive = 200 * time.Millisecond
	t.Cleanup(func() { ircTimeout, ircKeepAlive = oldTimeout, oldKeepAlive })
}

// changed is the config changes published so far.
func (d *drained) changed() []*IRCConfigChange {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*IRCConfigChange{}, d.changes...)
}

// registerFallback registers as me_0, the server refusing "me" with 433.
func registerFallback(t *testing.T, fc *fakeConn) {
	t.Helper()
	fc.expect(t, "NICK me")
	fc.send(":s 433 * me :in use")
	if got := fc.expect(t, "NICK "); got != "NICK me_0" {
		t.Fatalf("after 433: %q", got)
	}
	fc.send(":s CAP * ACK :echo-message")
	fc.send(":s CAP * ACK :message-tags")
	fc.expect(t, "CAP END")
	fc.welcome("me_0")
}

func clientNick(c *IRCClient) string {
	c.RLock()
	defer c.RUnlock()
	return c.ClientData().Nick
}

func TestChangeNickNotReverted(t *testing.T) {
	fastRetries(t)
	fastKeepAlive(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)

	c.ChangeNick("new")
	if got := fc.expect(t, "NICK "); got != "NICK new" {
		t.Fatalf("sent %q", got)
	}
	fc.send(":me!u@h NICK new")
	fc.expectNo(t, "NICK me", 600*time.Millisecond)

	changes := d.changed()
	if got, want := changed(t, changes), `{"Id":"c1","Config":{"Nick":"new"}}`; got != want {
		t.Errorf("published %s, want %s", got, want)
	}
}

func TestRefusedNickNotRetried(t *testing.T) {
	fastRetries(t)
	fastKeepAlive(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)

	c.ChangeNick("taken")
	if got := fc.expect(t, "NICK "); got != "NICK taken" {
		t.Fatalf("sent %q", got)
	}
	fc.send(":s 433 me taken :in use")
	// back to the nick irc-go preferred before the request
	if got := fc.expect(t, "NICK "); got != "NICK me" {
		t.Fatalf("after 433: %q", got)
	}
	fc.expectNo(t, "NICK taken", 600*time.Millisecond)

	if got := changed(t, d.changed()); got != "" {
		t.Errorf("published %s", got)
	}
	c.RLock()
	requested := c.requestedNick
	c.RUnlock()
	if requested != "" {
		t.Errorf("requestedNick %q", requested)
	}
}

func TestServerRenameNotFought(t *testing.T) {
	fastRetries(t)
	fastKeepAlive(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)

	fc.send(":me!u@h NICK Guest1")
	fc.expectNo(t, "NICK me", 600*time.Millisecond)

	if got := changed(t, d.changed()); got != "" {
		t.Errorf("published %s", got)
	}
	if nick := clientNick(c); nick != "Guest1" {
		t.Errorf("Nick %q", nick)
	}
}

// A refused request after a services rename goes back to the rename, not
// the configured nick.
func TestRefusedNickAfterRenameNotFought(t *testing.T) {
	fastRetries(t)
	fastKeepAlive(t)
	srv := newFakeServer(t)
	drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)

	fc.send(":me!u@h NICK Guest1")
	eventually(t, "the rename", func() bool { return clientNick(c) == "Guest1" })
	c.ChangeNick("x")
	if got := fc.expect(t, "NICK x"); got != "NICK x" {
		t.Fatalf("sent %q", got)
	}
	fc.send(":s 433 Guest1 x :in use")
	fc.expectNo(t, "NICK me", 600*time.Millisecond)

	if nick := clientNick(c); nick != "Guest1" {
		t.Errorf("Nick %q", nick)
	}
}

func TestRegainConfiguredNick(t *testing.T) {
	fastRetries(t)
	fastKeepAlive(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	registerFallback(t, fc)
	waitRegistered(t, c)

	// about 3 keepalive ticks
	if got := fc.expectWithin(t, "NICK ", 700*time.Millisecond); got != "NICK me" {
		t.Fatalf("sent %q", got)
	}
	fc.send(":me_0!u@h NICK me")
	eventually(t, "the nick back", func() bool { return clientNick(c) == "me" })
	fc.expectNo(t, "NICK", 300*time.Millisecond)

	if got := changed(t, d.changed()); got != "" {
		t.Errorf("published %s", got)
	}
}

func TestChangeNickWhileDisconnected(t *testing.T) {
	fastRetries(t)
	retryUnit = time.Hour
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)
	fc.close()
	d.nextMessage(t, "Reconnecting in 1h0m0s")

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.ChangeNick("new")
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ChangeNick blocked")
	}
}

// Each attempt starts as the configured nick: the status doesn't show the
// last session's fallback nick before the next 001.
func TestNickResetOnReconnect(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	registerFallback(t, fc)
	waitRegistered(t, c)
	if nick := clientNick(c); nick != "me_0" {
		t.Fatalf("Nick %q", nick)
	}

	fc.close()
	d.nextMessage(t, "Reconnecting in 20ms")
	fc = srv.accept(t)
	fc.expect(t, "NICK me")
	if nick := clientNick(c); nick != "me" {
		t.Errorf("before 001: Nick %q, want me", nick)
	}
	fc.send(":s CAP * ACK :echo-message")
	fc.send(":s CAP * ACK :message-tags")
	fc.expect(t, "CAP END")
	fc.welcome("me")
	waitRegistered(t, c)
}

// The acked caps are known from 001, before the end of registration, and
// kept while reconnecting, so the manager doesn't echo messages itself;
// a message sent while disconnected isn't queued, so it isn't echoed.
func TestAckedCapsKept(t *testing.T) {
	fastRetries(t)
	retryUnit = time.Hour
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.expect(t, "CAP LS")
	fc.send(":s CAP * LS :echo-message message-tags")
	fc.send(":s CAP * ACK :echo-message")
	fc.send(":s CAP * ACK :message-tags")
	fc.expect(t, "CAP END")
	fc.send(":s 001 me :Welcome")
	d.waitEvent(t, "001")
	// irc-go's acked caps are only read once Connect has returned, at the
	// end of MOTD, so on a first connection they're unknown until then
	if c.CapEnabled("echo-message") {
		t.Error("echo-message copied before Connect returned")
	}
	fc.send(":s 376 me :End of MOTD")
	waitRegistered(t, c)
	if !c.CapEnabled("echo-message") {
		t.Error("echo-message not copied when registered")
	}

	fc.close()
	d.nextMessage(t, "Reconnecting in 1h0m0s")
	if !c.CapEnabled("echo-message") {
		t.Error("echo-message forgotten while disconnected")
	}
	if c.Send("PRIVMSG #a :hi") {
		t.Error("Send while disconnected was queued")
	}
}

// A server that welcomes us in the same write as its CAP ACK, before our
// CAP END, makes irc-go write its acked caps while we handle 001, 005 and
// 376. We must not read them until Connect returns: under -race this test
// fails if we do, and in production concurrent map access kills liercd.
func TestCapsAckedWithEarlyWelcome(t *testing.T) {
	fastRetries(t)
	drain(t)
	for i := 0; i < 20; i++ {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			srv := newFakeServer(t)
			c := newTestClient(t, srv.config("me"))
			fc := srv.accept(t)
			fc.expect(t, "CAP LS")
			fc.send(":s CAP * LS :echo-message message-tags")
			fc.expect(t, "CAP REQ")
			fc.Write([]byte(":s CAP * ACK :echo-message message-tags\r\n" +
				":s 001 me :Welcome\r\n" +
				":s 005 me PREFIX=(ov)@+ :are supported\r\n" +
				":s 376 me :End of MOTD\r\n"))
			eventually(t, "echo-message acked", func() bool { return c.CapEnabled("echo-message") })
		})
	}
}

// waitEvent waits for a line with command cmd to have been published.
func (d *drained) waitEvent(t *testing.T, cmd string) {
	t.Helper()
	eventually(t, cmd+" published", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, e := range d.events {
			if e.Message.Command == cmd {
				return true
			}
		}
		return false
	})
}

// Setup finishes when irc-go has registered, at the end of MOTD, not at
// 001: only then is the client Registered, its PREFIX read and its
// channels joined, once, however many MOTDs follow.
func TestRegisteredAfterSetup(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me", "#a"))
	fc := srv.accept(t)
	fc.expect(t, "CAP LS")
	fc.send(":s CAP * LS :echo-message message-tags")
	fc.send(":s CAP * ACK :echo-message")
	fc.send(":s CAP * ACK :message-tags")
	fc.expect(t, "CAP END")
	fc.send(":s 001 me :Welcome")
	fc.send(":s 005 me PREFIX=(qov)~@+ :are supported")
	d.waitEvent(t, "005")
	if c.IsRegistered() {
		t.Error("Registered at 001")
	}
	fc.expectNo(t, "JOIN", 100*time.Millisecond)

	fc.send(":s 376 me :End of MOTD")
	fc.expect(t, "JOIN #a")
	waitRegistered(t, c)
	c.RLock()
	prefix := c.prefix
	c.RUnlock()
	if want := [][]byte{{'~', 'q'}, {'@', 'o'}, {'+', 'v'}}; !reflect.DeepEqual(prefix, want) {
		t.Errorf("prefix %q, want %q", prefix, want)
	}

	// a later MOTD doesn't join again
	fc.send(":s 422 me :No MOTD")
	fc.expectNo(t, "JOIN", 200*time.Millisecond)
}

// Only a nick change we asked for is saved: not a different nick than the
// one requested, nor someone else taking the requested nick; the match is
// case-insensitive.
func TestNickChangeOnlySavesRequested(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)

	c.ChangeNick("wanted")
	fc.expect(t, "NICK wanted")
	fc.send(":me!u@h NICK other")
	eventually(t, "nick other", func() bool { return c.CurrentNick() == "other" })

	c.ChangeNick("Mixed")
	fc.expect(t, "NICK Mixed")
	fc.send(":other!u@h NICK mixed")
	eventually(t, "nick saved", func() bool { return len(d.changed()) > 0 })
	if got, want := changed(t, d.changed()), `{"Id":"c1","Config":{"Nick":"mixed"}}`; got != want {
		t.Errorf("published %s, want %s", got, want)
	}

	c.ChangeNick("bob2")
	fc.expect(t, "NICK bob2")
	fc.send(":bob!u@h NICK bob2")
	fc.send(":s 422 me :No MOTD") // a marker after bob's NICK
	d.waitEvent(t, "422")
	if n := len(d.changed()); n != 1 {
		t.Errorf("%d config changes, want 1", n)
	}
	if nick := c.CurrentNick(); nick != "mixed" {
		t.Errorf("nick %q after someone else's change", nick)
	}
}

// An over-long line drops the connection, and the status says why.
func TestReadQExceeded(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)
	fc.Write([]byte(":s PRIVMSG me :" + strings.Repeat("x", 9000)))
	s := d.next(t, "disconnected", disconnected)
	if !strings.Contains(s.Message, "readQ") {
		t.Errorf("status %q", s.Message)
	}
}

// A nick request pending when the connection drops doesn't carry over:
// a refusal on the next connection leaves the nick alone.
func TestNickRequestClearedOnReconnect(t *testing.T) {
	fastRetries(t)
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)
	c.ChangeNick("taken")
	fc.expect(t, "NICK taken")
	fc.close()
	d.nextMessage(t, "Reconnecting in 20ms")

	fc = srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)
	fc.send(":s 433 me taken :in use")
	fc.expectNo(t, "NICK", 300*time.Millisecond)
	c.RLock()
	requested, before := c.requestedNick, c.nickBeforeRequest
	c.RUnlock()
	if requested != "" || before != "" {
		t.Errorf("requestedNick %q nickBeforeRequest %q", requested, before)
	}
}
