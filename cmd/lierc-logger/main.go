package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lib/pq"
	"github.com/lierc/lierc/lierc"
	"github.com/nsqio/go-nsq"
	"os"
	"regexp"
	"strconv"
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
	Self         bool
}

var hostname, _ = os.Hostname()
var loggable = map[string]string{
	"PRIVMSG": "#",
	"JOIN":    "#",
	"PART":    "#",
	"TOPIC":   "#",
	"MODE":    "#",
	"NICK":    "skip",
	"QUIT":    "skip",
	"NOTICE":  "status",
	"001":     "status",
	"002":     "status",
	"003":     "status",
	"004":     "status",
	"005":     "status",
	"251":     "status",
	"252":     "status",
	"253":     "status",
	"254":     "status",
	"255":     "status",
	"265":     "status",
	"266":     "status",
	"250":     "status",
	"375":     "status",
	"372":     "status",
	"376":     "status",
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
			event := <-send_event
			json, _ := json.Marshal(event)
			write.Publish("logged", json)
		}
	}()

	nsq_config := nsq.NewConfig()

	priv, _ := nsq.NewConsumer("privmsg", "logger", nsq_config)
	priv.AddHandler(nsq.HandlerFunc(func(message *nsq.Message) error {
		privmsg := struct {
			Id   string
			Line string
		}{}

		err := json.Unmarshal(message.Body, &privmsg)

		if err != nil {
			panic(err)
		}

		parsed := lierc.ParseIRCMessage(privmsg.Line)

		// don't log or echo if insufficient params
		if len(parsed.Params) < 2 {
			return nil
		}

		id := insertMessage(db, privmsg.Id, parsed, parsed.Params[0], true, false)

		send_event <- &LoggedMessage{
			MessageId:    id,
			Message:      parsed,
			ConnectionId: privmsg.Id,
			Self:         true,
		}

		return nil
	}))

	connects, _ := nsq.NewConsumer("connect", "logger", nsq_config)
	connects.AddHandler(nsq.HandlerFunc(func(message *nsq.Message) error {
		client := lierc.IRCClientData{}
		err := json.Unmarshal(message.Body, &client)
		if err != nil {
			panic(err)
		}
		var line = ":" + hostname

		if client.ConnectMessage.Connected {
			line += " CONNECT "
		} else {
			line += " DISCONNECT "
		}

		line += client.Config.Host + " " + strconv.Itoa(client.Config.Port)

		if len(client.ConnectMessage.Message) > 0 {
			line += " :" + client.ConnectMessage.Message
		}

		parsed := lierc.ParseIRCMessage(line)
		id := insertMessage(db, client.Id, parsed, "status", false, false)

		send_event <- &LoggedMessage{
			Message:      parsed,
			ConnectionId: client.Id,
			MessageId:    id,
			Self:         false,
		}

		return nil
	}))

	multi, _ := nsq.NewConsumer("multi", "logger", nsq_config)
	multi.AddHandler(nsq.HandlerFunc(func(message *nsq.Message) error {
		multi_message := lierc.IRCClientMultiMessage{}
		err := json.Unmarshal(message.Body, &multi_message)

		if err != nil {
			panic(err)
		}

		var id int

		for _, channel := range multi_message.Channels {
			id = insertMessage(db, multi_message.Id, multi_message.Message, channel, false, false)
		}

		send_event <- &LoggedMessage{
			Message:      multi_message.Message,
			ConnectionId: multi_message.Id,
			MessageId:    id,
			Self:         false,
		}

		return nil
	}))

	chats, _ := nsq.NewConsumer("chats", "logger", nsq_config)
	chats.AddHandler(nsq.HandlerFunc(func(message *nsq.Message) error {
		client_message := lierc.IRCClientMessage{}
		err := json.Unmarshal(message.Body, &client_message)

		if err != nil {
			panic(err)
		}

		log_type := logType(client_message.Message.Command)

		if log_type == "pass" {
			send_event <- &LoggedMessage{
				Message:      client_message.Message,
				ConnectionId: client_message.Id,
				Self:         false,
			}

			return nil
		}

		if log_type == "skip" {
			return nil
		}

		var channel string

		if log_type == "#" {
			channel = client_message.Message.Params[0]
			// private message because it is an invalid channel name
			// log using sender as "channel"
			if channel[0] != 35 && channel[0] != 38 && channel[0] != 43 && channel[0] != 33 {
				channel = client_message.Message.Prefix.Name
			}
		} else {
			channel = log_type
		}

		var highlight = false

		if client_message.Message.Command == "PRIVMSG" {
			if pattern, ok := highlighters.connection[client_message.Id]; ok {
				highlight = pattern.Match([]byte(client_message.Message.Params[1]))
			}
		}

		id := insertMessage(db, client_message.Id, client_message.Message, channel, false, highlight)

		send_event <- &LoggedMessage{
			MessageId:    id,
			Message:      client_message.Message,
			ConnectionId: client_message.Id,
			Self:         false,
		}

		return nil
	}))

	chats.ConnectToNSQD(nsqd)
	multi.ConnectToNSQD(nsqd)
	priv.ConnectToNSQD(nsqd)
	connects.ConnectToNSQD(nsqd)

	fmt.Print("Ready!\n")
	wg.Wait()
}

func logType(command string) string {
	t, ok := loggable[command]

	if ok {
		return t
	}

	if command[0] == 52 || command[0] == 53 {
		return "status"
	}

	return "pass"
}

func insertMessage(db *sql.DB, client_id string, message *lierc.IRCMessage, channel string, self bool, highlight bool) int {
	value, err := json.Marshal(message)

	if err != nil {
		panic(err)
	}

	var message_id int
	privmsg := strings.ToUpper(message.Command) == "PRIVMSG"

	insert_err := db.QueryRow(
		"INSERT INTO log (connection, channel, privmsg, message, time, self, highlight) VALUES($1,$2,$3,$4,to_timestamp($5),$6,$7) RETURNING id",
		client_id,
		channel,
		privmsg,
		value,
		message.Time,
		self,
		highlight,
	).Scan(&message_id)

	if insert_err != nil {
		panic(insert_err)
	}

	return message_id
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

		for i, term := range terms {
			terms[i] = "\\Q" + term + "\\E"
		}

		if len(terms) == 0 {
			continue
		}

		var source = "\\b(?:" + strings.Join(terms, "|") + ")\\b"
		fmt.Fprintf(os.Stderr, "Compiling regex '%s'\n", source)

		re, err := regexp.Compile(source)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to compile regexp '%s'\n", highlight)
			continue
		}

		highlighters.connection[id] = re
	}
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
