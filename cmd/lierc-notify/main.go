package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/lib/pq"
	"github.com/lierc/lierc/lierc"
	"github.com/nsqio/go-nsq"
	"io/ioutil"
	"net/http"
	"net/smtp"
	"os"
	"sync"
)

type LoggedMessage struct {
	Message      *lierc.IRCMessage
	ConnectionId string
	MessageId    int
	Self         bool
	Highlight    bool
}

var client = &http.Client{}

func main() {
	wg := &sync.WaitGroup{}
	wg.Add(1)

	emails := make(chan *LoggedMessage)
	go EmailWorker(emails)

	nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
	nsq_config := nsq.NewConfig()

	notify, _ := nsq.NewConsumer("highlight", "notifier", nsq_config)
	notify.AddHandler(nsq.HandlerFunc(func(message *nsq.Message) error {
		var logged LoggedMessage
		err := json.Unmarshal(message.Body, &logged)

		if err != nil {
			panic(err)
		}

		emails <- &logged
		return nil
	}))

	notify.ConnectToNSQD(nsqd)

	fmt.Print("Ready!\n")
	wg.Wait()
}

func StreamCount(connection string) int {
	api_stats := os.Getenv("API_STATS")
	api_key := os.Getenv("API_KEY")

	req, err := http.NewRequest("GET", api_stats, nil)

	if err != nil {
		panic(err)
	}

	req.Header.Set("Lierc-Key", api_key)
	res, err := client.Do(req)

	if err != nil {
		panic(err)
	}

	body, err := ioutil.ReadAll(res.Body)

	if err != nil {
		panic(err)
	}

	var counts map[string]int
	json.Unmarshal(body, &counts)

	if count, ok := counts[connection]; ok {
		return count
	}

	return 0
}

func EmailWorker(emails chan *LoggedMessage) {
	user := os.Getenv("POSTGRES_USER")
	pass := os.Getenv("POSTGRES_PASSWORD")
	host := os.Getenv("POSTGRES_HOST")
	dbname := os.Getenv("POSTGRES_DB")

	dsn := fmt.Sprintf("user=%s password=%s dbname=%s host=%s sslmode=disable", user, pass, dbname, host)
	db, err := sql.Open("postgres", dsn)

	auth := smtp.PlainAuth("", "", "", "127.0.0.1")

	if err != nil {
		panic(err)
	}

	for {
		logged := <-emails

		var email string
		var username string
		var id string

		err = db.QueryRow(
			"SELECT u.username, u.email, u.id FROM connection AS c LEFT JOIN \"user\" AS u ON c.\"user\" = u.id WHERE c.id=$1",
			logged.ConnectionId,
		).Scan(&username, &email, &id)

		if err != nil {
			panic(err)
		}

		if StreamCount(id) > 0 {
			fmt.Fprintf(os.Stderr, "Skipping because streams are open\n")
			continue
		}

		from := logged.Message.Prefix.Name
		channel := logged.Message.Params[0]
		text := logged.Message.Params[1]
		trunc := text

		if len(trunc) > 30 {
			trunc = text[:30] + "..."
		}

		msg := []byte(fmt.Sprintf("To: %s\r\n", email) +
			fmt.Sprintf("Subject: [%s] < %s> %s", channel, from, trunc) +
			"\r\n" +
			fmt.Sprintf("< %s> %s", from, text) + "\r\n")

		err = smtp.SendMail("127.0.0.1:25", auth, "no-reply@relaychat.party", []string{email}, msg)

		if err != nil {
			panic(err)
		}
	}
}
