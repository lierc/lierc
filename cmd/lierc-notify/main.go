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

type Notifier struct {
	Client        *http.Client
	Notifications map[string]*Notification
	Last          map[string]time.Time
	Mu            *sync.Mutex
	Limiter       *rate.Limiter
	Config        *NotifierConfig
	Queue         chan *LoggedMessage
}

type NotifierConfig struct {
	DBUser          string
	DBPass          string
	DBHost          string
	DBName          string
	APIStats        string
	APIKey          string
	APIURL          string
	VAPIDPrivateKey string
	NSQDHost        string
	Delay           time.Duration
}

func main() {
	wg := &sync.WaitGroup{}
	wg.Add(1)

	conf := &NotifierConfig{
		DBUser:          os.Getenv("POSTGRES_USER"),
		DBPass:          os.Getenv("POSTGRES_PASSWORD"),
		DBHost:          os.Getenv("POSTGRES_HOST"),
		DBName:          os.Getenv("POSTGRES_DB"),
		APIStats:        os.Getenv("API_STATS"),
		APIKey:          os.Getenv("API_KEY"),
		APIURL:          os.Getenv("API_URL"),
		VAPIDPrivateKey: os.Getenv("VAPID_PRIVATE"),
		NSQDHost:        os.Getenv("NSQD_HOST"),
		Delay:           15 * time.Second,
	}

	notifier := &Notifier{
		Client:        &http.Client{},
		Notifications: make(map[string]*Notification),
		Last:          make(map[string]time.Time),
		Mu:            &sync.Mutex{},
		Limiter:       rate.NewLimiter(1, 5),
		Config:        conf,
		Queue:         make(chan *LoggedMessage),
	}

	go notifier.Accumulate()
	notifier.ListenNSQ()

	fmt.Print("Ready!\n")
	wg.Wait()
}

func (n *Notifier) ListenNSQ() {
	nsqd := fmt.Sprintf("%s:4150", n.Config.NSQDHost)
	nsq_config := nsq.NewConfig()

	topic, _ := nsq.NewConsumer("highlight", "notifier", nsq_config)
	topic.AddHandler(nsq.HandlerFunc(func(m *nsq.Message) error {
		var logged LoggedMessage
		err := json.Unmarshal(m.Body, &logged)

		if err != nil {
			panic(err)
		}

		n.Queue <- &logged
		return nil
	}))

	topic.ConnectToNSQD(nsqd)
}

func (n *Notifier) StreamCount(connection string) int {

	req, err := http.NewRequest("GET", n.Config.APIStats, nil)

	if err != nil {
		panic(err)
	}

	req.Header.Set("Lierc-Key", n.Config.APIKey)
	res, err := n.Client.Do(req)

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

func (n *Notifier) RemoveNotification(user string) {
	n.Mu.Lock()
	defer n.Mu.Unlock()

	if o, ok := n.Notifications[user]; ok {
		o.Timer.Stop()
		delete(n.Notifications, user)
	}
}

func (n *Notifier) AddNotification(u string, p *NotificationPref, m *LoggedMessage) {
	n.Mu.Lock()
	defer n.Mu.Unlock()

	if o, ok := n.Notifications[u]; ok {
		o.Messages = append(o.Messages, m)
		o.Timer.Reset(n.Config.Delay)
		fmt.Fprintf(os.Stderr, "Accumulating message on %s\n", u)
		return
	}

	fmt.Fprintf(os.Stderr, "New message on %s\n", u)

	o := &Notification{
		Pref:     p,
		User:     u,
		Messages: []*LoggedMessage{m},
	}

	n.Notifications[u] = o

	o.Timer = time.AfterFunc(n.Config.Delay, func() {
		n.RemoveNotification(u)
		if n.StreamCount(u) == 0 {
			n.Send(o)
		} else {
			fmt.Fprintf(os.Stderr, "Skipping notification due to open streams\n")
		}
	})
}

func (n *Notifier) DB() *sql.DB {
	dsn := fmt.Sprintf(
		"user=%s password=%s dbname=%s host=%s sslmode=disable",
		n.Config.DBUser, n.Config.DBPass, n.Config.DBName, n.Config.DBHost,
	)

	db, err := sql.Open("postgres", dsn)

	if err != nil {
		panic(err)
	}

	return db
}

func (n *Notifier) Accumulate() {
	db := n.DB()

	for {
		m := <-n.Queue

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

		err := db.QueryRow(`
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
		if n.StreamCount(user) > 0 {
			n.RemoveNotification(user)
			continue
		}

		p := &NotificationPref{
			WebPushConfigs: webpushConfigs,
			EmailEnabled:   emailEnabled,
			EmailAddress:   emailAddress,
		}

		n.AddNotification(user, p, m)
	}
}

func (n *Notifier) UserRateLimit(user string) bool {
	if ts, ok := n.Last[user]; ok {
		now := time.Now()
		if ts.Add(10 * time.Minute).After(now) {
			return true
		}
	}
	return false
}

func (n *Notifier) GlobalRateLimit() bool {
	rv := n.Limiter.Reserve()
	if !rv.OK() {
		return true
	}
	delay := rv.Delay()
	fmt.Fprintf(os.Stderr, "Delaying email for %s seconds\n", delay)
	time.Sleep(delay)
	return false
}

func (n *Notifier) RateLimit(user string) bool {
	if n.UserRateLimit(user) {
		fmt.Fprintf(os.Stderr, "User rate limit hit\n")
		return true
	}

	if n.GlobalRateLimit() {
		fmt.Fprintf(os.Stderr, "Global rate limit hit\n")
		return true
	}

	return false
}

func (n *Notifier) Send(o *Notification) {
	if n.RateLimit(o.User) {
		fmt.Fprintf(os.Stderr, "Canceling notification\n")
		return
	}

	if o.Pref.EmailEnabled {
		n.SendEmail(o.Messages, o.Pref.EmailAddress)
	}

	for _, c := range o.Pref.WebPushConfigs {
		n.SendWebPush(o.Messages, c)
	}

	n.Mu.Lock()
	defer n.Mu.Unlock()

	n.Last[o.User] = time.Now()
}

func (n *Notifier) SendWebPush(m []*LoggedMessage, c *WebPushConfig) {
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
		Subscriber:      n.Config.APIURL,
		TTL:             10,
		VAPIDPrivateKey: n.Config.VAPIDPrivateKey,
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

func (n *Notifier) SendEmail(ms []*LoggedMessage, emailAddress string) {
	var (
		lines   []string
		subject string
	)

	for _, m := range ms {
		from := m.Message.Prefix.Name
		channel := m.Message.Params[0]
		text := m.Message.Params[1]
		connection := m.ConnectionId

		line := fmt.Sprintf("    [%s] < %s> %s\n    %s/app/#/%s/%s", channel, from, text, n.Config.APIURL, connection, url.PathEscape(channel))
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
