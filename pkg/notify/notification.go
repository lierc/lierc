package notify

import (
	"time"
)

type NotificationPref struct {
	EmailEnabled   bool
	EmailAddress   string
	WebPushConfigs []*WebPushConfig
	APNConfigs     []*APNConfig
}

type Notification struct {
	Messages []*LoggedMessage
	Timer    *time.Timer
	User     string
	Pref     *NotificationPref
}
