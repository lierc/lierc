package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
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

// SendEmail emails ms to emailAddress, for user.
func (n *Notifier) SendEmail(ms []*LoggedMessage, emailAddress, user string) error {
	m, err := n.emailMessage(ms, emailAddress, user)
	if err != nil {
		return err
	}
	return n.sendMail(m)
}

// emailMessage is the email of ms, with user's unsubscribe link and the
// one-click List-Unsubscribe headers (RFC 8058).
func (n *Notifier) emailMessage(ms []*LoggedMessage, emailAddress, user string) (*gomail.Msg, error) {
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

	m := gomail.NewMsg()
	if err := m.FromFormat("Relaychat Party", "notifications@relaychat.party"); err != nil {
		return nil, err
	}
	if err := m.To(emailAddress); err != nil {
		return nil, err
	}
	m.Subject(subject)
	m.SetUserAgent("relaychat")
	m.SetGenHeader("List-Unsubscribe", "<"+n.Config.APIURL+"/api/unsubscribe?token="+token+">")
	m.SetGenHeader("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	m.SetBodyString(gomail.TypeTextPlain,
		"You were mentioned in the following channels:\n\n"+
			strings.Join(lines, "\n\n")+"\n\n"+
			"This message is coming from the relaychat.party IRC client. You enabled email notifications in the client. When someone mentions your name, or uses a word that matches a highlight term you configured, you will recieve an email from us. If you would like to stop recieving these messages, please use this link to unsubscribe: "+n.Config.APIURL+"/unsubscribe/#"+token+"\n\n"+
			"Thank you for using relaychat.party!\n")
	return m, nil
}

func (c *NotifierConfig) smtpTimeout() time.Duration {
	if c.SMTPTimeout <= 0 {
		return DefaultSMTPTimeout
	}
	return c.SMTPTimeout
}

// sendMail sends m: implicit TLS with SMTPSSL, otherwise STARTTLS when
// the server offers it, and PLAIN auth when SMTPAuthUser is set (only over
// TLS, unless the server is localhost).
func (n *Notifier) sendMail(m *gomail.Msg) error {
	host, port := n.Config.SMTPAddress()
	timeout := n.Config.smtpTimeout()
	opts := []gomail.Option{
		gomail.WithPort(port),
		gomail.WithTimeout(timeout),
		gomail.WithDialContextFunc(smtpDialer(host, n.Config.SMTPSSL, timeout)),
		gomail.WithSMTPAuth(gomail.SMTPAuthNoAuth),
	}
	if n.Config.SMTPSSL {
		// smtpDialer does the TLS; this keeps go-mail from trying STARTTLS
		opts = append(opts, gomail.WithSSL())
	} else {
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	}
	if n.Config.SMTPAuthUser != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
			gomail.WithUsername(n.Config.SMTPAuthUser), gomail.WithPassword(n.Config.SMTPAuthPass))
	}
	c, err := gomail.NewClient(host, opts...)
	if err != nil {
		return err
	}
	return c.DialAndSend(m)
}

// smtpDialer connects with a deadline on every read and write, so a server
// that stops answering fails the send, while a slow but working one
// doesn't (go-mail's own timeout is for the whole session). With ssl it
// does the TLS handshake, which go-mail leaves to a custom dialer.
func smtpDialer(host string, ssl bool, timeout time.Duration) gomail.DialContextFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		conn := &deadlineConn{Conn: raw, timeout: timeout}
		if !ssl {
			return conn, nil
		}
		tc := tls.Client(conn, &tls.Config{ServerName: host})
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return tc, nil
	}
}

// deadlineConn gives every read and write its own deadline, replacing the
// one go-mail sets for the session.
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
