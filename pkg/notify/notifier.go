package notify

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/lib/pq"
	"github.com/nsqio/go-nsq"
	"golang.org/x/time/rate"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
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

const DefaultUserRateLimit = 10 * time.Minute

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
	SMTPHost        string
	SMTPPort        int
	SMTPSSL         bool
	SMTPAuthUser    string
	SMTPAuthPass    string
	Delay           time.Duration
	UserRateLimit   time.Duration
}

// SetUserRateLimit sets how long to wait before notifying the same user
// again, from a duration string as found in the USER_RATE_LIMIT env var. For
// example: 30s, 10m, 1h. An empty value falls back to 10m.
func (c *NotifierConfig) SetUserRateLimit(limit string) error {
	limit = strings.TrimSpace(limit)

	if limit == "" {
		c.UserRateLimit = DefaultUserRateLimit
		return nil
	}

	d, err := time.ParseDuration(limit)

	if err != nil {
		return fmt.Errorf("invalid user rate limit %s: %s", limit, err)
	}

	if d < 0 {
		return fmt.Errorf("user rate limit cannot be negative: %s", limit)
	}

	c.UserRateLimit = d

	return nil
}

// ListenNSQ consumes the highlight topic, blocking until nsqd is reachable.
func (n *Notifier) ListenNSQ() error {
	nsqd := fmt.Sprintf("%s:4150", n.Config.NSQDHost)

	topic, err := nsq.NewConsumer("highlight", "notifier", consumerConfig())
	if err != nil {
		return err
	}
	topic.AddHandler(nsq.HandlerFunc(n.handleHighlight))

	// go-nsq only retries connections that drop, not ones that never
	// connected
	retry("consuming highlight", func() error {
		return topic.ConnectToNSQD(nsqd)
	})
	return nil
}

// handleHighlight queues a highlight for Accumulate. A message that is not
// JSON is logged and dropped rather than retried forever.
func (n *Notifier) handleHighlight(m *nsq.Message) error {
	var logged LoggedMessage
	if err := json.Unmarshal(m.Body, &logged); err != nil {
		log.Printf("dropping unreadable highlight message: %v\n%s", err, m.Body)
		return nil
	}

	n.Queue <- &logged
	return nil
}

// Without nsqlookupd, go-nsq waits LookupdPollInterval (60s by default)
// between attempts to reconnect to nsqd, so after nsqd restarts no
// notifications were sent for a minute. Retry much sooner.
var reconnectInterval = 5 * time.Second

func consumerConfig() *nsq.Config {
	config := nsq.NewConfig()
	config.LookupdPollInterval = reconnectInterval
	return config
}

// retry calls f until it succeeds, reconnectInterval apart.
func retry(what string, f func() error) {
	for {
		err := f()
		if err == nil {
			return
		}
		log.Printf("%s: %v (retrying in %s)", what, err, reconnectInterval)
		time.Sleep(reconnectInterval)
	}
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
				User:     user,
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
				User:        user,
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
	limit := n.Config.UserRateLimit

	if limit == 0 {
		limit = DefaultUserRateLimit
	}

	if ts, ok := n.Last[user]; ok {
		now := time.Now()
		if ts.Add(limit).After(now) {
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
		res, err := n.SendWebPush(o.Messages, c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
		} else if res.StatusCode >= 400 {
			n.DeleteWebPushConfig(c)
		}
	}

	for _, a := range o.Pref.APNConfigs {
		err := n.SendAPNS(o.Messages, a)
		if err != nil && err.Error() == "INVALID_TOKEN" {
			n.DeleteAPNConfig(a)
		}
	}

	n.Mu.Lock()
	defer n.Mu.Unlock()

	n.Last[o.User] = time.Now()
}

func (n *Notifier) DeleteWebPushConfig(c *WebPushConfig) {
	db := n.DB()
	_, err := db.Exec(`
	  DELETE FROM web_push
		WHERE "user"=$1 AND endpoint=$2
	`, c.User, c.Endpoint)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting webpush: %s\n", err)
	}
}

func (n *Notifier) DeleteAPNConfig(c *APNConfig) {
	db := n.DB()
	_, err := db.Exec(`
	  DELETE FROM apn
		WHERE "user"=$1 AND device_id=$2
	`, c.User, c.DeviceToken)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting apn: %s\n", err)
	}
}
