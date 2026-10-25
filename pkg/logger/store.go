package logger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lib/pq"
	"github.com/lierc/lierc/pkg/lierc"
)

// ErrNoConnection is returned when a message's connection isn't in the
// database, so there's no user to log it for.
var ErrNoConnection = errors.New("no such connection")

// Store is where logged messages are kept.
type Store interface {
	// InsertMessage logs m under channel and returns its id.
	InsertMessage(ctx context.Context, connection string, m *lierc.IRCMessage, channel string, highlight bool) (int, error)
	// InsertMessages logs m once under each of channels and returns
	// the last id.
	InsertMessages(ctx context.Context, connection string, m *lierc.IRCMessage, channels []string) (int, error)
	// InsertPrivate records that nick sent connection a private message.
	InsertPrivate(ctx context.Context, connection string, nick string, time float64) error
	// Highlights returns each connection's highlight words, as JSON.
	Highlights(ctx context.Context) (map[string]string, error)
}

type pgStore struct {
	db *sql.DB
}

func (s *pgStore) InsertMessage(ctx context.Context, connection string, m *lierc.IRCMessage, channel string, highlight bool) (int, error) {
	v, err := json.Marshal(m)
	if err != nil {
		return 0, err
	}

	var id int
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO log ("user", connection, channel, command, message, time, self, highlight)
		SELECT connection."user", connection.id, $2, $3, $4, to_timestamp($5), $6, $7
		FROM connection WHERE connection.id = $1
		RETURNING id`,
		connection,
		strings.ToLower(channel),
		strings.ToUpper(m.Command),
		v,
		m.Time,
		m.Prefix.Self,
		highlight,
	).Scan(&id)

	if err == sql.ErrNoRows {
		return 0, ErrNoConnection
	}
	return id, err
}

func (s *pgStore) InsertMessages(ctx context.Context, connection string, m *lierc.IRCMessage, channels []string) (int, error) {
	if len(channels) == 0 {
		return 0, nil
	}

	v, err := json.Marshal(m)
	if err != nil {
		return 0, err
	}

	lower := make([]string, len(channels))
	for i, channel := range channels {
		lower[i] = strings.ToLower(channel)
	}

	// one statement for all the channels, in order, so the last id is
	// the last channel's
	var id sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`WITH inserted AS (
			INSERT INTO log ("user", connection, channel, command, message, time, self, highlight)
			SELECT connection."user", connection.id, c.channel, $3, $4, to_timestamp($5), $6, false
			FROM connection, unnest($2::text[]) WITH ORDINALITY AS c(channel, n)
			WHERE connection.id = $1
			ORDER BY c.n
			RETURNING id
		)
		SELECT max(id) FROM inserted`,
		connection,
		pq.Array(lower),
		strings.ToUpper(m.Command),
		v,
		m.Time,
		m.Prefix.Self,
	).Scan(&id)

	if err != nil {
		return 0, err
	}
	if !id.Valid {
		return 0, ErrNoConnection
	}
	return int(id.Int64), nil
}

func (s *pgStore) InsertPrivate(ctx context.Context, connection string, nick string, time float64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO private (connection, nick, time) VALUES ($1, $2, to_timestamp($3))
		ON CONFLICT (connection, nick) DO UPDATE SET time = to_timestamp($3)`,
		connection,
		nick,
		time,
	)
	return err
}

func (s *pgStore) Highlights(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, config->>'Highlight' FROM connection`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	configs := make(map[string]string)
	for rows.Next() {
		var id string
		var highlight sql.NullString
		if err := rows.Scan(&id, &highlight); err != nil {
			return nil, err
		}
		if highlight.Valid {
			configs[id] = highlight.String
		}
	}
	return configs, rows.Err()
}
