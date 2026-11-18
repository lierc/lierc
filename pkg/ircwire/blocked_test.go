package ircwire

import (
	"net/netip"
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
