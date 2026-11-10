package notify

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lierc/lierc/pkg/logger"
	"golang.org/x/time/rate"
)

// QueueSize is how many highlights can wait for Accumulate. Notify never
// waits for room, so a slow /stats or database can't hold up logging; a
// highlight that finds the queue full is dropped.
const QueueSize = 500

// New returns a notifier that uses db, the logger's pool. Run Accumulate
// to process what Notify queues.
func New(conf *NotifierConfig, db *sql.DB) *Notifier {
	return &Notifier{
		Client:        &http.Client{},
		Notifications: make(map[string]*Notification),
		Last:          make(map[string]time.Time),
		Mu:            &sync.Mutex{},
		Limiter:       rate.NewLimiter(1, 5),
		Config:        conf,
		Queue:         make(chan *LoggedMessage, QueueSize),
		db:            db,
	}
}

// Notify queues a highlight or private message from the logger, without
// waiting. A message without a sender or text is dropped, since email and
// web push need both.
func (n *Notifier) Notify(e *logger.LoggedMessage) {
	if msg := e.Message; msg == nil || msg.Prefix == nil || len(msg.Params) < 2 {
		log.Printf("dropping highlight %d on %s without a sender or text", e.MessageId, e.ConnectionId)
		return
	}

	m := &LoggedMessage{
		Message:      e.Message,
		ConnectionId: e.ConnectionId,
		MessageId:    e.MessageId,
		Highlight:    e.Highlight,
	}
	select {
	case n.Queue <- m:
	default:
		log.Printf("dropping highlight %d on %s: notifier queue full", e.MessageId, e.ConnectionId)
	}
}

// ConfigFromEnv reads the notifier's settings. An error names the setting
// that couldn't be parsed; empty settings get their defaults.
func ConfigFromEnv(getenv func(string) string) (*NotifierConfig, error) {
	c := &NotifierConfig{
		APIStats:        getenv("API_STATS"),
		APIURL:          getenv("API_URL"),
		VAPIDPrivateKey: getenv("VAPID_PRIVATE"),
		VAPIDPublicKey:  getenv("VAPID_PUBLIC"),
		Delay:           15 * time.Second,
	}
	if err := c.SetSMTPOpts(getenv("SMTP_OPTS")); err != nil {
		return nil, fmt.Errorf("SMTP_OPTS: %w", err)
	}
	if err := c.SetUserRateLimit(getenv("USER_RATE_LIMIT")); err != nil {
		return nil, fmt.Errorf("USER_RATE_LIMIT: %w", err)
	}
	return c, nil
}
