package lierc

import (
	"net"
	"strings"
	"testing"
)

func TestExempt(t *testing.T) {
	t.Setenv("DEFAULT_NETWORK_HOST", "IRC.relaychat.party")
	t.Setenv("DEFAULT_NETWORK_PORT", "")
	if !Exempt(&IRCConfig{Host: "irc.relaychat.party", Port: 6667}) {
		t.Error("default host on the default port (6667) should be exempt")
	}
	if Exempt(&IRCConfig{Host: "irc.relaychat.party", Port: 5005}) {
		t.Error("default host on another port should not be exempt")
	}
	if Exempt(&IRCConfig{Host: "other.example", Port: 6667}) {
		t.Error("another host should not be exempt")
	}

	t.Setenv("DEFAULT_NETWORK_PORT", "6697")
	if !Exempt(&IRCConfig{Host: "irc.relaychat.party", Port: 6697}) {
		t.Error("DEFAULT_NETWORK_PORT should be respected")
	}
	if Exempt(&IRCConfig{Host: "irc.relaychat.party", Port: 6667}) {
		t.Error("6667 is no longer the default port")
	}

	t.Setenv("DEFAULT_NETWORK_HOST", "")
	if Exempt(&IRCConfig{Host: "", Port: 6697}) {
		t.Error("nothing is exempt when DEFAULT_NETWORK_HOST is unset")
	}
}

func TestGuardedDialer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	addr := ln.Addr().String()

	_, err = guardedDialer(false).Dial("tcp", addr)
	if err == nil || !strings.Contains(err.Error(), "refusing to connect to 127.0.0.1: private address") {
		t.Fatalf("want a refusal, got %v", err)
	}

	// a name resolving to loopback is refused too
	_, port, _ := net.SplitHostPort(addr)
	_, err = guardedDialer(false).Dial("tcp", net.JoinHostPort("localhost", port))
	if err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("want a refusal for localhost, got %v", err)
	}

	c, err := guardedDialer(true).Dial("tcp", addr)
	if err != nil {
		t.Fatalf("exempt dial failed: %v", err)
	}
	c.Close()
}
