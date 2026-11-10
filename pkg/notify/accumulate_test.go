package notify

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc/pkg/lierc"
	"golang.org/x/time/rate"
)

// accumulateNotifier has no open streams and a delay long enough that no
// notification is sent during a test.
func accumulateNotifier(t *testing.T, db *sql.DB) *Notifier {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	n := &Notifier{
		Client:        &http.Client{},
		Notifications: map[string]*Notification{},
		Last:          map[string]time.Time{},
		Mu:            &sync.Mutex{},
		Config:        &NotifierConfig{APIStats: srv.URL + "/stats", Delay: time.Hour},
	}
	n.db = db
	t.Cleanup(func() {
		for _, o := range n.Notifications {
			o.Timer.Stop()
		}
	})
	return n
}

func highlight(connection string, at time.Time) *LoggedMessage {
	return &LoggedMessage{
		ConnectionId: connection,
		MessageId:    1,
		Message: &lierc.IRCMessage{
			Prefix:  &lierc.IRCPrefix{Name: "bob"},
			Command: "PRIVMSG",
			Params:  []string{"#c", "hi leedo"},
			Time:    float64(at.Unix()),
		},
	}
}

// With the database down, a highlight is dropped instead of panicking.
func TestAccumulateDatabaseDown(t *testing.T) {
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := accumulateNotifier(t, db)
	n.accumulate(highlight("c1", time.Now()))
	if len(n.Notifications) != 0 {
		t.Errorf("notified: %v", n.Notifications)
	}
}

// UserRateLimit reads Last while Send writes it from timer goroutines; run
// with -race.
func TestUserRateLimitConcurrent(t *testing.T) {
	n := accumulateNotifier(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		u := fmt.Sprint(i % 5)
		go func() {
			defer wg.Done()
			n.Mu.Lock()
			n.Last[u] = time.Now()
			n.Mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			n.UserRateLimit(u)
		}()
	}
	wg.Wait()
	if !n.UserRateLimit("0") {
		t.Error("user 0 not rate limited")
	}
}

// Runs against a real Postgres when NOTIFY_TEST_DSN is set, e.g.
// "user=postgres password=test host=localhost port=5432 sslmode=disable".
// The tables are created in a temporary schema that's dropped after.
func testDB(t *testing.T) *sql.DB {
	dsn := os.Getenv("NOTIFY_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTIFY_TEST_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// one connection, so search_path applies to every query
	db.SetMaxOpenConns(1)

	schema := fmt.Sprintf("notify_test_%d", time.Now().UnixNano())
	for _, q := range []string{
		"CREATE SCHEMA " + schema,
		"SET search_path TO " + schema,
		`CREATE TABLE "user" (id varchar(24) primary key, email varchar(255) not null)`,
		`CREATE TABLE connection (id varchar(24) primary key, "user" varchar(24) not null)`,
		`CREATE TABLE pref ("user" varchar(24) not null, name varchar(255) not null, value text not null)`,
		`CREATE TABLE web_push (endpoint varchar(255) not null, key varchar(255) not null,
			auth varchar(255) not null, "user" varchar(24) not null, primary key ("user", endpoint))`,
		`INSERT INTO "user" VALUES ('u1', 'u1@example.com'), ('u2', 'u2@example.com')`,
		`INSERT INTO connection VALUES ('c1', 'u1'), ('c2', 'u2'), ('c3', 'gone')`,
		`INSERT INTO pref VALUES ('u1', 'email', 'true'), ('u2', 'email', 'false')`,
		`INSERT INTO web_push VALUES ('https://push.example/a', 'ka', 'aa', 'u1'), ('https://push.example/b', 'kb', 'ab', 'u1')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Exec("DROP SCHEMA " + schema + " CASCADE")
		db.Close()
	})
	return db
}

func TestLookup(t *testing.T) {
	n := accumulateNotifier(t, testDB(t))

	user, p, err := n.lookup("c1")
	if err != nil {
		t.Fatal(err)
	}
	if user != "u1" || !p.EmailEnabled || p.EmailAddress != "u1@example.com" || len(p.WebPushConfigs) != 2 {
		t.Errorf("c1: %s %+v", user, p)
	}
	for _, c := range p.WebPushConfigs {
		if c.User != "u1" || c.Endpoint == "" || c.Key == "" || c.Auth == "" {
			t.Errorf("web push %+v", c)
		}
	}

	user, p, err = n.lookup("c2")
	if err != nil || user != "u2" || p.EmailEnabled || len(p.WebPushConfigs) != 0 {
		t.Errorf("c2: %s %+v %v", user, p, err)
	}

	// No connection, or one whose user is gone.
	for _, c := range []string{"nope", "c3"} {
		if _, _, err := n.lookup(c); err == nil || err.Error() != "unknown connection" {
			t.Errorf("%s: %v", c, err)
		}
	}
}

func TestAccumulate(t *testing.T) {
	db := testDB(t)
	n := accumulateNotifier(t, db)

	n.accumulate(highlight("c1", time.Now()))
	if o := n.Notifications["u1"]; o == nil || len(o.Messages) != 1 || len(o.Pref.WebPushConfigs) != 2 {
		t.Fatalf("not queued: %+v", n.Notifications)
	}

	// Too old, or on an unknown connection: dropped.
	n.accumulate(highlight("c2", time.Now().Add(-maxMessageAge-time.Minute)))
	n.accumulate(highlight("nope", time.Now()))
	if len(n.Notifications) != 1 {
		t.Errorf("notifications: %+v", n.Notifications)
	}

	// Many highlights in a row on a one-connection pool: each lookup must
	// release its connection, or the next one blocks.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			n.accumulate(highlight("c1", time.Now()))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("accumulate blocked: connection not released")
	}
	if o := n.Notifications["u1"]; len(o.Messages) != 21 {
		t.Errorf("u1 has %d messages", len(o.Messages))
	}
}

// An SMTP failure is logged instead of panicking, and still counts toward
// the user rate limit.
func TestSendEmailFailure(t *testing.T) {
	refused, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := refused.Addr().(*net.TCPAddr).Port
	refused.Close()

	n := accumulateNotifier(t, nil)
	n.Limiter = rate.NewLimiter(rate.Inf, 1)
	n.Config.SMTPHost, n.Config.SMTPPort = "127.0.0.1", port
	n.Send(&Notification{
		User:     "u1",
		Pref:     &NotificationPref{EmailEnabled: true, EmailAddress: "u1@example.com"},
		Messages: []*LoggedMessage{highlight("c1", time.Now())},
	})
	if !n.UserRateLimit("u1") {
		t.Error("send not recorded")
	}
}

// A highlight for a user with an open stream is dropped, and logged.
func TestAccumulateOpenStreamsLogged(t *testing.T) {
	db := testDB(t)
	logs := captureLog(t)
	n := accumulateNotifier(t, db)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"u1":1}`))
	}))
	defer srv.Close()
	n.Config.APIStats = srv.URL + "/stats"

	n.accumulate(highlight("c1", time.Now()))
	if len(n.Notifications) != 0 {
		t.Errorf("queued: %+v", n.Notifications)
	}
	logs.mustContain(t, "highlight 1 for u1 not notified: 1 open streams")
}
