package notify

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal plain SMTP server. Each reply waits delay; if
// stallAfterGreeting, it greets and then never answers.
type fakeSMTP struct {
	ln                 net.Listener
	delay              time.Duration
	stallAfterGreeting bool
	silent             bool // accept and never say anything

	mu   sync.Mutex
	data string
}

func startSMTP(t *testing.T, f *fakeSMTP) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	var conns []net.Conn
	var mu sync.Mutex
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	if f.silent {
		return
	}
	r := bufio.NewReader(c)
	reply := func(s string) {
		time.Sleep(f.delay)
		c.Write([]byte(s + "\r\n"))
	}
	reply("220 fake ESMTP")
	if f.stallAfterGreeting {
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch cmd := strings.ToUpper(strings.Fields(line + " x")[0]); cmd {
		case "EHLO", "HELO":
			reply("250 fake")
		case "MAIL", "RCPT", "RSET", "NOOP":
			reply("250 ok")
		case "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			c.Close()
			return
		default:
			reply("502 no")
		}
	}
}

func smtpNotifier(t *testing.T, f *fakeSMTP, timeout time.Duration) *Notifier {
	host, port, _ := net.SplitHostPort(f.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return &Notifier{Config: &NotifierConfig{SMTPHost: host, SMTPPort: p, SMTPTimeout: timeout}}
}

func TestSMTPTimeoutOpt(t *testing.T) {
	c := &NotifierConfig{}
	if err := c.SetSMTPOpts(""); err != nil || c.SMTPTimeout != 10*time.Second {
		t.Errorf("default: %s %v", c.SMTPTimeout, err)
	}
	if err := c.SetSMTPOpts("Host,h,Timeout,3"); err != nil || c.SMTPTimeout != 3*time.Second {
		t.Errorf("Timeout,3: %s %v", c.SMTPTimeout, err)
	}
	if err := c.SetSMTPOpts("Timeout,0.5"); err != nil || c.SMTPTimeout != 500*time.Millisecond {
		t.Errorf("Timeout,0.5: %s %v", c.SMTPTimeout, err)
	}
	for _, bad := range []string{"Timeout,0", "Timeout,-1", "Timeout,soon"} {
		if err := c.SetSMTPOpts(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if (&NotifierConfig{}).smtpTimeout() != DefaultSMTPTimeout {
		t.Error("zero config doesn't fall back to the default")
	}
}

func TestSendMailDelivers(t *testing.T) {
	f := startSMTP(t, &fakeSMTP{})
	n := smtpNotifier(t, f, time.Second)
	if err := n.sendMail("from@example.com", "to@example.com", []byte("Subject: hi\r\n\r\nhello\r\n")); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.data, "hello") {
		t.Errorf("server got %q", f.data)
	}
}

// The timeout is per read and write: a server slower than the timeout in
// total, but not on any one reply, still gets the mail.
func TestSendMailSlowServer(t *testing.T) {
	f := startSMTP(t, &fakeSMTP{delay: 60 * time.Millisecond})
	n := smtpNotifier(t, f, 200*time.Millisecond)
	start := time.Now()
	if err := n.sendMail("from@example.com", "to@example.com", []byte("hello\r\n")); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("took %s; the server should be slower than the timeout in total", d)
	}
}

func TestSendMailTimesOut(t *testing.T) {
	for name, f := range map[string]*fakeSMTP{
		"silent":         {silent: true},
		"after greeting": {stallAfterGreeting: true},
	} {
		f := startSMTP(t, f)
		for _, ssl := range []bool{false, true} {
			n := smtpNotifier(t, f, 200*time.Millisecond)
			n.Config.SMTPSSL = ssl
			start := time.Now()
			err := n.sendMail("from@example.com", "to@example.com", []byte("hello\r\n"))
			if err == nil {
				t.Errorf("%s ssl=%v: no error", name, ssl)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("%s ssl=%v: took %s", name, ssl, d)
			}
		}
	}
}
