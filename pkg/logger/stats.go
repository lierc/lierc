package logger

import vm "github.com/VictoriaMetrics/metrics"

// messagesTotal counts messages by how they're handled: the chats topic's
// kinds (logKind's String) and multi for the multi topic's NICK and QUIT.
// highlightsTotal counts messages that matched a connection's highlights.
// Served on METRICS_LISTEN (pkg/metrics); created up front, so each
// series exists from zero.
var (
	messagesTotal = map[string]*vm.Counter{
		logTarget.String(): vm.NewCounter(`logger_messages_total{kind="target"}`),
		logStatus.String(): vm.NewCounter(`logger_messages_total{kind="status"}`),
		logPass.String():   vm.NewCounter(`logger_messages_total{kind="pass"}`),
		logSkip.String():   vm.NewCounter(`logger_messages_total{kind="skip"}`),
		"multi":            vm.NewCounter(`logger_messages_total{kind="multi"}`),
	}
	highlightsTotal = vm.NewCounter(`logger_highlights_total`)
)
