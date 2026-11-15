package notify

import (
	"bytes"
	"io"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// the vector lierc-api's accounts test pins too, computed independently
const unsubscribeVector = "dTE6YWVkODQwMTQ4OTJlZGFiZGM4OWFhNDI2MzRhMTQ5YTc1OWZmNjc1ZDMxNzM0YjkzOGMwYmYzYzZjYjAwOTFmMw"

func TestUnsubscribeTokenVector(t *testing.T) {
	if got := unsubscribeToken("test-secret", "u1"); got != unsubscribeVector {
		t.Errorf("token %s, want %s", got, unsubscribeVector)
	}
}

// emailFor is the notification email for one highlight, its headers parsed
// and its body decoded.
func emailFor(t *testing.T) (mail.Header, string) {
	t.Helper()
	n := &Notifier{Config: &NotifierConfig{APIURL: "https://relaychat.party", UnsubscribeSecret: "test-secret"}}
	m, err := n.emailMessage([]*LoggedMessage{highlight("c1", time.Now())}, "u1@example.com", "u1")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if _, err := m.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(&b)
	if err != nil {
		t.Fatalf("unreadable email: %v\n%s", err, b.String())
	}
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatal(err)
	}
	return msg.Header, string(body)
}

func TestEmailHeaders(t *testing.T) {
	h, _ := emailFor(t)
	for k, want := range map[string]string{
		"From":                      `"Relaychat Party" <notifications@relaychat.party>`,
		"To":                        "<u1@example.com>",
		"Subject":                   "[#c] < bob> hi leedo",
		"Content-Type":              "text/plain; charset=UTF-8",
		"Content-Transfer-Encoding": "quoted-printable",
		"X-Mailer":                  "relaychat",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"Date", "Message-Id"} {
		if h.Get(k) == "" {
			t.Errorf("no %s header", k)
		}
	}
}

func TestEmailUnsubscribeLink(t *testing.T) {
	_, body := emailFor(t)
	if !strings.Contains(body, "https://relaychat.party/unsubscribe/#"+unsubscribeVector) {
		t.Errorf("body lacks the signed link:\n%s", body)
	}
	if strings.Contains(body, "https://relaychat.party/unsubscribe/\n") {
		t.Errorf("body still has the bare link:\n%s", body)
	}
}

func TestEmailUnsubscribeHeaders(t *testing.T) {
	header, body := emailFor(t)
	for k, want := range map[string]string{
		"List-Unsubscribe":      "<https://relaychat.party/api/unsubscribe?token=" + unsubscribeVector + ">",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	} {
		if got := header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if strings.Contains(body, "List-Unsubscribe") {
		t.Errorf("List-Unsubscribe in the body:\n%s", body)
	}
}

func TestConfigUnsubscribeSecret(t *testing.T) {
	c, err := ConfigFromEnv(func(k string) string {
		if k == "UNSUBSCRIBE_SECRET" {
			return "s3"
		}
		return ""
	})
	if err != nil || c.UnsubscribeSecret != "s3" {
		t.Errorf("set: %v %q", err, c.UnsubscribeSecret)
	}
	c, err = ConfigFromEnv(func(string) string { return "" })
	if err != nil || c.UnsubscribeSecret != "changeme" {
		t.Errorf("unset: %v %q, want changeme", err, c.UnsubscribeSecret)
	}
}
