package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/lib/pq"
	"golang.org/x/time/rate"
	"io/ioutil"
	"log"
	"net/http"
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

	// PushHTTP sends web pushes; nil means a client that refuses private
	// addresses (newPushClient). Tests set it to reach httptest servers.
	PushHTTP *http.Client
	pushOnce sync.Once

	// db is the logger's pool, shared.
	db *sql.DB
}

const DefaultUserRateLimit = 10 * time.Minute

type NotifierConfig struct {
	APIStats string
	APIURL   string
	// UnsubscribeSecret signs the emails' unsubscribe links
	// (UNSUBSCRIBE_SECRET); lierc-api checks them with the same one.
	UnsubscribeSecret string
	VAPIDPrivateKey   string
	VAPIDPublicKey    string
	SMTPHost          string
	SMTPPort          int
	SMTPSSL           bool
	SMTPAuthUser      string
	SMTPAuthPass      string
	SMTPTimeout       time.Duration // per read and write; 0 means DefaultSMTPTimeout
	Delay             time.Duration
	UserRateLimit     time.Duration
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

// openStreams is how many streams the user has open. If stats can't be
// fetched it logs and answers 0: notifying someone who may be online
// (still subject to the rate limits) beats dropping the notification.
func (n *Notifier) openStreams(user string) int {
	count, err := n.StreamCount(user)
	if err != nil {
		log.Printf("stream stats unavailable, notifying %s anyway: %v", user, err)
		return 0
	}
	return count
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
		log.Printf("highlight %d for %s added to pending notification (%d messages)", m.MessageId, u, len(o.Messages))
		return
	}

	log.Printf("highlight %d for %s: notifying in %s", m.MessageId, u, n.Config.Delay)

	o := &Notification{
		Pref:     p,
		User:     u,
		Messages: []*LoggedMessage{m},
	}

	n.Notifications[u] = o

	o.Timer = time.AfterFunc(n.Config.Delay, func() {
		n.RemoveNotification(u)
		if streams := n.openStreams(u); streams > 0 {
			log.Printf("not notifying %s: %d open streams", u, streams)
			return
		}
		n.Send(o)
	})
}

// DB returns the pool New was given.
func (n *Notifier) DB() (*sql.DB, error) {
	if n.db == nil {
		return nil, errors.New("no database")
	}
	return n.db, nil
}

// maxMessageAge is how old a highlight may be before it is discarded rather
// than notified, e.g. after the logger was down and nsqd replays a backlog of chats.
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
	if streams := n.openStreams(user); streams > 0 {
		log.Printf("highlight %d for %s not notified: %d open streams", m.MessageId, user, streams)
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

func (n *Notifier) userRateLimit() time.Duration {
	if n.Config.UserRateLimit == 0 {
		return DefaultUserRateLimit
	}
	return n.Config.UserRateLimit
}

func (n *Notifier) UserRateLimit(user string) bool {
	limit := n.userRateLimit()

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
	if delay := rv.Delay(); delay > 0 {
		log.Printf("global rate limit: waiting %s", delay)
		time.Sleep(delay)
	}
	return false
}

func (n *Notifier) RateLimit(user string) bool {
	if n.UserRateLimit(user) {
		log.Printf("not notifying %s: notified less than %s ago", user, n.userRateLimit())
		return true
	}

	if n.GlobalRateLimit() {
		log.Printf("not notifying %s: global rate limit", user)
		return true
	}

	return false
}

// Send notifies the user by email and every web push subscription they
// have. Each method runs on its own, so one that fails, panics or hangs
// doesn't stop the others being tried.
func (n *Notifier) Send(o *Notification) {
	if !o.Pref.EmailEnabled && len(o.Pref.WebPushConfigs) == 0 {
		log.Printf("not notifying %s: email off and no web push subscriptions", o.User)
		return
	}

	if n.RateLimit(o.User) {
		return
	}

	// Recorded before sending, so the user rate limit holds while a slow
	// send is still going.
	n.Mu.Lock()
	n.Last[o.User] = time.Now()
	n.Mu.Unlock()

	var wg sync.WaitGroup
	// Each send is counted once, under kind: by what f reports, or as a
	// failure if it panics.
	attempt := func(kind, what string, f func() bool) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := "failure"
			defer func() {
				if r := recover(); r != nil {
					log.Printf("%s for %s failed: panic: %v", what, o.User, r)
				}
				notificationsTotal[kind+"/"+status].Inc()
			}()
			if f() {
				status = "success"
			}
		}()
	}

	if o.Pref.EmailEnabled {
		attempt("email", "email", func() bool { return n.sendEmail(o) })
	}
	for _, c := range o.Pref.WebPushConfigs {
		attempt("webpush", "web push to "+c.Endpoint, func() bool { return n.sendWebPush(o, c) })
	}
	wg.Wait()
}

// sendEmail emails o, logs the outcome and reports whether it was sent.
func (n *Notifier) sendEmail(o *Notification) bool {
	if err := n.SendEmail(o.Messages, o.Pref.EmailAddress, o.User); err != nil {
		log.Printf("email to %s failed: %v", o.User, err)
		return false
	}
	log.Printf("emailed %s (%d messages)", o.User, len(o.Messages))
	return true
}

// sendWebPush pushes o to c, logs the outcome and reports whether the
// push service accepted it.
func (n *Notifier) sendWebPush(o *Notification, c *WebPushConfig) bool {
	res, body, err := n.SendWebPush(o.Messages, c)
	switch {
	case err != nil:
		log.Printf("web push to %s for %s failed: %v", c.Endpoint, c.User, err)
	case WebPushGone(res.StatusCode):
		log.Printf("web push to %s for %s: %s, deleting the subscription", c.Endpoint, c.User, res.Status)
		n.DeleteWebPushConfig(c)
	case res.StatusCode >= 400:
		log.Printf("web push to %s for %s failed: %s %s", c.Endpoint, c.User, res.Status, strings.TrimSpace(string(body)))
	default:
		log.Printf("web push to %s for %s: %s (%d messages)", c.Endpoint, c.User, res.Status, len(o.Messages))
		return true
	}
	return false
}

func (n *Notifier) DeleteWebPushConfig(c *WebPushConfig) {
	db, err := n.DB()
	if err != nil {
		log.Printf("deleting web push %s for %s: %v", c.Endpoint, c.User, err)
		return
	}
	_, err = db.Exec(`
	  DELETE FROM web_push
		WHERE "user"=$1 AND endpoint=$2
	`, c.User, c.Endpoint)

	if err != nil {
		log.Printf("deleting web push %s for %s: %v", c.Endpoint, c.User, err)
	}
}
