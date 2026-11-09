package notify

import (
	"context"
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

	dbOnce sync.Once
	db     *sql.DB
	dbErr  error
}

const DefaultUserRateLimit = 10 * time.Minute

type NotifierConfig struct {
	DBUser          string
	DBPass          string
	DBHost          string
	DBName          string
	APIStats        string
	APIURL          string
	VAPIDPrivateKey string
	VAPIDPublicKey  string
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
	// Email and web push need a sender and the message text.
	if msg := logged.Message; msg == nil || msg.Prefix == nil || len(msg.Params) < 2 {
		log.Printf("dropping highlight without a sender or text: %s", m.Body)
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

// statsTimeout bounds one /stats request, so a hung stream server can't
// stall the queue.
var statsTimeout = 5 * time.Second

// StreamCount asks the stream server's admin listener (API_STATS, on
// loopback, no key) how many streams the user has open.
func (n *Notifier) StreamCount(user string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", n.Config.APIStats, nil)
	if err != nil {
		return 0, err
	}

	res, err := n.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return 0, err
	}
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var counts map[string]int
	if err := json.Unmarshal(body, &counts); err != nil {
		return 0, err
	}

	return counts[user], nil
}

// hasStreams reports whether the user has a stream open. If stats can't be
// fetched it logs and answers false: notifying someone who may be online
// (still subject to the rate limits) beats dropping the notification.
func (n *Notifier) hasStreams(user string) bool {
	count, err := n.StreamCount(user)
	if err != nil {
		log.Printf("stream stats unavailable, notifying %s anyway: %v", user, err)
		return false
	}
	return count > 0
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
		if !n.hasStreams(u) {
			n.Send(o)
		} else {
			fmt.Fprintf(os.Stderr, "Skipping notification due to open streams\n")
		}
	})
}

// DB returns the notifier's Postgres pool, opened on first use and shared
// by every caller.
func (n *Notifier) DB() (*sql.DB, error) {
	n.dbOnce.Do(func() {
		dsn := fmt.Sprintf(
			"user=%s password=%s dbname=%s host=%s sslmode=disable",
			n.Config.DBUser, n.Config.DBPass, n.Config.DBName, n.Config.DBHost,
		)
		n.db, n.dbErr = sql.Open("postgres", dsn)
	})
	return n.db, n.dbErr
}

// maxMessageAge is how old a highlight may be before it is discarded rather
// than notified, e.g. after the notifier was down and nsqd replays a backlog.
const maxMessageAge = 5 * time.Minute

func (n *Notifier) Accumulate() {
	for m := range n.Queue {
		n.accumulate(m)
	}
}

// accumulate handles one highlight. Errors are logged and the highlight
// dropped, so one bad message or a database outage never stops the notifier.
func (n *Notifier) accumulate(m *LoggedMessage) {
	received := time.Unix(int64(m.Message.Time), 0)
	if received.Add(maxMessageAge).Before(time.Now()) {
		log.Printf("discarding highlight %d on %s: too old (%s)", m.MessageId, m.ConnectionId, received.UTC().Format(time.RFC3339))
		return
	}

	user, p, err := n.lookup(m.ConnectionId)
	if err != nil {
		log.Printf("discarding highlight %d on %s: %v", m.MessageId, m.ConnectionId, err)
		return
	}

	// Skip if user has any streams open
	if n.hasStreams(user) {
		n.RemoveNotification(user)
		return
	}

	n.AddNotification(user, p, m)
}

// lookup finds the user who owns a connection and how to notify them.
func (n *Notifier) lookup(connection string) (string, *NotificationPref, error) {
	db, err := n.DB()
	if err != nil {
		return "", nil, err
	}

	var (
		user         string
		emailPref    sql.NullString
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
	  `, connection,
	).Scan(&emailAddress, &user, &emailPref)
	if err == sql.ErrNoRows {
		return "", nil, fmt.Errorf("unknown connection")
	}
	if err != nil {
		return "", nil, fmt.Errorf("looking up connection: %w", err)
	}

	rows, err := db.Query(`
		SELECT endpoint, auth, key
			FROM web_push
		WHERE "user"=$1
		`, user,
	)
	if err != nil {
		return "", nil, fmt.Errorf("looking up web push: %w", err)
	}
	defer rows.Close()

	var webpushConfigs []*WebPushConfig
	for rows.Next() {
		c := &WebPushConfig{User: user}
		if err := rows.Scan(&c.Endpoint, &c.Auth, &c.Key); err != nil {
			return "", nil, fmt.Errorf("reading web push: %w", err)
		}
		webpushConfigs = append(webpushConfigs, c)
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("reading web push: %w", err)
	}

	return user, &NotificationPref{
		WebPushConfigs: webpushConfigs,
		EmailEnabled:   emailPref.Valid && emailPref.String == "true",
		EmailAddress:   emailAddress,
	}, nil
}

func (n *Notifier) UserRateLimit(user string) bool {
	limit := n.Config.UserRateLimit

	if limit == 0 {
		limit = DefaultUserRateLimit
	}

	n.Mu.Lock()
	ts, ok := n.Last[user]
	n.Mu.Unlock()

	if ok {
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
		if err := n.SendEmail(o.Messages, o.Pref.EmailAddress); err != nil {
			log.Printf("error sending email for %s: %v", o.User, err)
		}
	}

	for _, c := range o.Pref.WebPushConfigs {
		res, err := n.SendWebPush(o.Messages, c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error sending webpush to %s for %s: %s\n", c.Endpoint, c.User, err)
		} else if WebPushGone(res.StatusCode) {
			fmt.Fprintf(os.Stderr, "Deleting webpush %s for %s: %s\n", c.Endpoint, c.User, res.Status)
			n.DeleteWebPushConfig(c)
		} else if res.StatusCode >= 400 {
			fmt.Fprintf(os.Stderr, "Webpush to %s for %s failed: %s\n", c.Endpoint, c.User, res.Status)
		}
	}

	n.Mu.Lock()
	defer n.Mu.Unlock()

	n.Last[o.User] = time.Now()
}

func (n *Notifier) DeleteWebPushConfig(c *WebPushConfig) {
	db, err := n.DB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting webpush: %s\n", err)
		return
	}
	_, err = db.Exec(`
	  DELETE FROM web_push
		WHERE "user"=$1 AND endpoint=$2
	`, c.User, c.Endpoint)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting webpush: %s\n", err)
	}
}
