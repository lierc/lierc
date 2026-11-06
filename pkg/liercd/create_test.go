package liercd

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A created connection is in the portmap by the time its server accepts
// it: ergo asks identd about a connection as soon as it's accepted, and
// identd answers from the portmap, so a connection added after it started
// dialing could be answered NO-USER and get a ~ username.
func TestCreateListedBeforeConnecting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	t.Setenv("DEFAULT_NETWORK_HOST", "127.0.0.1")
	t.Setenv("DEFAULT_NETWORK_PORT", strconv.Itoa(port))

	m := NewClientManager(discard{})
	listed := make(chan bool, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			listed <- false
			return
		}
		// left open until the test ends, so the portmap keeps it
		t.Cleanup(func() { conn.Close() })
		_, err = m.GetClient("c1")
		listed <- err == nil
	}()

	body := fmt.Sprintf(`{"Host":"127.0.0.1","Port":%d,"Nick":"me","User":"me"}`, port)
	w := httptest.NewRecorder()
	m.HandleCommand(w, httptest.NewRequest("POST", "/c1/create", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	t.Cleanup(func() {
		m.HandleCommand(httptest.NewRecorder(), httptest.NewRequest("POST", "/c1/destroy", nil))
	})
	select {
	case ok := <-listed:
		if !ok {
			t.Fatal("c1 wasn't in the manager when its server accepted the connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no connection")
	}

	// and the portmap names it once the socket is up
	deadline := time.Now().Add(5 * time.Second)
	for {
		w := httptest.NewRecorder()
		m.HandleCommand(w, httptest.NewRequest("GET", "/portmap", nil))
		var ports [][]string
		json.Unmarshal(w.Body.Bytes(), &ports)
		if len(ports) == 1 && ports[0][0] == "me" && ports[0][2] == strconv.Itoa(port) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("portmap %s", w.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
