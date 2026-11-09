package notify

import (
	"net/http"
	"testing"
	"time"
)

func sent() map[string]uint64 {
	c := map[string]uint64{}
	for k, v := range notificationsTotal {
		c[k] = v.Get()
	}
	return c
}

func delta(before, after map[string]uint64) map[string]uint64 {
	d := map[string]uint64{}
	for k := range after {
		if n := after[k] - before[k]; n != 0 {
			d[k] = n
		}
	}
	return d
}

// Email failing, one push failing, one push gone and one push accepted
// count as one email failure, two web push failures and one success.
func TestSendCountsNotifications(t *testing.T) {
	captureLog(t)
	n := sendNotifier(t, refusedAddr(t))
	okURL, ok := pushService(t, http.StatusCreated)
	badURL, bad := pushService(t, http.StatusInternalServerError)
	goneURL, gone := pushService(t, http.StatusGone)

	before := sent()
	n.Send(&Notification{
		User: "u1",
		Pref: &NotificationPref{
			EmailEnabled: true, EmailAddress: "u1@example.com",
			WebPushConfigs: []*WebPushConfig{subscription(t, okURL), subscription(t, badURL), subscription(t, goneURL)},
		},
		Messages: []*LoggedMessage{highlight("c1", time.Now())},
	})
	received(t, "ok", ok)
	received(t, "bad", bad)
	received(t, "gone", gone)

	got := delta(before, sent())
	want := map[string]uint64{"email/failure": 1, "webpush/failure": 2, "webpush/success": 1}
	if len(got) != len(want) {
		t.Fatalf("counted %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// A panicking email counts as an email failure.
func TestSendCountsPanicAsFailure(t *testing.T) {
	captureLog(t)
	n := sendNotifier(t, refusedAddr(t))
	m := highlight("c1", time.Now())
	m.Message.Prefix = nil
	before := sent()
	n.Send(&Notification{User: "u1", Pref: &NotificationPref{EmailEnabled: true, EmailAddress: "u1@example.com"}, Messages: []*LoggedMessage{m}})
	if got := delta(before, sent()); len(got) != 1 || got["email/failure"] != 1 {
		t.Errorf("counted %v, want email/failure 1", got)
	}
}
