package logger

import (
	"strings"

	vm "github.com/VictoriaMetrics/metrics"
)

// logger_messages_total counts chats messages by IRC command: a numeric
// by its hundred ("0xx" … "9xx"), a known command by name, and anything
// else as "other", so a server can't make up label values.
// storedTotal counts messages stored (in a channel, a private chat or the
// status panel; a NICK or QUIT once however many channels it goes to).
// highlightsTotal counts messages that matched a connection's highlights.
// Served on METRICS_LISTEN (pkg/metrics).
var (
	storedTotal     = vm.NewCounter(`logger_messages_stored_total`)
	highlightsTotal = vm.NewCounter(`logger_highlights_total`)
)

// knownCommands are the named commands counted by name.
var knownCommands = map[string]bool{}

func init() {
	for _, c := range []string{
		"PRIVMSG", "NOTICE", "TAGMSG", "JOIN", "PART", "QUIT", "NICK", "MODE",
		"TOPIC", "KICK", "INVITE", "KILL", "WALLOPS", "ERROR", "PING", "PONG",
		"CAP", "AUTHENTICATE", "ACCOUNT", "AWAY", "CHGHOST", "SETNAME",
		"BATCH", "FAIL", "WARN", "NOTE", "MARKREAD", "REDACT", "CHATHISTORY",
		"CONNECT", "DISCONNECT",
	} {
		knownCommands[c] = true
		vm.GetOrCreateCounter(`logger_messages_total{command="` + c + `"}`)
	}
	for d := '0'; d <= '9'; d++ {
		vm.GetOrCreateCounter(`logger_messages_total{command="` + string(d) + `xx"}`)
	}
	vm.GetOrCreateCounter(`logger_messages_total{command="other"}`)
}

// commandLabel is command's logger_messages_total label.
func commandLabel(command string) string {
	if len(command) == 3 && isDigit(command[0]) && isDigit(command[1]) && isDigit(command[2]) {
		return command[:1] + "xx"
	}
	if c := strings.ToUpper(command); knownCommands[c] {
		return c
	}
	return "other"
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func countCommand(command string) {
	vm.GetOrCreateCounter(`logger_messages_total{command="` + commandLabel(command) + `"}`).Inc()
}
