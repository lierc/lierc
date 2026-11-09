package notify

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"golang.org/x/time/rate"
)

// captureLog collects the notifier's log output for the rest of the test.
func captureLog(t *testing.T) *syncBuf {
	b := &syncBuf{}
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return b
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (b *syncBuf) mustContain(t *testing.T, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(b.String(), s) {
			t.Errorf("log has no %q:\n%s", s, b.String())
		}
	}
}

// pushService answers web pushes with status and records each one.
func pushService(t *testing.T, status int) (url string, got chan struct{}) {
	got = make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status >= 400 {
			w.Write([]byte("push service says no"))
		}
		got <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/push/abc", got
}

// subscription is a browser's side of a web push subscription to endpoint.
func subscription(t *testing.T, endpoint string) *WebPushConfig {
	browser, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	rand.Read(secret)
	return &WebPushConfig{
		Endpoint: endpoint,
		Key:      base64.RawURLEncoding.EncodeToString(browser.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(secret),
		User:     "u1",
	}
}

// sendNotifier sends email to smtpAddr and has valid VAPID keys.
func sendNotifier(t *testing.T, smtpAddr string) *Notifier {
	n := accumulateNotifier(t, nil)
	n.Limiter = rate.NewLimiter(rate.Inf, 1)
	n.PushHTTP = &http.Client{} // the push services are httptest servers on 127.0.0.1
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	n.Config.APIURL = "https://relaychat.party"
	n.Config.VAPIDPrivateKey, n.Config.VAPIDPublicKey = private, public
	if smtpAddr != "" {
		host, port, _ := net.SplitHostPort(smtpAddr)
		p, err := strconv.Atoi(port)
		if err != nil {
			t.Fatal(err)
		}
		n.Config.SMTPHost, n.Config.SMTPPort = host, p
	}
	return n
}

func refusedAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func received(t *testing.T, what string, got chan struct{}) {
	t.Helper()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never received", what)
	}
}

// A failing email doesn't stop web push, and one failing push service
// doesn't stop another; every outcome is logged.
func TestSendEmailFailsPushStillSent(t *testing.T) {
	logs := captureLog(t)
	n := sendNotifier(t, refusedAddr(t))
	okURL, ok := pushService(t, http.StatusCreated)
	badURL, bad := pushService(t, http.StatusInternalServerError)

	n.Send(&Notification{
		User: "u1",
		Pref: &NotificationPref{
			EmailEnabled: true, EmailAddress: "u1@example.com",
			WebPushConfigs: []*WebPushConfig{subscription(t, badURL), subscription(t, okURL)},
		},
		Messages: []*LoggedMessage{highlight("c1", time.Now())},
	})
	received(t, "good push", ok)
	received(t, "failing push", bad)
	logs.mustContain(t,
		"email to u1 failed: dial tcp",
		"web push to "+badURL+" for u1 failed: 500 Internal Server Error push service says no",
		"web push to "+okURL+" for u1: 201 Created (1 messages)",
	)
}

// A panic in the email code is recovered and the push still goes out.
func TestSendEmailPanicsPushStillSent(t *testing.T) {
	logs := captureLog(t)
	n := sendNotifier(t, refusedAddr(t))
	url, got := pushService(t, http.StatusCreated)
	m := highlight("c1", time.Now())
	m.Message.Prefix = nil // SendEmail reads Prefix.Name

	n.Send(&Notification{
		User:     "u1",
		Pref:     &NotificationPref{EmailEnabled: true, EmailAddress: "u1@example.com", WebPushConfigs: []*WebPushConfig{subscription(t, url)}},
		Messages: []*LoggedMessage{m},
	})
	received(t, "push", got)
	logs.mustContain(t, "email for u1 failed: panic:", "web push to "+url+" for u1: 201 Created")
}

// An SMTP server that never answers doesn't hold up web push.
func TestSendEmailHangsPushStillSent(t *testing.T) {
	captureLog(t)
	hang, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := hang.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c) // accept, then say nothing
			mu.Unlock()
		}
	}()
	n := sendNotifier(t, hang.Addr().String())
	url, got := pushService(t, http.StatusCreated)

	done := make(chan struct{})
	go func() {
		n.Send(&Notification{
			User:     "u1",
			Pref:     &NotificationPref{EmailEnabled: true, EmailAddress: "u1@example.com", WebPushConfigs: []*WebPushConfig{subscription(t, url)}},
			Messages: []*LoggedMessage{highlight("c1", time.Now())},
		})
		close(done)
	}()
	received(t, "push while SMTP hangs", got)

	hang.Close()
	mu.Lock()
	for _, c := range conns {
		c.Close()
	}
	mu.Unlock()
	<-done
}

func TestSendNothingEnabled(t *testing.T) {
	logs := captureLog(t)
	n := sendNotifier(t, "")
	n.Send(&Notification{User: "u1", Pref: &NotificationPref{}, Messages: []*LoggedMessage{highlight("c1", time.Now())}})
	logs.mustContain(t, "not notifying u1: email off and no web push subscriptions")
	if n.UserRateLimit("u1") {
		t.Error("nothing sent but rate limited")
	}
}

func TestSendRateLimitedLogged(t *testing.T) {
	logs := captureLog(t)
	n := sendNotifier(t, refusedAddr(t))
	n.Last["u1"] = time.Now()
	n.Send(&Notification{User: "u1", Pref: &NotificationPref{EmailEnabled: true}, Messages: []*LoggedMessage{highlight("c1", time.Now())}})
	logs.mustContain(t, "not notifying u1: notified less than 10m0s ago")
}

// A pending notification whose user has opened a stream by the time it's
// due is skipped, and says so.
func TestPendingSkippedForOpenStreams(t *testing.T) {
	logs := captureLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"u1":2}`))
	}))
	defer srv.Close()
	n := sendNotifier(t, "")
	n.Config.APIStats = srv.URL + "/stats"
	n.Config.Delay = 10 * time.Millisecond

	n.AddNotification("u1", &NotificationPref{EmailEnabled: true}, highlight("c1", time.Now()))
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "not notifying u1: 2 open streams") {
		if time.Now().After(deadline) {
			t.Fatalf("no skip logged:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	logs.mustContain(t, "highlight 1 for u1: notifying in 10ms")
}
