package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc/pkg/lierc"
	"github.com/lierc/lierc/pkg/logger"
)

func TestNotify(t *testing.T) {
	n := New(&NotifierConfig{}, nil)
	bob := &lierc.IRCPrefix{Name: "bob"}
	// Without a sender or text, email and web push can't be built.
	for _, m := range []*logger.LoggedMessage{
		{ConnectionId: "c1", MessageId: 7},
		{ConnectionId: "c1", Message: &lierc.IRCMessage{Params: []string{"#c", "hi"}}},
		{ConnectionId: "c1", Message: &lierc.IRCMessage{Prefix: bob, Params: []string{"#c"}}},
	} {
		n.Notify(m)
	}
	if len(n.Queue) != 0 {
		t.Fatalf("queued %d messages without a sender or text", len(n.Queue))
	}

	n.Notify(&logger.LoggedMessage{ConnectionId: "c1", MessageId: 7, Highlight: true,
		Message: &lierc.IRCMessage{Prefix: bob, Params: []string{"#c", "hi"}}})
	if m := <-n.Queue; m.ConnectionId != "c1" || m.MessageId != 7 || !m.Highlight || m.Message.Params[1] != "hi" {
		t.Fatalf("queued %+v", m)
	}
}

func TestNotifyFullQueueDrops(t *testing.T) {
	n := New(&NotifierConfig{}, nil)
	n.Queue = make(chan *LoggedMessage, 1)
	m := &logger.LoggedMessage{ConnectionId: "c1",
		Message: &lierc.IRCMessage{Prefix: &lierc.IRCPrefix{Name: "bob"}, Params: []string{"#c", "hi"}}}

	done := make(chan struct{})
	go func() { n.Notify(m); n.Notify(m); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked on a full queue")
	}
	if len(n.Queue) != 1 {
		t.Fatalf("queue has %d, want 1", len(n.Queue))
	}
}

func TestNewQueueIsBuffered(t *testing.T) {
	if c := cap(New(&NotifierConfig{}, nil).Queue); c != QueueSize {
		t.Fatalf("queue capacity %d, want %d", c, QueueSize)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"API_STATS":       "http://127.0.0.1:5009/stats",
		"API_URL":         "https://example.com",
		"VAPID_PRIVATE":   "priv",
		"VAPID_PUBLIC":    "pub",
		"SMTP_OPTS":       "Host,mail,Port,2525",
		"USER_RATE_LIMIT": "5m",
	}
	c, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.APIStats != env["API_STATS"] || c.APIURL != env["API_URL"] ||
		c.VAPIDPrivateKey != "priv" || c.VAPIDPublicKey != "pub" ||
		c.SMTPHost != "mail" || c.SMTPPort != 2525 ||
		c.UserRateLimit != 5*time.Minute || c.Delay != 15*time.Second {
		t.Fatalf("config %+v", c)
	}

	// empty settings are not errors
	if _, err := ConfigFromEnv(func(string) string { return "" }); err != nil {
		t.Fatalf("empty env: %v", err)
	}

	for k, v := range map[string]string{"SMTP_OPTS": "Host", "USER_RATE_LIMIT": "soon"} {
		_, err := ConfigFromEnv(func(key string) string {
			if key == k {
				return v
			}
			return ""
		})
		if err == nil || !strings.HasPrefix(err.Error(), k+": ") {
			t.Errorf("%s=%q: err %v, want it named", k, v, err)
		}
	}
}
