package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/lib/pq"
	"github.com/lierc/lierc/pkg/lierc"
	"github.com/nsqio/go-nsq"
	webpush "github.com/sherclockholmes/webpush-go"
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
	Direct       bool
}

type WebPushConfig struct {
	Endpoint string
	Key      string
	Auth     string
}

type WebPush struct {
	To   string           `json:"to"`
	Data []*LoggedMessage `json:"data"`
}

type NotificationPref struct {
	EmailEnabled   bool
	EmailAddress   string
	WebPushConfigs []*WebPushConfig
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
	vapid_priv    = os.Getenv("VAPID_PRIVATE")
)

func main() {
	wg := &sync.WaitGroup{}
	wg.Add(1)

	c := make(chan *LoggedMessage)
	go Accumulate(c)

	nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
	nsq_config := nsq.NewConfig()

	notify, _ := nsq.NewConsumer("highlight", "notifier", nsq_config)
	notify.AddHandler(nsq.HandlerFunc(func(m *nsq.Message) error {
		var logged LoggedMessage
		err := json.Unmarshal(m.Body, &logged)

		if err != nil {
			panic(err)
		}

		c <- &logged
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

func AddNotification(u string, p *NotificationPref, m *LoggedMessage) {
	mu.Lock()
	defer mu.Unlock()

	if n, ok := notifications[u]; ok {
		n.Messages = append(n.Messages, m)
		n.Timer.Reset(delay)
		fmt.Fprintf(os.Stderr, "Accumulating message on %s\n", u)
		return
	}

	fmt.Fprintf(os.Stderr, "New message on %s\n", u)

	n := &Notification{
		Pref:     p,
		User:     u,
		Messages: []*LoggedMessage{m},
	}

	notifications[u] = n

	n.Timer = time.AfterFunc(delay, func() {
		RemoveNotification(u)
		if StreamCount(u) == 0 {
			n.Send()
		} else {
			fmt.Fprintf(os.Stderr, "Skipping notification due to open streams\n")
		}
	})
}

func Accumulate(c chan *LoggedMessage) {
	dsn := fmt.Sprintf("user=%s password=%s dbname=%s host=%s sslmode=disable", db_user, db_pass, db_name, db_host)
	db, err := sql.Open("postgres", dsn)

	if err != nil {
		panic(err)
	}

	for {
		m := <-c

		recieved := time.Unix(int64(m.Message.Time), 0)
		if recieved.Add(5 * time.Minute).Before(time.Now()) {
			fmt.Fprintf(os.Stderr, "Message is too old, discarding.\n")
		}

		var (
			user         string
			emailPref    sql.NullString
			emailEnabled bool
			emailAddress string
		)

		err = db.QueryRow(`
			SELECT u.email, u.id, p.value
				FROM connection AS c
			JOIN "user" AS u
				ON c."user" = u.id
			LEFT JOIN pref AS p
				ON p."user" = u.id
				AND p.name = 'email'
			WHERE c.id=$1
		  `, m.ConnectionId,
		).Scan(&emailAddress, &user, &emailPref)

		emailEnabled = emailPref.Valid && emailPref.String == "true"

		var (
			webpushConfigs []*WebPushConfig
			endpoint       string
			auth           string
			key            string
		)

		rows, err := db.Query(`
			SELECT endpoint, auth, key
				FROM web_push
			WHERE "user"=$1
			`, user,
		)

		if err != nil {
			panic(err)
		}

		defer rows.Close()

		for rows.Next() {
			err := rows.Scan(&endpoint, &auth, &key)
			if err != nil {
				panic(err)
			}

			config := &WebPushConfig{
				Endpoint: endpoint,
				Auth:     auth,
				Key:      key,
			}
			webpushConfigs = append(webpushConfigs, config)
		}

		// Skip if user has any streams open
		if StreamCount(user) > 0 {
			RemoveNotification(user)
			continue
		}

		p := &NotificationPref{
			WebPushConfigs: webpushConfigs,
			EmailEnabled:   emailEnabled,
			EmailAddress:   emailAddress,
		}

		AddNotification(user, p, m)
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

func (n *Notification) Send() {
	if RateLimit(n.User) {
		fmt.Fprintf(os.Stderr, "Canceling notification\n")
		return
	}

	if n.Pref.EmailEnabled {
		SendEmail(n.Messages, n.Pref.EmailAddress)
	}

	for _, c := range n.Pref.WebPushConfigs {
		SendWebPush(n.Messages, c)
	}

	last[n.User] = time.Now()
}

func SendWebPush(m []*LoggedMessage, c *WebPushConfig) {
	parts := strings.Split(c.Endpoint, "/")
	id := parts[len(parts)-1]

	keys := &webpush.Keys{
		Auth:   c.Auth,
		P256dh: c.Key,
	}

	sub := &webpush.Subscription{
		Endpoint: c.Endpoint,
		Keys:     *keys,
	}

	payload := &WebPush{
		To:   id,
		Data: m,
	}

	var buf bytes.Buffer
	err := json.NewEncoder(&buf).Encode(&payload)

	if err != nil {
		panic(err)
	}

	fmt.Fprintf(os.Stderr, "sending webpush '%s'\n", c.Endpoint)

	res, err := webpush.SendNotification(buf.Bytes(), sub, &webpush.Options{
		Subscriber:      api_url,
		TTL:             10,
		VAPIDPrivateKey: vapid_priv,
	})

	if err != nil {
		panic(err)
	}

	fmt.Fprintf(os.Stderr, "%s\n", res.Status)
	body, err := ioutil.ReadAll(res.Body)

	if err != nil {
		panic(err)
	}

	fmt.Fprintf(os.Stderr, "%s\n", body)
}

func SendEmail(ms []*LoggedMessage, emailAddress string) {
	var (
		lines   []string
		subject string
	)

	for _, m := range ms {
		from := m.Message.Prefix.Name
		channel := m.Message.Params[0]
		text := m.Message.Params[1]
		connection := m.ConnectionId

		line := fmt.Sprintf("    [%s] < %s> %s\n    %s/app/#/%s/%s", channel, from, text, api_url, connection, url.PathEscape(channel))
		lines = append(lines, line)

		if subject == "" {
			subject = fmt.Sprintf("[%s] < %s> %s", channel, from, text)
		}
	}

	fmt.Fprintf(os.Stderr, "sending email '%s'\n", emailAddress)

	msg := []byte(fmt.Sprintf("To: %s\r\n", emailAddress) +
		fmt.Sprintf(
			"From: Relaychat Party <no-reply@relaychat.party>\n"+
				"Subject: %s\n", subject) +
		"\r\n" +
		"You were mentioned in the following channels:\n\n" +
		strings.Join(lines, "\n\n") + "\n\n" +
		"Please do not reply to this message." +
		"\r\n")

	auth := smtp.PlainAuth("", "", "", "127.0.0.1")
	err := smtp.SendMail("127.0.0.1:25", auth, "no-reply@relaychat.party", []string{emailAddress}, msg)

	if err != nil {
		panic(err)
	}
}
