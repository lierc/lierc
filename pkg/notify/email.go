package notify

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	DefaultSMTPHost = "127.0.0.1"
	DefaultSMTPPort = 25
)

// SetSMTPOpts configures the email notifier from a comma separated list of
// key value pairs, as found in the SMTP_OPTS env var. For example:
//
//	Host,smtp.example.com,Port,465,SSL,1,AuthUser,me,AuthPass,secret
//
// Recognized keys are Host, Port, SSL, AuthUser and AuthPass. Anything left
// unset falls back to 127.0.0.1:25 without SSL or authentication.
func (c *NotifierConfig) SetSMTPOpts(opts string) error {
	c.SMTPHost = DefaultSMTPHost
	c.SMTPPort = DefaultSMTPPort

	opts = strings.TrimSpace(opts)

	if opts == "" {
		return nil
	}

	pairs := strings.Split(opts, ",")

	if len(pairs)%2 != 0 {
		return fmt.Errorf("SMTP opts has an odd number of values: %s", opts)
	}

	for i := 0; i < len(pairs); i += 2 {
		key := strings.TrimSpace(pairs[i])
		value := strings.TrimSpace(pairs[i+1])

		switch strings.ToLower(key) {
		case "host":
			c.SMTPHost = value
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid SMTP port %s: %s", value, err)
			}
			c.SMTPPort = port
		case "ssl":
			c.SMTPSSL = value == "1" || strings.EqualFold(value, "true")
		case "authuser":
			c.SMTPAuthUser = value
		case "authpass":
			c.SMTPAuthPass = value
		default:
			return fmt.Errorf("unknown SMTP opt %s", key)
		}
	}

	return nil
}

func (c *NotifierConfig) SMTPAddress() (string, int) {
	host := c.SMTPHost
	port := c.SMTPPort

	if host == "" {
		host = DefaultSMTPHost
	}

	if port == 0 {
		port = DefaultSMTPPort
	}

	return host, port
}

func (c *NotifierConfig) SMTPAuth(host string) smtp.Auth {
	if c.SMTPAuthUser == "" {
		return nil
	}

	return smtp.PlainAuth("", c.SMTPAuthUser, c.SMTPAuthPass, host)
}

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
		"This message is coming from the relaychat.party IRC client. You enabled email notifications in the client. When someone mentions your name, or uses a word that matches a highlight term you configured, you will recieve an email from us. If you would like to stop recieving these messages, please use this link to unsubscribe: https://relaychat.party/unsubscribe/\n\n" +
		"Thank you for using relaychat.party!" +
		"\r\n")

	err := n.sendMail("notifications@relaychat.party", emailAddress, msg)

	if err != nil {
		panic(err)
	}
}

func (n *Notifier) sendMail(from string, to string, msg []byte) error {
	host, port := n.Config.SMTPAddress()
	addr := fmt.Sprintf("%s:%d", host, port)
	auth := n.Config.SMTPAuth(host)

	if !n.Config.SMTPSSL {
		return smtp.SendMail(addr, auth, from, []string{to}, msg)
	}

	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})

	if err != nil {
		return err
	}

	c, err := smtp.NewClient(conn, host)

	if err != nil {
		conn.Close()
		return err
	}

	defer c.Close()

	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}

	if err := c.Mail(from); err != nil {
		return err
	}

	if err := c.Rcpt(to); err != nil {
		return err
	}

	w, err := c.Data()

	if err != nil {
		return err
	}

	if _, err := w.Write(msg); err != nil {
		return err
	}

	if err := w.Close(); err != nil {
		return err
	}

	return c.Quit()
}
