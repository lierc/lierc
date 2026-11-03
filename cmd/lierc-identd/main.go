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
	"strings"
	"time"
)

// Ident clients give up after about 10-30 seconds; don't let a hung
// liercd hold a reply past that.
var httpClient = &http.Client{Timeout: 5 * time.Second}

func main() {
	urls := portmapURLs(os.Getenv("LIERCD_HOST"), os.Getenv("LIERCD_NEXT_HOST"))

	ln, err := net.Listen("tcp", ":113")
	if err != nil {
		log.Panic(err)
	}

	log.Printf("listening on port 113, portmaps %s", strings.Join(urls, " "))

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Error accepting: %v", err)
			continue
		}
		go handleConnection(conn, urls)
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

func handleConnection(conn net.Conn, urls []string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadString('\n')

		if err == io.EOF {
			return
		}

		if err != nil {
			log.Printf("Failed to read line: %v", err)
			return
		}

		line = strings.TrimSuffix(line, "\r\n")
		log.Printf("%s", line)

		out := reply(line, func(local, remote string) (string, error) {
			return lookup(urls, local, remote)
		})
		log.Print(out)
		conn.Write([]byte(out))
	}
}

// reply answers one ident request line ("local , remote") using find
// to look up the connection's user.
func reply(line string, find func(local, remote string) (string, error)) string {
	parts := strings.SplitN(line, ",", 2)
	if len(parts) != 2 {
		return fmt.Sprintf("%s : ERROR : INVALID-PORT\n", line)
	}

	local := strings.TrimSpace(parts[0])
	remote := strings.TrimSpace(parts[1])

	user, err := find(local, remote)
	switch {
	case err == nil:
		return fmt.Sprintf("%s, %s : USERID : UNIX : %s\n", local, remote, user)
	case errors.Is(err, errNoUser):
		return fmt.Sprintf("%s, %s : ERROR : NO-USER\n", local, remote)
	default:
		return fmt.Sprintf("%s, %s : ERROR : UNKNOWN-ERROR\n", local, remote)
	}
}
