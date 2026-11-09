package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/lierc/lierc/pkg/lierc"
)

// pushTimeout bounds one web push request, from connecting to reading the
// push service's reply.
const pushTimeout = 30 * time.Second

// errPushRedirect is returned for a push service that redirects: a push is
// sent to the stored endpoint only.
var errPushRedirect = errors.New("push service redirected; not following")

// newPushClient is the HTTP client for web pushes. Endpoints come from
// users, so it refuses to connect to any address blocked reports, checked
// after name resolution (so a name can't point somewhere private when the
// push is sent), follows no redirects and gives up after pushTimeout.
func newPushClient(blocked func(netip.Addr) bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("refusing to connect to %s: %v", address, err)
			}
			if blocked(ap.Addr()) {
				return fmt.Errorf("refusing to connect to %s: private address", ap.Addr().Unmap())
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: pushTimeout,
		Transport: &http.Transport{
			Proxy: nil, // straight to the push service, never via a proxy
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConnsPerHost: 4,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errPushRedirect },
	}
}

// pushClient is the client SendWebPush uses: n.PushHTTP if set (tests),
// otherwise one that refuses lierc.BlockedIP addresses.
func (n *Notifier) pushClient() *http.Client {
	n.pushOnce.Do(func() {
		if n.PushHTTP == nil {
			n.PushHTTP = newPushClient(lierc.BlockedIP)
		}
	})
	return n.PushHTTP
}
