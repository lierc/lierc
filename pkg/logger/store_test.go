package logger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lierc/lierc/pkg/lierc"
)

// Runs against a real Postgres when LOGGER_TEST_DSN is set, e.g.
// "user=postgres password=test host=localhost port=5432 sslmode=disable".
// The tables are created in a temporary schema that's dropped after.
func testStore(t *testing.T) (*pgStore, *sql.DB) {
	dsn := os.Getenv("LOGGER_TEST_DSN")
	if dsn == "" {
		t.Skip("LOGGER_TEST_DSN not set")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// one connection, so search_path applies to every query
	db.SetMaxOpenConns(1)

	schema := fmt.Sprintf("logger_test_%d", time.Now().UnixNano())
	for _, q := range []string{
		"CREATE SCHEMA " + schema,
		"SET search_path TO " + schema,
		`CREATE TABLE connection (id varchar(24) primary key, "user" varchar(24) not null, config json not null)`,
		`CREATE TABLE log (id serial primary key, connection varchar(24) not null, "user" varchar(24) not null,
			channel varchar(32) not null, command varchar(16), highlight bool not null default false,
			time timestamp not null, message jsonb not null, self bool not null default false)`,
		`CREATE TABLE private (connection varchar(24) not null, nick varchar(32) not null, time timestamp not null,
			primary key (connection, nick))`,
		`INSERT INTO connection VALUES ('c1', 'u1', '{"Highlight": ["leedo"]}'), ('c2', 'u2', '{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		db.Exec("DROP SCHEMA " + schema + " CASCADE")
		db.Close()
	})

	return &pgStore{db: db}, db
}

func TestStoreInsertMessage(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	m := &lierc.IRCMessage{Command: "privmsg", Params: []string{"#Chan", "hi"}, Time: 1700000000.5, Prefix: &lierc.IRCPrefix{Name: "bob"}}

	id, err := s.InsertMessage(ctx, "c1", m, "#Chan", true)
	if err != nil || id == 0 {
		t.Fatalf("InsertMessage = %d, %v", id, err)
	}

	var user, channel, command, text string
	var highlight bool
	var at time.Time
	err = db.QueryRow(`SELECT "user", channel, command, highlight, time, message->'Params'->>1 FROM log WHERE id = $1`, id).
		Scan(&user, &channel, &command, &highlight, &at, &text)
	if err != nil {
		t.Fatal(err)
	}
	if user != "u1" || channel != "#chan" || command != "PRIVMSG" || !highlight || text != "hi" || at.Unix() != 1700000000 {
		t.Errorf("stored %q %q %q %v %v %q", user, channel, command, highlight, at, text)
	}

	if _, err := s.InsertMessage(ctx, "nope", m, "#chan", false); !errors.Is(err, ErrNoConnection) {
		t.Errorf("missing connection: err = %v, want ErrNoConnection", err)
	}
}

func TestStoreInsertMessages(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	m := &lierc.IRCMessage{Command: "QUIT", Params: []string{"bye"}, Time: 1700000000, Prefix: &lierc.IRCPrefix{Name: "bob"}}

	id, err := s.InsertMessages(ctx, "c1", m, []string{"#B", "#a", "#c"})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(`SELECT id, channel FROM log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var last int
	for rows.Next() {
		var channel string
		rows.Scan(&last, &channel)
		got = append(got, channel)
	}
	rows.Close()

	if !equal(got, []string{"#b", "#a", "#c"}) {
		t.Errorf("channels in id order = %q, want #b #a #c", got)
	}
	if id != last {
		t.Errorf("returned id %d, want the last one, %d", id, last)
	}

	if id, err := s.InsertMessages(ctx, "c1", m, nil); id != 0 || err != nil {
		t.Errorf("no channels: %d, %v", id, err)
	}
	if _, err := s.InsertMessages(ctx, "nope", m, []string{"#a"}); !errors.Is(err, ErrNoConnection) {
		t.Errorf("missing connection: err = %v, want ErrNoConnection", err)
	}
}

func TestStoreInsertPrivate(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()

	if err := s.InsertPrivate(ctx, "c1", "bob", 1700000000); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPrivate(ctx, "c1", "bob", 1700000100); err != nil {
		t.Fatal(err)
	}

	var count int
	var at time.Time
	db.QueryRow(`SELECT count(*), max(time) FROM private`).Scan(&count, &at)
	if count != 1 || at.Unix() != 1700000100 {
		t.Errorf("private: %d rows, time %v; want one row, updated", count, at)
	}
}

func TestStoreHighlights(t *testing.T) {
	s, _ := testStore(t)

	configs, err := s.Highlights(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs["c1"] != `["leedo"]` {
		t.Errorf("Highlights = %v, want only c1's", configs)
	}
}
