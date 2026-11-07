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
	"os"
	"strconv"
	"strings"
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
	urls := portmapURLs(os.Getenv("LIERCD_HOST"), os.Getenv("LIERCD_NEXT_HOST"))

	ln, err := net.Listen("tcp", ":113")
	if err != nil {
		log.Panic(err)
	}

	log.Printf("listening on port 113, portmaps %s", strings.Join(urls, " "))

	log.Fatal(serveIdent(ln, func(local, remote string) (string, error) {
		return lookup(urls, local, remote)
	}, maxConns))
}

// serveIdent answers ident queries on ln, handling at most max
// connections at once; one past that is closed straight away. It
// returns when ln is closed.
func serveIdent(ln net.Listener, find func(local, remote string) (string, error), max int) error {
	slots := make(chan struct{}, max)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			log.Printf("Error accepting: %v", err)
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
// since the connection may be the unreadable instance's.
func lookup(urls []string, local, remote string) (string, error) {
	var failed error
	for _, url := range urls {
		ports, err := fetchPortmap(url)
		if err != nil {
			log.Printf("Error reading portmap %s: %v", url, err)
			failed = err
			continue
		}
		for _, mapping := range ports {
			if len(mapping) >= 3 && mapping[1] == local && mapping[2] == remote {
				return mapping[0], nil
			}
		}
	}
	if failed != nil {
		return "", failed
	}
	return "", errNoUser
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
func handle(conn net.Conn, find func(local, remote string) (string, error)) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(connTimeout))
	reader := bufio.NewReaderSize(conn, maxLineLen)

	for range maxQueries {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				log.Printf("%s: line too long", conn.RemoteAddr())
			} else if err != io.EOF {
				log.Printf("%s: %v", conn.RemoteAddr(), err)
			}
			return
		}
		query := strings.TrimRight(string(line), "\r\n")
		out := reply(query, find)
		log.Printf("%s: %q: %s", conn.RemoteAddr(), truncate(query, 100), strings.TrimSuffix(out, "\n"))
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

// reply answers one ident request line ("local , remote") using find
// to look up the connection's user.
func reply(line string, find func(local, remote string) (string, error)) string {
	parts := strings.SplitN(line, ",", 2)
	if len(parts) != 2 {
		return "0, 0 : ERROR : INVALID-PORT\n"
	}
	local, okLocal := parsePort(parts[0])
	remote, okRemote := parsePort(parts[1])
	if !okLocal || !okRemote {
		return "0, 0 : ERROR : INVALID-PORT\n"
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
