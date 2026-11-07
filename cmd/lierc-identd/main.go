package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// Ident clients give up after about 10-30 seconds; don't let a hung
// liercd hold a reply past that.
var httpClient = &http.Client{Timeout: 5 * time.Second}

// Port 113 is open to the internet, for the IRC servers liercd
// connects to. These keep what anyone else can make it do small.
var connTimeout = 30 * time.Second // a whole connection, however busy

const (
	maxConns   = 200  // connections handled at once; more are closed
	maxQueries = 10   // per connection; a server asks once
	maxLineLen = 1000 // a query line, with its line ending
	maxUserLen = 64   // characters of the user id sent back
)

func main() {
	log.SetFlags(0) // journald adds the time
	urls := portmapURLs(os.Getenv("LIERCD_HOST"), os.Getenv("LIERCD_NEXT_HOST"))

	ln, err := net.Listen("tcp", ":113")
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("listening on port 113, portmaps %s", strings.Join(urls, " "))

	log.Fatal(serveIdent(ln, func(asker netip.Addr, local, remote string) (string, error) {
		return lookup(urls, asker, local, remote)
	}, maxConns))
}

// lookupFunc finds the user of the connection from local to remote,
// for a query from asker.
type lookupFunc func(asker netip.Addr, local, remote string) (string, error)

// serveIdent answers ident queries on ln, handling at most max
// connections at once; one past that is closed straight away. It
// returns when ln is closed.
func serveIdent(ln net.Listener, find lookupFunc, max int) error {
	slots := make(chan struct{}, max)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			log.Printf("accepting: %v", err)
			time.Sleep(100 * time.Millisecond) // out of fds: don't spin
			continue
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				handle(conn, find)
			}()
		default:
			conn.Close()
		}
	}
}

// portmapURL gives a liercd's portmap URL. host may carry a port;
// without one it's liercd's default, 5005.
func portmapURL(host string) string {
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "5005")
	}
	return "http://" + host + "/portmap"
}

// portmapURLs gives the portmap of each liercd instance: main, then
// next when it has a host.
func portmapURLs(mainHost, nextHost string) []string {
	urls := []string{portmapURL(mainHost)}
	if nextHost != "" {
		urls = append(urls, portmapURL(nextHost))
	}
	return urls
}

var errNoUser = errors.New("no user")

// lookup finds the user owning the connection from local to remote
// in any of the portmaps. It returns errNoUser only when every
// portmap was read; if one failed, it returns that failure instead,
// since the connection may be the unreadable instance's. One with
// nothing listening isn't running, so has no connections, and counts
// as read.
//
// Only the server at the other end may ask (RFC 1413), so an entry
// with the server's ip matches only a query from that ip. An older
// liercd's entries have no ip and match on ports alone.
func lookup(urls []string, asker netip.Addr, local, remote string) (string, error) {
	var failed error
	for _, url := range urls {
		ports, err := fetchPortmap(url)
		noteFetch(url, err)
		if err != nil {
			if !errors.Is(err, syscall.ECONNREFUSED) {
				failed = err
			}
			continue
		}
		for _, mapping := range ports {
			if len(mapping) < 3 || mapping[1] != local || mapping[2] != remote {
				continue
			}
			if len(mapping) < 4 {
				return mapping[0], nil
			}
			if ip, err := netip.ParseAddr(mapping[3]); err == nil && ip.Unmap() == asker.Unmap() {
				return mapping[0], nil
			}
			// logged: a network whose ident check comes from
			// another address would show up here
			log.Printf("%s: asked for %s, %s, which is to %s", asker, local, remote, mapping[3])
		}
	}
	if failed != nil {
		return "", failed
	}
	return "", errNoUser
}

// down holds the portmaps that failed their last read.
var down = struct {
	sync.Mutex
	urls map[string]bool
}{urls: map[string]bool{}}

// noteFetch logs a portmap going unreachable or coming back, rather
// than every failed read: with an instance stopped, every query would
// log one.
func noteFetch(url string, err error) {
	down.Lock()
	defer down.Unlock()
	switch {
	case err != nil && !down.urls[url]:
		down.urls[url] = true
		log.Printf("portmap %s unreachable: %v", url, err)
	case err == nil && down.urls[url]:
		delete(down.urls, url)
		log.Printf("portmap %s is back", url)
	}
}

func fetchPortmap(url string) ([][]string, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	var ports [][]string
	if err := json.NewDecoder(resp.Body).Decode(&ports); err != nil {
		return nil, err
	}
	return ports, nil
}

// handle answers the queries on conn, one per line, until the client
// closes it, it has asked maxQueries, it sends a line over maxLineLen,
// or connTimeout passes.
func handle(conn net.Conn, lookup lookupFunc) {
	defer conn.Close()
	// not a TCP peer (in tests): the zero Addr, which no entry's ip is
	var asker netip.Addr
	if a, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		asker = a.AddrPort().Addr().Unmap()
	}
	find := func(local, remote string) (string, error) {
		return lookup(asker, local, remote)
	}
	from := conn.RemoteAddr().String()
	if asker.IsValid() {
		from = asker.String()
	}
	conn.SetDeadline(time.Now().Add(connTimeout))
	reader := bufio.NewReaderSize(conn, maxLineLen)

	for range maxQueries {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			switch {
			case errors.Is(err, bufio.ErrBufferFull):
				log.Printf("%s: line too long", from)
			case errors.Is(err, os.ErrDeadlineExceeded):
				log.Printf("%s: timed out", from)
			case err != io.EOF:
				log.Printf("%s: %v", from, err)
			}
			return
		}
		query := strings.TrimRight(string(line), "\r\n")
		out := reply(query, find)
		// the reply names the ports; an invalid query's doesn't, so
		// show what was sent
		if out == invalidReply {
			log.Printf("%s: %q : ERROR : INVALID-PORT", from, truncate(query, 100))
		} else {
			log.Printf("%s: %s", from, strings.TrimSuffix(out, "\n"))
		}
		if _, err := io.WriteString(conn, out); err != nil {
			return
		}
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

const invalidReply = "0, 0 : ERROR : INVALID-PORT\n"

// reply answers one ident request line ("local , remote") using find
// to look up the connection's user.
func reply(line string, find func(local, remote string) (string, error)) string {
	parts := strings.SplitN(line, ",", 2)
	if len(parts) != 2 {
		return invalidReply
	}
	local, okLocal := parsePort(parts[0])
	remote, okRemote := parsePort(parts[1])
	if !okLocal || !okRemote {
		return invalidReply
	}

	user, err := find(local, remote)
	if err == nil {
		if user = cleanUser(user); user == "" {
			err = errNoUser
		}
	}
	switch {
	case err == nil:
		return fmt.Sprintf("%s, %s : USERID : UNIX : %s\n", local, remote, user)
	case errors.Is(err, errNoUser):
		return fmt.Sprintf("%s, %s : ERROR : NO-USER\n", local, remote)
	default:
		return fmt.Sprintf("%s, %s : ERROR : UNKNOWN-ERROR\n", local, remote)
	}
}

// parsePort reads a port number, 1 to 65535, written as digits with
// spaces around, and gives it back plainly written, as the portmap
// has it.
func parsePort(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return "", false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return strconv.Itoa(n), true
}

// cleanUser makes a user id safe to send: the user sets it, and a line
// break or colon in it would change the reply. It keeps printable
// characters other than spaces and colons, up to maxUserLen of them.
func cleanUser(user string) string {
	var b strings.Builder
	n := 0
	for _, r := range user {
		if n == maxUserLen {
			break
		}
		if !unicode.IsPrint(r) || unicode.IsSpace(r) || r == ':' {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
