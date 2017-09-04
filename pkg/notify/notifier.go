package notify

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/lib/pq"
	"github.com/nsqio/go-nsq"
	"golang.org/x/time/rate"
	"io/ioutil"
	"net/http"
	"os"
	"sync"
	"time"
)

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
			apnConfigs     []*APNConfig
			device_id      string
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

		rows, err = db.Query(`
			SELECT device_id
				FROM apn
			WHERE "user"=$1
			`, user,
		)

		if err != nil {
			panic(err)
		}

		defer rows.Close()

		for rows.Next() {
			err := rows.Scan(&device_id)
			if err != nil {
				panic(err)
			}

			config := &APNConfig{
				DeviceToken: device_id,
			}
			apnConfigs = append(apnConfigs, config)
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
			APNConfigs:     apnConfigs,
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

	for _, a := range o.Pref.APNConfigs {
		n.SendAPNS(o.Messages, a)
	}

	n.Mu.Lock()
	defer n.Mu.Unlock()

	n.Last[o.User] = time.Now()
}
