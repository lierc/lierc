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

// changes is the config changes published so far.
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
