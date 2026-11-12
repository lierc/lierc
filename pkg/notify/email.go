package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSMTPHost = "127.0.0.1"
	DefaultSMTPPort = 25
	// DefaultSMTPTimeout bounds connecting and each read and write, as
	// the Perl API's Net::SMTP Timeout does.
	DefaultSMTPTimeout = 10 * time.Second
)

// SetSMTPOpts configures the email notifier from a comma separated list of
// key value pairs, as found in the SMTP_OPTS env var. For example:
//
//	Host,smtp.example.com,Port,465,SSL,1,AuthUser,me,AuthPass,secret,Timeout,10
//
// Recognized keys are Host, Port, SSL, AuthUser, AuthPass and Timeout (in
// seconds, as for the Perl API's Net::SMTP). Anything left unset falls back
// to 127.0.0.1:25 without SSL or authentication, with a 10s timeout.
func (c *NotifierConfig) SetSMTPOpts(opts string) error {
	c.SMTPHost = DefaultSMTPHost
	c.SMTPPort = DefaultSMTPPort
	c.SMTPTimeout = DefaultSMTPTimeout

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
		case "timeout":
			secs, err := strconv.ParseFloat(value, 64)
			if err != nil || secs <= 0 {
				return fmt.Errorf("invalid SMTP timeout %s: want a positive number of seconds", value)
			}
			c.SMTPTimeout = time.Duration(secs * float64(time.Second))
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

// SendEmail emails ms to emailAddress, for user.
func (n *Notifier) SendEmail(ms []*LoggedMessage, emailAddress, user string) error {
	return n.sendMail("notifications@relaychat.party", emailAddress, n.emailMessage(ms, emailAddress, user))
}

// emailMessage is the email of ms, with user's unsubscribe link and the
// one-click List-Unsubscribe headers (RFC 8058).
func (n *Notifier) emailMessage(ms []*LoggedMessage, emailAddress, user string) []byte {
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

	token := unsubscribeToken(n.Config.UnsubscribeSecret, user)
	msg := []byte(fmt.Sprintf("To: %s\r\n", emailAddress) +
		fmt.Sprintf(
			"From: Relaychat Party <notifications@relaychat.party>\n"+
				"Subject: %s\n", subject) +
		fmt.Sprintf("List-Unsubscribe: <%s/api/unsubscribe?token=%s>\n", n.Config.APIURL, token) +
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\n" +
		"Content-Transfer-Encoding: 8bit\n" +
		"X-Mailer: relaychat 0.1\n" +
		"\r\n" +
		"You were mentioned in the following channels:\n\n" +
		strings.Join(lines, "\n\n") + "\n\n" +
		"This message is coming from the relaychat.party IRC client. You enabled email notifications in the client. When someone mentions your name, or uses a word that matches a highlight term you configured, you will recieve an email from us. If you would like to stop recieving these messages, please use this link to unsubscribe: " + n.Config.APIURL + "/unsubscribe/#" + token + "\n\n" +
		"Thank you for using relaychat.party!" +
		"\r\n")

	return msg
}

func (c *NotifierConfig) smtpTimeout() time.Duration {
	if c.SMTPTimeout <= 0 {
		return DefaultSMTPTimeout
	}
	return c.SMTPTimeout
}

// deadlineConn gives every read and write its own deadline, so a server
// that stops answering fails the send, while a slow but working one
// doesn't.
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	c.Conn.SetDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(b)
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	c.Conn.SetDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(b)
}

func (n *Notifier) sendMail(from string, to string, msg []byte) error {
	host, port := n.Config.SMTPAddress()
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	auth := n.Config.SMTPAuth(host)
	timeout := n.Config.smtpTimeout()

	raw, err := (&net.Dialer{Timeout: timeout}).Dial("tcp", addr)
	if err != nil {
		return err
	}
	var conn net.Conn = &deadlineConn{Conn: raw, timeout: timeout}
	tlsConfig := &tls.Config{ServerName: host}

	if n.Config.SMTPSSL {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.Handshake(); err != nil {
			raw.Close()
			return err
		}
		conn = tc
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		raw.Close()
		return err
	}
	defer c.Close()

	// as smtp.SendMail does: upgrade a plain connection when offered
	if !n.Config.SMTPSSL {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConfig); err != nil {
				return err
			}
		}
	}

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
