// Package logger stores IRC messages from liercd in the log table and
// republishes them, with their ids, for the API and the notifier.
//
// It reads two NSQ topics written by liercd: "chats", one message at a
// time, and "multi", messages like NICK and QUIT that belong in several
// channels at once. Everything it handles is published to "logged", and
// highlights and private messages also to "highlight".
package logger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/lib/pq"
	"github.com/lierc/lierc/pkg/lierc"
	"github.com/nsqio/go-nsq"
)

type Config struct {
	DBUser   string
	DBPass   string
	DBHost   string
	DBName   string
	NSQDHost string
	// most database connections to open; 0 means no limit
	MaxDBConns int
}

func (c *Config) dsn() string {
	return fmt.Sprintf("user=%s password=%s dbname=%s host=%s sslmode=disable", c.DBUser, c.DBPass, c.DBName, c.DBHost)
}

func (c *Config) nsqd() string {
	return fmt.Sprintf("%s:4150", c.NSQDHost)
}

// LoggedMessage is what's published to the logged and highlight topics.
type LoggedMessage struct {
	Message      *lierc.IRCMessage
	ConnectionId string
	MessageId    int
	Highlight    bool
}

type publisher interface {
	Publish(topic string, body []byte) error
}

type Logger struct {
	config     *Config
	db         *sql.DB
	store      Store
	pub        publisher
	producer   *nsq.Producer
	consumers  []*nsq.Consumer
	listener   *pq.Listener
	highlights *Highlighters
}

func New(config *Config) (*Logger, error) {
	db, err := sql.Open("postgres", config.dsn())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(config.MaxDBConns)

	producer, err := nsq.NewProducer(config.nsqd(), nsq.NewConfig())
	if err != nil {
		db.Close()
		return nil, err
	}

	return &Logger{
		config:     config,
		db:         db,
		store:      &pgStore{db: db},
		pub:        producer,
		producer:   producer,
		highlights: &Highlighters{},
	}, nil
}

// Start loads highlights, starts listening for changes to them, and
// starts consuming the chats and multi topics.
func (l *Logger) Start(ctx context.Context) error {
	if err := l.db.PingContext(ctx); err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := l.producer.Ping(); err != nil {
		return fmt.Errorf("connecting to nsqd: %w", err)
	}

	l.reloadHighlights(ctx)
	if err := l.listenForHighlights(ctx); err != nil {
		return err
	}

	for topic, handler := range map[string]nsq.HandlerFunc{
		"chats": l.handleChat,
		"multi": l.handleMulti,
	} {
		consumer, err := nsq.NewConsumer(topic, "logger", nsq.NewConfig())
		if err != nil {
			return err
		}
		consumer.AddHandler(handler)
		if err := consumer.ConnectToNSQD(l.config.nsqd()); err != nil {
			return fmt.Errorf("consuming %s: %w", topic, err)
		}
		l.consumers = append(l.consumers, consumer)
	}

	return nil
}

// Stop finishes the messages in flight, then disconnects.
func (l *Logger) Stop() {
	for _, consumer := range l.consumers {
		consumer.Stop()
	}
	for _, consumer := range l.consumers {
		<-consumer.StopChan
	}
	l.producer.Stop()
	if l.listener != nil {
		l.listener.Close()
	}
	l.db.Close()
}

func (l *Logger) handleChat(n *nsq.Message) error {
	var m lierc.IRCClientMessage
	if err := json.Unmarshal(n.Body, &m); err != nil {
		log.Printf("dropping unreadable chats message: %v\n%s", err, n.Body)
		return nil
	}
	if m.Message == nil {
		log.Printf("dropping chats message without a message\n%s", n.Body)
		return nil
	}

	ctx := context.Background()
	route := routeChat(m.Message)

	switch route.action {
	case actionSkip:
		return nil
	case actionPass:
		l.publish(&LoggedMessage{Message: m.Message, ConnectionId: m.Id})
		return nil
	}

	if route.private {
		if err := l.store.InsertPrivate(ctx, m.Id, route.channel, m.Message.Time); err != nil {
			return fmt.Errorf("logging private message: %w", err)
		}
	}

	highlight := l.highlights.Match(m.Id, m.Message)

	id, err := l.store.InsertMessage(ctx, m.Id, m.Message, route.channel, highlight)
	if err != nil && !errors.Is(err, ErrNoConnection) {
		return fmt.Errorf("logging message for %s: %w", m.Id, err)
	}
	if err != nil {
		log.Printf("got message for missing connection %q\n%s", m.Id, m.Message.Raw)
	}

	l.publish(&LoggedMessage{
		Message:      m.Message,
		ConnectionId: m.Id,
		MessageId:    id,
		Highlight:    highlight,
	})
	return nil
}

func (l *Logger) handleMulti(n *nsq.Message) error {
	var m lierc.IRCClientMultiMessage
	if err := json.Unmarshal(n.Body, &m); err != nil {
		log.Printf("dropping unreadable multi message: %v\n%s", err, n.Body)
		return nil
	}
	if m.Message == nil {
		log.Printf("dropping multi message without a message\n%s", n.Body)
		return nil
	}

	id, err := l.store.InsertMessages(context.Background(), m.Id, m.Message, m.Channels)
	if err != nil && !errors.Is(err, ErrNoConnection) {
		return fmt.Errorf("logging message for %s: %w", m.Id, err)
	}
	if err != nil {
		log.Printf("got message for missing connection %q\n%s", m.Id, m.Message.Raw)
	}

	l.publish(&LoggedMessage{
		Message:      m.Message,
		ConnectionId: m.Id,
		MessageId:    id,
	})
	return nil
}

// publish sends e to the API, and to the notifier if it's a highlight
// or a private message. Failures are only logged: the message is
// already stored, and retrying it would store it twice.
func (l *Logger) publish(e *LoggedMessage) {
	body, err := json.Marshal(e)
	if err != nil {
		log.Printf("encoding logged message: %v", err)
		return
	}

	if err := l.pub.Publish("logged", body); err != nil {
		log.Printf("publishing to logged: %v", err)
	}
	if e.Highlight || (e.Message.Direct && !e.Message.Prefix.Self) {
		if err := l.pub.Publish("highlight", body); err != nil {
			log.Printf("publishing to highlight: %v", err)
		}
	}
}

func (l *Logger) reloadHighlights(ctx context.Context) {
	configs, err := l.store.Highlights(ctx)
	if err != nil {
		log.Printf("loading highlights: %v", err)
		return
	}

	patterns, errs := compileHighlights(configs)
	for _, err := range errs {
		log.Print(err)
	}
	l.highlights.Replace(patterns)
}

// listenForHighlights reloads highlights whenever the API sends
// NOTIFY highlights, and after reconnecting, in case one was missed.
func (l *Logger) listenForHighlights(ctx context.Context) error {
	l.listener = pq.NewListener(l.config.dsn(), 10*time.Second, time.Minute,
		func(ev pq.ListenerEventType, err error) {
			if err != nil {
				log.Printf("highlight listener: %v", err)
			}
		})

	if err := l.listener.Listen("highlights"); err != nil {
		return fmt.Errorf("listening for highlight changes: %w", err)
	}

	go func() {
		for range l.listener.Notify {
			l.reloadHighlights(ctx)
		}
	}()

	return nil
}
