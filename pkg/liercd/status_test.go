package liercd

import (
	"fmt"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc/pkg/lierc"
)

type enqueued struct {
	topic string
	v     interface{}
}

// queueRec records everything enqueued, in order.
type queueRec struct {
	mu  sync.Mutex
	got []enqueued
}

func (q *queueRec) Enqueue(topic string, v interface{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.got = append(q.got, enqueued{topic, v})
}

func (q *queueRec) all() []enqueued {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]enqueued{}, q.got...)
}

// A status change is a 000 line on chats, for the status panel's
// history, then the state on the status topic, for the stream server.
func TestStatusPublishesLineAndState(t *testing.T) {
	q := &queueRec{}
	m := NewClientManager(q)
	m.Status(&lierc.IRCClientStatus{Id: "c1", Host: "irc.x", Nick: "me", Message: "Reconnecting in 30s"})
	m.Status(&lierc.IRCClientStatus{Id: "c1", Host: "irc.x", Connected: true, Message: "Connected"})

	got := q.all()
	if len(got) != 4 {
		t.Fatalf("enqueued %d, want 4: %+v", len(got), got)
	}
	line, ok := got[0].v.(*lierc.IRCClientMessage)
	if got[0].topic != "chats" || !ok || line.Id != "c1" || line.Message.Command != "000" ||
		line.Message.Prefix.Name != "irc.x" || strings.Join(line.Message.Params, "|") != "me|Reconnecting in 30s" {
		t.Errorf("first: %s %+v", got[0].topic, got[0].v)
	}
	st, ok := got[1].v.(*statusMessage)
	if got[1].topic != "status" || !ok || st.Id != "c1" || st.Connected || st.Message != "Reconnecting in 30s" ||
		time.Since(time.Unix(int64(st.Time), 0)) > time.Minute {
		t.Errorf("second: %s %+v", got[1].topic, got[1].v)
	}
	// before registration there's no nick yet: *
	if line := got[2].v.(*lierc.IRCClientMessage); strings.Join(line.Message.Params, "|") != "*|Connected" {
		t.Errorf("no-nick line params %q", line.Message.Params)
	}
	if st := got[3].v.(*statusMessage); got[3].topic != "status" || !st.Connected {
		t.Errorf("fourth: %s %+v", got[3].topic, got[3].v)
	}
}

// Creating and destroying a connection publishes none of the old fake
// connection events: the API announces those changes now.
func TestCreateDestroyPublishNoConnectionEvents(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	t.Setenv("DEFAULT_NETWORK_HOST", "127.0.0.1")
	t.Setenv("DEFAULT_NETWORK_PORT", strconv.Itoa(port))

	q := &queueRec{}
	m := NewClientManager(q)
	body := fmt.Sprintf(`{"Host":"127.0.0.1","Port":%d,"Nick":"me","User":"me"}`, port)
	w := httptest.NewRecorder()
	m.HandleCommand(w, httptest.NewRequest("POST", "/c1/create", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	time.Sleep(200 * time.Millisecond) // let it connect and publish its status
	w = httptest.NewRecorder()
	m.HandleCommand(w, httptest.NewRequest("POST", "/c1/destroy", nil))
	if w.Code != 200 {
		t.Fatalf("destroy: %d %s", w.Code, w.Body)
	}

	sawStatus := false
	for _, e := range q.all() {
		if e.topic == "status" {
			sawStatus = true
		}
		if msg, ok := e.v.(*lierc.IRCClientMessage); ok {
			switch msg.Message.Command {
			case "CREATE", "DELETE", "CONNECT", "DISCONNECT":
				t.Errorf("published %s on %s", msg.Message.Command, e.topic)
			}
		}
	}
	if !sawStatus {
		t.Error("no status published")
	}
}
