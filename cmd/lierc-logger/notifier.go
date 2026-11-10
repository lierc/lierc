package main

import (
	"database/sql"
	"log"

	"github.com/lierc/lierc/pkg/notify"
)

// newNotifier builds the notifier from the environment, sharing db. A
// setting that can't be parsed turns notifications off (nil) rather than
// stopping the logger.
func newNotifier(getenv func(string) string, db *sql.DB) *notify.Notifier {
	conf, err := notify.ConfigFromEnv(getenv)
	if err != nil {
		log.Printf("notifications off: %v", err)
		return nil
	}
	return notify.New(conf, db)
}
