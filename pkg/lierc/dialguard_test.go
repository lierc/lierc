package lierc

import (
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "100.64.0.1", "100.127.255.255", "169.254.1.1",
		"172.16.0.1", "172.31.255.255", "192.168.1.1", "192.0.0.1", "198.18.0.1",
		"198.19.255.255", "0.0.0.0", "224.0.0.1", "255.255.255.255", "::", "::1",
		"fd00::1", "fe80::1", "ff02::1", "64:ff9b::1", "::ffff:127.0.0.1",
		"::ffff:10.1.2.3", "::1%lo", "fe80::1%eth0", "::ffff:127.0.0.1%lo",
	}
	allowed := []string{
		"8.8.8.8", "100.63.255.255", "100.128.0.0", "172.15.255.255", "172.32.0.0",
		"198.17.255.255", "198.20.0.0", "192.0.1.1", "2001:4860::8888",
		"::ffff:8.8.8.8",
	}
	for _, s := range blocked {
		if !BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range allowed {
		if BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

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
