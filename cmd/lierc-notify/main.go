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
	"strings"
	"sync"
	"time"
)

type LoggedMessage struct {
	Message      *lierc.IRCMessage
	ConnectionId string
	MessageId    int
	Self         bool
	Highlight    bool
}

type Notification struct {
	Messages []*LoggedMessage
	Timer    *time.Timer
	Email    string
}

var (
	client        = &http.Client{}
	notifications = make(map[string]*Notification)
	mu            = &sync.Mutex{}
	delay         = 2 * time.Minute
)

func main() {
	wg := &sync.WaitGroup{}
	wg.Add(1)

	messages := make(chan *LoggedMessage)
	go Accumulate(messages)

	nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
	nsq_config := nsq.NewConfig()

	notify, _ := nsq.NewConsumer("highlight", "notifier", nsq_config)
	notify.AddHandler(nsq.HandlerFunc(func(message *nsq.Message) error {
		var logged LoggedMessage
		err := json.Unmarshal(message.Body, &logged)

		if err != nil {
			panic(err)
		}

		messages <- &logged
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

func RemoveNotification(user string) {
	mu.Lock()
	defer mu.Unlock()

	if notification, ok := notifications[user]; ok {
		notification.Timer.Stop()
		delete(notifications, user)
	}
}

func AddNotification(user string, email string, message *LoggedMessage) {
	notifications.Lock()
	defer notifications.Unlock()

	if notification, ok := notifications[user]; ok {
		notification.Messages = append(notification.Messages, message)
		notification.Timer.Reset(delay)
		return
	}

	notification := &Notification{
		Email:    email,
		Messages: []*LoggedMessage{message},
	}
	notification.Timer = time.AfterFunc(delay, func() {
		RemoveNotification(user)
		if StreamCount(user) == 0 {
			SendNotification(notification)
		}
	})

}

func Accumulate(messages chan *LoggedMessage) {
	user := os.Getenv("POSTGRES_USER")
	pass := os.Getenv("POSTGRES_PASSWORD")
	host := os.Getenv("POSTGRES_HOST")
	dbname := os.Getenv("POSTGRES_DB")

	dsn := fmt.Sprintf("user=%s password=%s dbname=%s host=%s sslmode=disable", user, pass, dbname, host)
	db, err := sql.Open("postgres", dsn)

	if err != nil {
		panic(err)
	}

	for {
		message := <-messages

		var (
			email string
			user  string
		)

		err = db.QueryRow(
			"SELECT u.email, u.id FROM connection AS c LEFT JOIN \"user\" AS u ON c.\"user\" = u.id WHERE c.id=$1",
			message.ConnectionId,
		).Scan(&email, &user)

		if err != nil {
			panic(err)
		}

		// Skip if user has any streams open
		if StreamCount(user) > 0 {
			RemoveNotification(user)
			continue
		}

		AddNotification(user, email, message)
	}
}

func SendNotification(notification *Notification) {
	auth := smtp.PlainAuth("", "", "", "127.0.0.1")

	var lines []string

	for _, message := range notification.Messages {
		from := message.Message.Prefix.Name
		channel := message.Message.Params[0]
		text := message.Message.Params[1]
		lines = append(lines, fmt.Sprintf("[%s] < %s> %s", channel, from, text))
	}

	email := notification.Email

	msg := []byte(fmt.Sprintf("To: %s\r\n", email) +
		fmt.Sprintf("Subject: %s", lines[0]) +
		"\r\n" +
		strings.Join(lines, "\n") + "\r\n")

	err := smtp.SendMail("127.0.0.1:25", auth, "no-reply@relaychat.party", []string{email}, msg)

	if err != nil {
		panic(err)
	}
}
