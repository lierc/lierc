package notify

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func statsNotifier(url string) *Notifier {
	return &Notifier{Client: &http.Client{}, Config: &NotifierConfig{APIStats: url}}
}

func TestStreamCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"7":2}`))
	}))
	defer srv.Close()
	n := statsNotifier(srv.URL + "/stats")

	if c, err := n.StreamCount("7"); err != nil || c != 2 {
		t.Errorf("user 7: %d, %v", c, err)
	}
	if c, err := n.StreamCount("8"); err != nil || c != 0 {
		t.Errorf("user 8: %d, %v", c, err)
	}
	if !n.hasStreams("7") || n.hasStreams("8") {
		t.Error("hasStreams wrong")
	}
}

// Every way stats can fail is an error from StreamCount, and hasStreams
// answers false so the notification still goes out.
func TestStreamCountUnavailable(t *testing.T) {
	old := statsTimeout
	statsTimeout = 100 * time.Millisecond
	defer func() { statsTimeout = old }()

	refused, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedURL := "http://" + refused.Addr().String() + "/stats"
	refused.Close()

	handler := func(f http.HandlerFunc) string {
		srv := httptest.NewServer(f)
		t.Cleanup(srv.Close)
		return srv.URL + "/stats"
	}
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })

	cases := map[string]string{
		"refused": refusedURL,
		"401": handler(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"status":"error","error":"Unauthorized"}`))
		}),
		"not json": handler(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html>"))
		}),
		"hangs": handler(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-hang:
			case <-r.Context().Done():
			}
		}),
	}
	for name, url := range cases {
		n := statsNotifier(url)
		start := time.Now()
		if _, err := n.StreamCount("7"); err == nil {
			t.Errorf("%s: no error", name)
		}
		if n.hasStreams("7") {
			t.Errorf("%s: hasStreams true", name)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: took %s", name, d)
		}
	}
}
