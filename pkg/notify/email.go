package notify

import (
	"fmt"
	"net/smtp"
	"net/url"
	"os"
	"strings"
)

func (n *Notifier) SendEmail(ms []*LoggedMessage, emailAddress string) {
	var (
		lines   []string
		subject string
	)

	for _, m := range ms {
		from := m.Message.Prefix.Name
		text := m.Message.Params[1]
		connection := m.ConnectionId

		var channel string

		if m.Message.Direct {
			channel = "direct"
		} else {
			channel = m.Message.Params[0]
		}

		line := fmt.Sprintf("    [%s] < %s> %s\n    %s/app/#/%s/%s", channel, from, text, n.Config.APIURL, connection, url.PathEscape(channel))
		lines = append(lines, line)

		if subject == "" {
			subject = fmt.Sprintf("[%s] < %s> %s", channel, from, text)
		}
	}

	fmt.Fprintf(os.Stderr, "sending email '%s'\n", emailAddress)

	msg := []byte(fmt.Sprintf("To: %s\r\n", emailAddress) +
		fmt.Sprintf(
			"From: Relaychat Party <no-reply@relaychat.party>\n"+
				"Subject: %s\n", subject) +
		"\r\n" +
		"You were mentioned in the following channels:\n\n" +
		strings.Join(lines, "\n\n") + "\n\n" +
		"Please do not reply to this message." +
		"\r\n")

	auth := smtp.PlainAuth("", "", "", "127.0.0.1")
	err := smtp.SendMail("127.0.0.1:25", auth, "no-reply@relaychat.party", []string{emailAddress}, msg)

	if err != nil {
		panic(err)
	}
}
