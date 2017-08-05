package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/googlechrome/push-encryption-go/webpush"
	_ "github.com/lib/pq"
	"github.com/lierc/lierc/pkg/lierc"
	"github.com/nsqio/go-nsq"
	"golang.org/x/time/rate"
	"io/ioutil"
	"net/http"
	"net/smtp"
	"net/url"
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

type GCMNotificationConfig struct {
	Endpoint string `json:"endpoint"`
	Key      string `json:"key"`
	Auth     string `json:"auth"`
}

type GCMNotification struct {
	To   string           `json:"to"`
	Data []*LoggedMessage `json:"data"`
}

type NotificationPref struct {
	EmailEnabled bool
	GCMEnabled   bool
	Email        string
	GCM          *GCMNotificationConfig
}

type Notification struct {
	Messages []*LoggedMessage
	Timer    *time.Timer
	User     string
	Pref     *NotificationPref
}

var (
	client        = &http.Client{}
	notifications = make(map[string]*Notification)
	last          = make(map[string]time.Time)
	mu            = &sync.Mutex{}
	delay         = 15 * time.Second
	limiter       = rate.NewLimiter(1, 5)
	db_user       = os.Getenv("POSTGRES_USER")
	db_pass       = os.Getenv("POSTGRES_PASSWORD")
	db_host       = os.Getenv("POSTGRES_HOST")
	db_name       = os.Getenv("POSTGRES_DB")
	api_stats     = os.Getenv("API_STATS")
	api_key       = os.Getenv("API_KEY")
	api_url       = os.Getenv("API_URL")
	gcm_key       = os.Getenv("GCM_KEY")
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

func AddNotification(user string, pref *NotificationPref, message *LoggedMessage) {
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
		Pref:     pref,
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
	dsn := fmt.Sprintf("user=%s password=%s dbname=%s host=%s sslmode=disable", db_user, db_pass, db_name, db_host)
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
			email     string
			user      string
			emailPref sql.NullString
			gcmPref   sql.NullString
		)

		err = db.QueryRow(`
			SELECT u.email, u.id, p.value, p2.value
				FROM connection AS c
			LEFT JOIN "user" AS u
				ON c."user" = u.id
			LEFT JOIN pref AS p
				ON p."user" = u.id
				AND p.name = 'email'
			LEFT JOIN pref AS p2
			  ON p2."user" = u.id
				AND p2.name = 'gcm_sub'
			WHERE c.id=$1
		  `, message.ConnectionId,
		).Scan(&email, &user, &emailPref, &gcmPref)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error selecting email pref (probably unset): '%s'\n", err)
			continue
		}

		if !emailPref.Valid && !gcmPref.Valid {
			fmt.Fprintf(os.Stderr, "No notification prefs enabled for '%s'\n", user)
			continue
		}

		// Skip if user has any streams open
		if StreamCount(user) > 0 {
			RemoveNotification(user)
			continue
		}

		gcm_config := &GCMNotificationConfig{}

		if gcmPref.Valid {
			err = json.Unmarshal([]byte(gcmPref.String), &gcm_config)
			if err != nil {
				panic(err)
			}
		}

		pref := &NotificationPref{
			GCMEnabled:   gcmPref.Valid,
			GCM:          gcm_config,
			EmailEnabled: emailPref.Valid && emailPref.String == "true",
			Email:        email,
		}
		AddNotification(user, pref, message)
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

	if notification.Pref.EmailEnabled {
		SendEmail(notification)
	}
	if notification.Pref.GCMEnabled {
		NotifyGCM(notification)
	}

	last[notification.User] = time.Now()
}

func NotifyGCM(notification *Notification) {
	parts := strings.Split(notification.Pref.GCM.Endpoint, "/")
	id := parts[len(parts)-1]

	key, err := base64.StdEncoding.DecodeString(
		notification.Pref.GCM.Key)

	if err != nil {
		panic(err)
	}

	auth, err := base64.StdEncoding.DecodeString(
		notification.Pref.GCM.Auth)

	if err != nil {
		panic(err)
	}

	sub := &webpush.Subscription{
		notification.Pref.GCM.Endpoint,
		key,
		auth,
	}

	payload := &GCMNotification{
		To:   id,
		Data: notification.Messages,
	}

	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&payload)

	if err != nil {
		panic(err)
	}

	webpush.Send(nil, sub, buf.String(), gcm_key)
}

func SendEmail(notification *Notification) {
	var (
		lines   []string
		subject string
	)

	for _, message := range notification.Messages {
		from := message.Message.Prefix.Name
		channel := message.Message.Params[0]
		text := message.Message.Params[1]
		connection := message.ConnectionId

		line := fmt.Sprintf("    [%s] < %s> %s\n    %s/app/#/%s/%s", channel, from, text, api_url, connection, url.PathEscape(channel))
		lines = append(lines, line)

		if subject == "" {
			subject = fmt.Sprintf("[%s] < %s> %s", channel, from, text)
		}
	}

	email := notification.Pref.Email
	msg := []byte(fmt.Sprintf("To: %s\r\n", email) +
		fmt.Sprintf(
			"From: Relaychat Party <no-reply@relaychat.party>\n"+
				"Subject: %s\n", subject) +
		"\r\n" +
		"You were mentioned in the following channels:\n\n" +
		strings.Join(lines, "\n\n") + "\n\n" +
		"Please do not reply to this message." +
		"\r\n")

	auth := smtp.PlainAuth("", "", "", "127.0.0.1")
	err := smtp.SendMail("127.0.0.1:25", auth, "no-reply@relaychat.party", []string{email}, msg)

	if err != nil {
		panic(err)
	}
}
