package main

import (
	"bufio"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPortmapURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1":      "http://127.0.0.1:5005/portmap",
		"127.0.0.1:5008": "http://127.0.0.1:5008/portmap",
		"liercd":         "http://liercd:5005/portmap",
		"liercd-next:80": "http://liercd-next:80/portmap",
	}
	for host, want := range cases {
		if got := portmapURL(host); got != want {
			t.Errorf("portmapURL(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestPortmapURLs(t *testing.T) {
	if got, want := portmapURLs("a", ""), []string{"http://a:5005/portmap"}; !reflect.DeepEqual(got, want) {
		t.Errorf("without next: %v, want %v", got, want)
	}
	got := portmapURLs("a", "127.0.0.1:5008")
	want := []string{"http://a:5005/portmap", "http://127.0.0.1:5008/portmap"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with next: %v, want %v", got, want)
	}
}

func serve(t *testing.T, body string, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/portmap" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/portmap"
}

func deadURL(t *testing.T) string {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/portmap"
	srv.Close()
	return url
}

var asker = netip.MustParseAddr("192.0.2.1")

func TestLookup(t *testing.T) {
	mainURL := serve(t, `[["alice","40000","6667"]]`, http.StatusOK)
	next := serve(t, `[["bob","40001","6697"]]`, http.StatusOK)
	urls := []string{mainURL, next}

	if u, err := lookup(urls, asker, "40000", "6667"); err != nil || u != "alice" {
		t.Errorf("main: %q, %v", u, err)
	}
	if u, err := lookup(urls, asker, "40001", "6697"); err != nil || u != "bob" {
		t.Errorf("next: %q, %v", u, err)
	}
	if _, err := lookup(urls, asker, "40002", "6667"); !errors.Is(err, errNoUser) {
		t.Errorf("unknown connection: %v, want errNoUser", err)
	}
	// Ports must match as a pair.
	if _, err := lookup(urls, asker, "40000", "6697"); !errors.Is(err, errNoUser) {
		t.Errorf("mismatched pair: %v, want errNoUser", err)
	}
}

// An entry with the server's ip is only given to that server.
func TestLookupChecksAsker(t *testing.T) {
	url := serve(t, `[["alice","40000","6697","192.0.2.1"],["bob","40001","6697","2001:db8::1"]]`, http.StatusOK)
	urls := []string{url}

	if u, err := lookup(urls, asker, "40000", "6697"); err != nil || u != "alice" {
		t.Errorf("its server: %q, %v", u, err)
	}
	if _, err := lookup(urls, netip.MustParseAddr("198.51.100.7"), "40000", "6697"); !errors.Is(err, errNoUser) {
		t.Errorf("another host: %v, want errNoUser", err)
	}
	// an IPv4 peer on a dual-stack socket
	if u, err := lookup(urls, netip.MustParseAddr("::ffff:192.0.2.1"), "40000", "6697"); err != nil || u != "alice" {
		t.Errorf("mapped v4: %q, %v", u, err)
	}
	if u, err := lookup(urls, netip.MustParseAddr("2001:db8::1"), "40001", "6697"); err != nil || u != "bob" {
		t.Errorf("v6: %q, %v", u, err)
	}
	// unknown asker (not a TCP peer)
	if _, err := lookup(urls, netip.Addr{}, "40000", "6697"); !errors.Is(err, errNoUser) {
		t.Errorf("no asker: %v, want errNoUser", err)
	}
}

// The same local port can be in use to two servers; each gets its own.
func TestLookupSamePortsTwoServers(t *testing.T) {
	url := serve(t, `[["alice","40000","6697","198.51.100.7"],["bob","40000","6697","192.0.2.1"]]`, http.StatusOK)
	if u, err := lookup([]string{url}, asker, "40000", "6697"); err != nil || u != "bob" {
		t.Errorf("%q, %v, want bob", u, err)
	}
}

// An older liercd's entries have no ip: they match on ports alone.
func TestLookupOldLiercd(t *testing.T) {
	old := serve(t, `[["alice","40000","6667"]]`, http.StatusOK)
	if u, err := lookup([]string{old}, netip.MustParseAddr("198.51.100.7"), "40000", "6667"); err != nil || u != "alice" {
		t.Errorf("%q, %v", u, err)
	}
}

// An ip that doesn't parse matches nobody.
func TestLookupBadIP(t *testing.T) {
	url := serve(t, `[["alice","40000","6667","nonsense"],["bob","40001","6667",""]]`, http.StatusOK)
	for _, local := range []string{"40000", "40001"} {
		if _, err := lookup([]string{url}, asker, local, "6667"); !errors.Is(err, errNoUser) {
			t.Errorf("%s: %v, want errNoUser", local, err)
		}
	}
}

func TestLookupWithAnInstanceDown(t *testing.T) {
	next := serve(t, `[["bob","40001","6697"]]`, http.StatusOK)
	broken := serve(t, `[]`, http.StatusInternalServerError)

	// Found on the instance that answered.
	if u, err := lookup([]string{broken, next}, asker, "40001", "6697"); err != nil || u != "bob" {
		t.Errorf("found on next: %q, %v", u, err)
	}
	// Not found, and one instance answered badly: it may be that
	// one's, so not NO-USER.
	if _, err := lookup([]string{broken, next}, asker, "40000", "6667"); err == nil || errors.Is(err, errNoUser) {
		t.Errorf("missing with main broken: %v, want a non-NO-USER error", err)
	}
}

// Nothing listening means that liercd isn't running, so it has no
// connections: a miss is NO-USER.
func TestLookupWithAnInstanceStopped(t *testing.T) {
	next := serve(t, `[["bob","40001","6697"]]`, http.StatusOK)
	urls := []string{deadURL(t), next}

	if u, err := lookup(urls, asker, "40001", "6697"); err != nil || u != "bob" {
		t.Errorf("found on next: %q, %v", u, err)
	}
	if _, err := lookup(urls, asker, "40000", "6667"); !errors.Is(err, errNoUser) {
		t.Errorf("missing with main stopped: %v, want errNoUser", err)
	}
	if _, err := lookup([]string{deadURL(t)}, asker, "40000", "6667"); !errors.Is(err, errNoUser) {
		t.Errorf("only instance stopped: %v, want errNoUser", err)
	}
}

func TestLookupBadResponses(t *testing.T) {
	for name, url := range map[string]string{
		"status": serve(t, `[]`, http.StatusInternalServerError),
		"json":   serve(t, `not json`, http.StatusOK),
	} {
		if _, err := lookup([]string{url}, asker, "1", "2"); err == nil || errors.Is(err, errNoUser) {
			t.Errorf("%s: %v, want a non-NO-USER error", name, err)
		}
	}
}

func TestReply(t *testing.T) {
	find := func(user string, err error) func(string, string) (string, error) {
		return func(local, remote string) (string, error) {
			if local != "40000" || remote != "6667" {
				t.Errorf("find(%q, %q)", local, remote)
			}
			return user, err
		}
	}
	cases := []struct {
		name string
		user string
		err  error
		want string
	}{
		{"found", "alice", nil, "40000, 6667 : USERID : UNIX : alice\n"},
		{"no user", "", errNoUser, "40000, 6667 : ERROR : NO-USER\n"},
		{"failure", "", errors.New("down"), "40000, 6667 : ERROR : UNKNOWN-ERROR\n"},
	}
	for _, c := range cases {
		if got := reply("40000 , 6667", find(c.user, c.err)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

}

// Bad ports get a fixed reply that doesn't echo the line, and find
// isn't called.
func TestReplyInvalidPorts(t *testing.T) {
	never := func(local, remote string) (string, error) {
		t.Errorf("find(%q, %q) called", local, remote)
		return "", nil
	}
	for _, line := range []string{
		"garbage", "", ",", "40000,", ",6667", "abc, 6667", "40000, x",
		"0, 6667", "40000, 65536", "-1, 6667", "40000, 6667, 1",
		"40000 6667", "1e3, 6667", "<script>, 6667",
	} {
		if got, want := reply(line, never), "0, 0 : ERROR : INVALID-PORT\n"; got != want {
			t.Errorf("reply(%q) = %q, want %q", line, got, want)
		}
	}
}

// Ports are passed on, and echoed, in their plain form.
func TestReplyNormalisesPorts(t *testing.T) {
	var gotLocal, gotRemote string
	find := func(local, remote string) (string, error) {
		gotLocal, gotRemote = local, remote
		return "alice", nil
	}
	if got, want := reply(" 040000 ,\t6667 ", find), "40000, 6667 : USERID : UNIX : alice\n"; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
	if gotLocal != "40000" || gotRemote != "6667" {
		t.Errorf("find(%q, %q)", gotLocal, gotRemote)
	}
}

func TestReplyCleansUser(t *testing.T) {
	cases := map[string]string{
		"alice":                    "alice",
		"al\r\nice":                "alice",
		"a : USERID : UNIX : root": "aUSERIDUNIXroot",
		"al\x00i\tce\x7f":          "alice",
		"ålice":                    "ålice",
		strings.Repeat("x", 100):   strings.Repeat("x", maxUserLen),
	}
	for user, want := range cases {
		find := func(local, remote string) (string, error) { return user, nil }
		if got := reply("1, 2", find); got != "1, 2 : USERID : UNIX : "+want+"\n" {
			t.Errorf("user %q: %q, want %q", user, got, want)
		}
	}
	// nothing left: no user
	find := func(local, remote string) (string, error) { return "\r\n : ", nil }
	if got, want := reply("1, 2", find), "1, 2 : ERROR : NO-USER\n"; got != want {
		t.Errorf("empty user: %q, want %q", got, want)
	}
}

func alice(asker netip.Addr, local, remote string) (string, error) { return "alice", nil }

// session runs handle on one end of a pipe and returns the other.
func session(t *testing.T, find lookupFunc) (net.Conn, chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		handle(server, find)
		close(done)
	}()
	t.Cleanup(func() { client.Close() })
	return client, done
}

func waitDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle didn't return")
	}
}

func TestHandleAnswersLines(t *testing.T) {
	c, _ := session(t, alice)
	r := bufio.NewReader(c)
	for _, line := range []string{"1, 2\r\n", "3 , 4\n"} {
		io.WriteString(c, line)
		got, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(got, ": USERID : UNIX : alice\n") {
			t.Errorf("%q: %q", line, got)
		}
	}
}

// A client that never sends anything is dropped.
func TestHandleIdleTimeout(t *testing.T) {
	defer func(d time.Duration) { connTimeout = d }(connTimeout)
	connTimeout = 50 * time.Millisecond
	_, done := session(t, alice)
	waitDone(t, done)
}

// An endless line is cut off without reading it all.
func TestHandleLongLine(t *testing.T) {
	c, done := session(t, alice)
	go func() {
		c.Write([]byte(strings.Repeat("1", maxLineLen+1)))
	}()
	waitDone(t, done)
}

func TestHandleQueryLimit(t *testing.T) {
	c, done := session(t, alice)
	r := bufio.NewReader(c)
	go func() {
		for i := 0; i < maxQueries+1; i++ {
			if _, err := io.WriteString(c, "1, 2\n"); err != nil {
				return
			}
		}
	}()
	answers := 0
	for {
		if _, err := r.ReadString('\n'); err != nil {
			break
		}
		answers++
	}
	waitDone(t, done)
	if answers != maxQueries {
		t.Errorf("%d answers, want %d", answers, maxQueries)
	}
}

// Past the connection limit, a new connection is closed unanswered.
func TestServeConnectionLimit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serveIdent(ln, alice, 1)

	first, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// make sure the first is being handled
	io.WriteString(first, "1, 2\n")
	if _, err := bufio.NewReader(first).ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	second, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(second, "1, 2\n")
	if got, err := bufio.NewReader(second).ReadString('\n'); err == nil {
		t.Errorf("second connection answered: %q", got)
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Error("second connection left open")
	}

	// once the first goes, there's room again
	first.Close()
	time.Sleep(50 * time.Millisecond)
	third, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	third.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(third, "1, 2\n")
	if _, err := bufio.NewReader(third).ReadString('\n'); err != nil {
		t.Errorf("third connection: %v", err)
	}
}

// The lookup is given the address the query came from.
func TestServePassesAsker(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan netip.Addr, 1)
	go serveIdent(ln, func(a netip.Addr, local, remote string) (string, error) {
		got <- a
		return "alice", nil
	}, 10)

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "1, 2\n")
	select {
	case a := <-got:
		if a != netip.MustParseAddr("127.0.0.1") {
			t.Errorf("asker %v, want 127.0.0.1", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no lookup")
	}
	// Read the reply before closing: closing with it unread resets the
	// connection, and the handler's "connection reset" log line lands in
	// whichever test is capturing the log next.
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
		t.Errorf("reply: %v", err)
	}
}

type logBuf struct {
	sync.Mutex
	b strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.Lock()
	defer l.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.Lock()
	defer l.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *logBuf {
	t.Helper()
	buf := &logBuf{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

// An instance that can't be read is logged when it goes down and when
// it's back, not on every lookup.
func TestLookupLogsInstanceStateChanges(t *testing.T) {
	logs := captureLog(t)
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	urls := []string{srv.URL + "/portmap"}

	lookup(urls, asker, "1", "2")
	if s := logs.String(); s != "" {
		t.Errorf("logged while up: %q", s)
	}
	down.Store(true)
	for range 3 {
		lookup(urls, asker, "1", "2")
	}
	if n := strings.Count(logs.String(), "unreachable"); n != 1 {
		t.Errorf("%d unreachable lines, want 1: %q", n, logs.String())
	}
	down.Store(false)
	for range 3 {
		lookup(urls, asker, "1", "2")
	}
	if n := strings.Count(logs.String(), "is back"); n != 1 {
		t.Errorf("%d back lines, want 1: %q", n, logs.String())
	}
}

// Each query is one line: who asked and the reply, or the query
// itself when it was invalid.
func TestHandleLogsQueries(t *testing.T) {
	logs := captureLog(t)
	c, done := session(t, alice)
	r := bufio.NewReader(c)
	for _, q := range []string{"1, 2\n", "nonsense\n"} {
		io.WriteString(c, q)
		r.ReadString('\n')
	}
	c.Close()
	waitDone(t, done)
	got := logs.String()
	for _, want := range []string{
		"1, 2 : USERID : UNIX : alice\n",
		`"nonsense" : ERROR : INVALID-PORT` + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("want 2 lines:\n%s", got)
	}
}
