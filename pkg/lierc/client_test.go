package lierc

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// A server that, like a slow one, never closes the connection after QUIT,
// so Destroy's 2s timer closes it instead.
func ignoreQuitServer(t *testing.T) (port int, quit <-chan struct{}) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	got := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "QUIT") {
				close(got)
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}

func TestDestroyWhenServerIgnoresQuit(t *testing.T) {
	port, quit := ignoreQuitServer(t)

	connected := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case s := <-Status:
				if s.Connected {
					select {
					case connected <- struct{}{}:
					default:
					}
				}
			case <-Events:
			case <-Multi:
			case <-stop:
				return
			}
		}
	}()

	c := NewIRCClient(&IRCConfig{Host: "127.0.0.1", Port: port, Nick: "tester", User: "tester"}, "c1")
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("never connected")
	}

	c.Destroy()
	<-quit

	// the disconnect from Destroy's timer closing the connection reaches
	// Event after Destroy returns
	time.Sleep(200 * time.Millisecond)
}
