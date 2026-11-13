package logger

import vm "github.com/VictoriaMetrics/metrics"

// messagesTotal counts chats messages by how they're handled: their
// kinds (logKind's String), and multi for NICK and QUIT with their
// channels.
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
