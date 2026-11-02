package lierc

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Addresses liercd must not connect to: loopback, private and other
// internal ranges, so a user's connection can't reach services on this
// host or its network. The same list is in
// lierc-api/lib/API/ConnectionConfig.pm; keep the two in step.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("64:ff9b::/96"),
}

// BlockedIP reports whether liercd refuses to connect to ip. An
// IPv4-mapped IPv6 address is checked as the IPv4 address it carries.
func BlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Exempt reports whether cfg is the default network (DEFAULT_NETWORK_HOST
// on DEFAULT_NETWORK_PORT, default 6667), which may be private, e.g.
// irc.relaychat.party -> 127.0.0.1 in /etc/hosts.
func Exempt(cfg *IRCConfig) bool {
	host := os.Getenv("DEFAULT_NETWORK_HOST")
	if host == "" || !strings.EqualFold(cfg.Host, host) {
		return false
	}
	port := 6667
	if p, err := strconv.Atoi(os.Getenv("DEFAULT_NETWORK_PORT")); err == nil && p != 0 {
		port = p
	}
	return cfg.Port == port
}

// guardedDialer checks each address it is about to connect to, after name
// resolution, so a name can't point somewhere private at connect time.
func guardedDialer(exempt bool) *net.Dialer {
	d := &net.Dialer{Timeout: 30 * time.Second}
	if exempt {
		return d
	}
	d.Control = func(network, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("refusing to connect to %s: %v", address, err)
		}
		if BlockedIP(ap.Addr()) {
			return fmt.Errorf("refusing to connect to %s: private address", ap.Addr().Unmap())
		}
		return nil
	}
	return d
}
