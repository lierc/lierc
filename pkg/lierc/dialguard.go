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

	"github.com/lierc/lierc/pkg/ircwire"
)

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
		if ircwire.BlockedIP(ap.Addr()) {
			return fmt.Errorf("refusing to connect to %s: private address", ap.Addr().Unmap())
		}
		return nil
	}
	return d
}
