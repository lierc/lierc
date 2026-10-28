package notify

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func TestSendWebPushSendsVAPIDPublicKey(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}

	// the browser's side of the subscription
	browser, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	rand.Read(secret)

	n := &Notifier{Config: &NotifierConfig{
		APIURL:          "https://relaychat.party",
		VAPIDPrivateKey: private,
		VAPIDPublicKey:  public,
	}}
	res, err := n.SendWebPush([]*LoggedMessage{}, &WebPushConfig{
		Endpoint: srv.URL + "/push/abc",
		Key:      base64.RawURLEncoding.EncodeToString(browser.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if !strings.HasSuffix(auth, ", k="+public) {
		t.Fatalf("Authorization %q does not end with the VAPID public key %q", auth, public)
	}
}

func TestWebPushGone(t *testing.T) {
	for status, gone := range map[int]bool{
		http.StatusCreated:             false,
		http.StatusBadRequest:          false,
		http.StatusUnauthorized:        false,
		http.StatusForbidden:           false,
		http.StatusNotFound:            true,
		http.StatusGone:                true,
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
		http.StatusServiceUnavailable:  false,
	} {
		if got := WebPushGone(status); got != gone {
			t.Errorf("WebPushGone(%d) = %v, want %v", status, got, gone)
		}
	}
}
