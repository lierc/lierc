package metrics

import (
	"bytes"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestListenRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{":0", "0.0.0.0:0", "[::]:0", "10.0.0.1:0", "localhost:0", "not-an-addr"} {
		ln, err := listen(addr)
		if err == nil {
			ln.Close()
			t.Errorf("%q: listened", addr)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "invalid") {
			t.Errorf("%q: error %q", addr, err)
		}
	}
}

func TestListenLoopback(t *testing.T) {
	ln, err := listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	ln, err = listen("[::1]:0")
	if err != nil {
		if strings.Contains(err.Error(), "loopback") || strings.Contains(err.Error(), "invalid") {
			t.Fatalf("[::1] refused: %v", err)
		}
		t.Skipf("no IPv6 loopback: %v", err)
	}
	ln.Close()
}

func TestHandlerRoutes(t *testing.T) {
	srv := httptest.NewServer(handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	body.ReadFrom(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("GET /metrics: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	for _, want := range []string{"go_goroutines ", "process_resident_memory_bytes "} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("GET /metrics: no %q", want)
		}
	}

	for _, path := range []string{"/", "/metrics/x", "/debug/pprof/"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("GET %s: %d, want 404", path, res.StatusCode)
		}
	}

	res, err = http.Post(srv.URL+"/metrics", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 405 {
		t.Errorf("POST /metrics: %d, want 405", res.StatusCode)
	}
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

func captureLog(t *testing.T) *syncBuf {
	b := &syncBuf{}
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return b
}

func waitForLog(t *testing.T, logs *syncBuf, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no log line with %q in %q", want, logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A taken port is logged, and Serve returns instead of exiting the process
// (reaching the end of this test is the proof).
func TestServeLogsAndReturnsOnBindFailure(t *testing.T) {
	logs := captureLog(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	Serve(taken.Addr().String())
	waitForLog(t, logs, taken.Addr().String())
}

func TestServeRefusalLogged(t *testing.T) {
	logs := captureLog(t)
	Serve("0.0.0.0:0")
	waitForLog(t, logs, "loopback")
}

func TestServeEmptyDoesNothing(t *testing.T) {
	logs := captureLog(t)
	Serve("")
	time.Sleep(50 * time.Millisecond)
	if logs.String() != "" {
		t.Errorf("logged %q", logs.String())
	}
}
