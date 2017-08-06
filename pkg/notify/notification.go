package notify

import (
	"time"
)

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
