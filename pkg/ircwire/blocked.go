package ircwire

import "net/netip"

// Addresses liercd must not connect to: loopback, private and other
// internal ranges, so a user's connection can't reach services on this
// host or its network. The same list is in
// lierc-api's internal/connconfig; keep the two in step.
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
// IPv4-mapped IPv6 address is checked as the IPv4 address it carries, and
// any zone is ignored.
func BlockedIP(ip netip.Addr) bool {
	// a zone (fe80::1%eth0) would make Contains always false
	ip = ip.WithZone("").Unmap()
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
