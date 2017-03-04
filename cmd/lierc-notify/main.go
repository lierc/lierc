package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/lib/pq"
	"github.com/lierc/lierc/lierc"
	"github.com/nsqio/go-nsq"
	"golang.org/x/time/rate"
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
	User     string
}

var (
	client        = &http.Client{}
	notifications = make(map[string]*Notification)
	last          = make(map[string]time.Time)
	mu            = &sync.Mutex{}
	delay         = 15 * time.Second
	limiter       = rate.NewLimiter(1, 5)
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
	mu.Lock()
	defer mu.Unlock()

	if notification, ok := notifications[user]; ok {
		notification.Messages = append(notification.Messages, message)
		notification.Timer.Reset(delay)
		fmt.Fprintf(os.Stderr, "Accumulating message on %s\n", user)
		return
	}

	fmt.Fprintf(os.Stderr, "New message on %s\n", user)

	notification := &Notification{
		Email:    email,
		User:     user,
		Messages: []*LoggedMessage{message},
	}
	notifications[user] = notification
	notification.Timer = time.AfterFunc(delay, func() {
		RemoveNotification(user)
		if StreamCount(user) == 0 {
			SendNotification(notification)
		} else {
			fmt.Fprintf(os.Stderr, "Skipping notification due to open streams\n")
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

		recieved := time.Unix(int64(message.Message.Time), 0)
		if recieved.Add(5 * time.Minute).Before(time.Now()) {
			fmt.Fprintf(os.Stderr, "Message is too old, discarding.\n")
		}

		var (
			email   string
			user    string
			enabled string
		)

		err = db.QueryRow(`
			SELECT u.email, u.id, p.value
				FROM connection AS c
			LEFT JOIN "user" AS u
				ON c."user" = u.id
			LEFT JOIN pref AS p
				ON p."user" = u.id
				AND p.name = 'email'
			WHERE c.id=$1
		  `, message.ConnectionId,
		).Scan(&email, &user, &enabled)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error selecting email pref (probably unset): %s\n", err)
			continue
		}

		if enabled == "false" {
			fmt.Fprintf(os.Stderr, "User has email notifications disabled\n")
			continue
		}

		// Skip if user has any streams open
		if StreamCount(user) > 0 {
			RemoveNotification(user)
			continue
		}

		AddNotification(user, email, message)
	}
}

func UserRateLimit(user string) bool {
	if ts, ok := last[user]; ok {
		now := time.Now()
		if ts.Add(10 * time.Minute).After(now) {
			return true
		}
	}
	return false
}

func GlobalRateLimit() bool {
	rv := limiter.Reserve()
	if !rv.OK() {
		return true
	}
	delay := rv.Delay()
	fmt.Fprintf(os.Stderr, "Delaying email for %s seconds\n", delay)
	time.Sleep(delay)
	return false
}

func RateLimit(user string) bool {
	if UserRateLimit(user) {
		fmt.Fprintf(os.Stderr, "User rate limit hit\n")
		return true
	}

	if GlobalRateLimit() {
		fmt.Fprintf(os.Stderr, "Global rate limit hit\n")
		return true
	}

	return false
}

func SendNotification(notification *Notification) {
	if RateLimit(notification.User) {
		fmt.Fprintf(os.Stderr, "Canceling notification\n")
		return
	}

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

	auth := smtp.PlainAuth("", "", "", "127.0.0.1")
	err := smtp.SendMail("127.0.0.1:25", auth, "no-reply@relaychat.party", []string{email}, msg)

	if err != nil {
		panic(err)
	}

	last[notification.User] = time.Now()
}
