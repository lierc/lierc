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
			channel = m.Message.Prefix.Name
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
			"From: Relaychat Party <notifications@relaychat.party>\n"+
				"Subject: %s\n", subject) +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\n" +
		"Content-Transfer-Encoding: 8bit\n" +
		"X-Mailer: relaychat 0.1\n" +
		"\r\n" +
		"You were mentioned in the following channels:\n\n" +
		strings.Join(lines, "\n\n") + "\n\n" +
		"This message is coming from the relaychat.party IRC client. You enabled email notifications in the client. When someone mentions your name, or uses a word that matches a highlight term you configured, you will recieve an email from us. If you would like to stop recieving these messages, please use this link to unsubscribe: https://relaychat.party/app/unsubscribe\n\n" +
		"Thank you for using relaychat.party!" +
		"\r\n")

	err := smtp.SendMail("127.0.0.1:25", nil, "notifications@relaychat.party", []string{emailAddress}, msg)

	if err != nil {
		panic(err)
	}
}
