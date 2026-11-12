package notify

import (
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

func emailFor(t *testing.T) (header, body string) {
	t.Helper()
	n := &Notifier{Config: &NotifierConfig{APIURL: "https://relaychat.party", UnsubscribeSecret: "test-secret"}}
	msg := string(n.emailMessage([]*LoggedMessage{highlight("c1", time.Now())}, "u1@example.com", "u1"))
	// the message mixes \r\n and \n; the first empty line ends the headers
	msg = strings.ReplaceAll(msg, "\r\n", "\n")
	header, body, ok := strings.Cut(msg, "\n\n")
	if !ok {
		t.Fatalf("no blank line after the headers:\n%s", msg)
	}
	return header, body
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
	lines := strings.Split(header, "\n")
	has := func(want string) bool {
		for _, l := range lines {
			if l == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"List-Unsubscribe: <https://relaychat.party/api/unsubscribe?token=" + unsubscribeVector + ">",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
	} {
		if !has(want) {
			t.Errorf("header lacks %q:\n%s", want, header)
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
