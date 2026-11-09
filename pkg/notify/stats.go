package notify

import vm "github.com/VictoriaMetrics/metrics"

// notificationsTotal counts notifications sent, keyed "type/status": type
// email or webpush (one per subscription), status success or failure. A
// push the service reports gone, or a send that panics, is a failure.
// Served on METRICS_LISTEN (pkg/metrics); created up front, so each series
// exists from zero.
var notificationsTotal = map[string]*vm.Counter{
	"email/success":   vm.NewCounter(`notifier_notifications_total{type="email",status="success"}`),
	"email/failure":   vm.NewCounter(`notifier_notifications_total{type="email",status="failure"}`),
	"webpush/success": vm.NewCounter(`notifier_notifications_total{type="webpush",status="success"}`),
	"webpush/failure": vm.NewCounter(`notifier_notifications_total{type="webpush",status="failure"}`),
}
