package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lib/pq"
	"github.com/lierc/lierc/pkg/lierc"
	"github.com/nsqio/go-nsq"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Highlighters struct {
	sync.RWMutex
	connection map[string]*regexp.Regexp
}

var highlighters = &Highlighters{
	connection: make(map[string]*regexp.Regexp),
}

type LoggedMessage struct {
	Message      *lierc.IRCMessage
	ConnectionId string
	MessageId    int
	Highlight    bool
}

var hostname, _ = os.Hostname()
var loggable = map[string]string{
	"PRIVMSG":    "#",
	"JOIN":       "#",
	"PART":       "#",
	"TOPIC":      "#",
	"MODE":       "#",
	"NICK":       "skip",
	"QUIT":       "skip",
	"ERROR":      "status",
	"CONNECT":    "status",
	"DISCONNECT": "status",
	"001":        "status",
	"002":        "status",
	"003":        "status",
	"004":        "status",
	"005":        "status",
	"251":        "status",
	"252":        "status",
	"253":        "status",
	"254":        "status",
	"255":        "status",
	"265":        "status",
	"266":        "status",
	"250":        "status",
	"375":        "status",
	"372":        "status",
	"376":        "status",
}

func main() {

	wg := &sync.WaitGroup{}
	wg.Add(1)

	user := os.Getenv("POSTGRES_USER")
	pass := os.Getenv("POSTGRES_PASSWORD")
	host := os.Getenv("POSTGRES_HOST")
	dbname := os.Getenv("POSTGRES_DB")
	nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))

	dsn := fmt.Sprintf("user=%s password=%s dbname=%s host=%s sslmode=disable", user, pass, dbname, host)
	db, err := sql.Open("postgres", dsn)

	if err != nil {
		panic(err)
	}

	updateHighlighters(db)
	setupHighlightListener(dsn, db)

	send_event := make(chan *LoggedMessage)

	go func() {
		nsq_config := nsq.NewConfig()
		write, _ := nsq.NewProducer(nsqd, nsq_config)
		for {
			e := <-send_event
			json, _ := json.Marshal(e)
			write.Publish("logged", json)
			if e.Highlight || (e.Message.Direct && !e.Message.Prefix.Self) {
				write.Publish("highlight", json)
			}
		}
	}()

	nsq_config := nsq.NewConfig()

	multi, _ := nsq.NewConsumer("multi", "logger", nsq_config)
	multi.AddHandler(nsq.HandlerFunc(func(message *nsq.Message) error {
		multi_message := lierc.IRCClientMultiMessage{}
		err := json.Unmarshal(message.Body, &multi_message)

		if err != nil {
			panic(err)
		}

		var id int

		for _, channel := range multi_message.Channels {
			id = insertMessage(db, multi_message.Id, multi_message.Message, channel, false)
		}

		send_event <- &LoggedMessage{
			Message:      multi_message.Message,
			ConnectionId: multi_message.Id,
			MessageId:    id,
			Highlight:    false,
		}

		return nil
	}))

	chats, _ := nsq.NewConsumer("chats", "logger", nsq_config)
	chats.AddHandler(nsq.HandlerFunc(func(n *nsq.Message) error {
		m := lierc.IRCClientMessage{}
		err := json.Unmarshal(n.Body, &m)

		if err != nil {
			panic(err)
		}

		log_type := logType(m.Message)

		if log_type == "pass" {
			send_event <- &LoggedMessage{
				Message:      m.Message,
				ConnectionId: m.Id,
				Highlight:    false,
			}

			return nil
		}

		if log_type == "skip" {
			return nil
		}

		var channel string

		if log_type == "#" {
			if m.Message.Direct && !m.Message.Prefix.Self {
				channel = m.Message.Prefix.Name
				err := insertPrivate(db, m.Id, channel, m.Message.Time)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error logging privmsg: %v", err)
				}
			} else {
				channel = m.Message.Params[0]
			}
		} else {
			channel = log_type
		}

		var highlight = false

		if !m.Message.Prefix.Self && m.Message.Command == "PRIVMSG" {
			highlighters.RLock()
			defer highlighters.RUnlock()
			if pattern, ok := highlighters.connection[m.Id]; ok {
				highlight = pattern.Match([]byte(m.Message.Params[1]))
			}
		}

		id := insertMessage(db, m.Id, m.Message, channel, highlight)

		send_event <- &LoggedMessage{
			MessageId:    id,
			Message:      m.Message,
			ConnectionId: m.Id,
			Highlight:    highlight,
		}

		return nil
	}))

	chats.ConnectToNSQD(nsqd)
	multi.ConnectToNSQD(nsqd)

	fmt.Print("Ready!\n")
	wg.Wait()
}

func logType(m *lierc.IRCMessage) string {
	t, ok := loggable[m.Command]

	if ok {
		return t
	}

	if m.Command == "NOTICE" {
		if m.Direct {
			return "#"
		} else {
			return "status"
		}
	}

	if m.Command[0] == '4' || m.Command[0] == '5' {
		return "status"
	}

	return "pass"
}

func insertPrivate(db *sql.DB, client_id string, nick string, time float64) error {
	_, err := db.Exec(
		"INSERT INTO private (connection, nick, time) VALUES($1,$2,to_timestamp($3)) ON CONFLICT (connection, nick) DO UPDATE SET time=to_timestamp($4)",
		client_id,
		nick,
		time,
		time,
	)
	return err
}

func insertMessage(db *sql.DB, client_id string, m *lierc.IRCMessage, channel string, highlight bool) int {
	v, err := json.Marshal(m)

	if err != nil {
		panic(err)
	}

	var i int

	insert_err := db.QueryRow(
		"INSERT INTO log (connection, channel, command, message, time, self, highlight) VALUES($1,$2,$3,$4,to_timestamp($5),$6,$7) RETURNING id",
		client_id,
		strings.ToLower(channel),
		strings.ToUpper(m.Command),
		v,
		m.Time,
		m.Prefix.Self,
		highlight,
	).Scan(&i)

	if insert_err != nil {
		panic(insert_err)
	}

	return i
}

func updateHighlighters(db *sql.DB) {
	rows, err := db.Query("SELECT id,config->>'Highlight' FROM connection")
	defer rows.Close()

	if err != nil {
		panic(err)
	}

	highlighters.Lock()
	defer highlighters.Unlock()

	for rows.Next() {
		var id string
		var highlight string
		rows.Scan(&id, &highlight)

		fmt.Fprintf(os.Stderr, "Building regex for highlights '%s'\n", highlight)

		var terms []string

		err = json.Unmarshal([]byte(highlight), &terms)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to unmarshall highlights '%s'\n", err.Error)
			continue
		}

		var parts []string

		for _, term := range terms {
			matched, _ := regexp.MatchString("\\S", term)
			if matched {
				parts = append(parts, "\\Q"+term+"\\E")
			}
		}

		if len(parts) == 0 {
			continue
		}

		var source = "(?i)\\b(?:" + strings.Join(parts, "|") + ")\\b"
		fmt.Fprintf(os.Stderr, "Compiling regex '%s'\n", source)

		re, err := regexp.Compile(source)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to compile regexp '%s'\n", highlight)
			continue
		}

		highlighters.connection[id] = re
	}
}

func isChannel(channel byte) bool {
	return channel == 35 || channel == 38 || channel == 43 || channel == 33
}

func setupHighlightListener(dsn string, db *sql.DB) {
	reportProblem := func(ev pq.ListenerEventType, err error) {
		if err != nil {
			panic(err)
		}
	}

	listener := pq.NewListener(dsn, 10*time.Second, time.Minute, reportProblem)
	err := listener.Listen("highlights")

	if err != nil {
		panic(err)
	}

	go func() {
		for {
			fmt.Fprintf(os.Stderr, "Waiting for highlight change notify\n")
			<-listener.Notify
			fmt.Fprintf(os.Stderr, "Got highlight change notify\n")
			updateHighlighters(db)
		}
	}()
}
