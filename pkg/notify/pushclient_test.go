package notify

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc/pkg/lierc"
)

// The default push client refuses private addresses, by IP or by a name
// that resolves to one, so an endpoint can't reach services on this host.
func TestPushClientRefusesPrivate(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]

	n := &Notifier{}
	for _, url := range []string{srv.URL + "/push", "http://localhost" + port + "/push", "https://[::1]" + port + "/push"} {
		_, err := n.pushClient().Post(url, "application/octet-stream", nil)
		if err == nil || !strings.Contains(err.Error(), "private address") {
			t.Errorf("%s: %v", url, err)
		}
	}
	if hit {
		t.Error("a request reached the local server")
	}
}

// SendWebPush uses that client: a stored endpoint on 127.0.0.1 is never
// contacted.
func TestSendWebPushRefusesPrivate(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	n := sendNotifier(t, "")
	n.PushHTTP = nil // the real client
	_, _, err := n.SendWebPush([]*LoggedMessage{highlight("c1", time.Now())}, subscription(t, srv.URL+"/push/abc"))
	if err == nil || !strings.Contains(err.Error(), "private address") || hit {
		t.Errorf("err %v, hit %v", err, hit)
	}
}

// A redirect isn't followed, so a public endpoint can't bounce the push
// somewhere else.
func TestPushClientNoRedirects(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	c := newPushClient(func(netip.Addr) bool { return false }) // allow httptest
	_, err := c.Post(redirect.URL, "application/octet-stream", nil)
	if !errors.Is(err, errPushRedirect) || followed {
		t.Errorf("err %v, followed %v", err, followed)
	}
}

// Public addresses are allowed through.
func TestPushClientBlockedList(t *testing.T) {
	for addr, want := range map[string]bool{
		"142.250.80.10": false, "2607:f8b0:4006::200a": false,
		"127.0.0.1": true, "10.1.2.3": true, "::ffff:127.0.0.1": true, "169.254.169.254": true,
	} {
		if got := lierc.BlockedIP(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s blocked = %v", addr, got)
		}
	}
}
