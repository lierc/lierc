package lierc

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"golang.org/x/time/rate"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type IRCConn struct {
	sync.Mutex
	incoming  chan *IRCMessage
	outgoing  chan string
	status    chan *IRCClientStatus
	end       chan struct{}
	reader    *bufio.Reader
	socket    net.Conn
	tls       *tls.Conn
	id        string
	debug     int64
	pingfreq  time.Duration
	timeout   time.Duration
	keepalive time.Duration
	lastmsg   time.Time
	limiter   *rate.Limiter
}

func (c *IRCConn) Resume(conn net.Conn, ssl bool) {
	c.SetupConn(conn, ssl)

	go c.Send()
	go c.Recv()
	go c.Ping()
}

func (c *IRCConn) Connect(server string, ssl bool) error {
	if c.debug > 0 {
		log.Printf("%s Connecting to %s", c.id, server)
	}

	conn, err := net.Dial("tcp", server)

	if err != nil {
		if c.debug > 0 {
			log.Printf("%s connection failed: %v", c.id, err)
		}
		c.status <- &IRCClientStatus{
			Connected: false,
			Message:   err.Error(),
		}
		return err
	}

	c.SetupConn(conn, ssl)

	if c.debug > 1 {
		log.Printf("%s Connected to %s", c.id, server)
	}

	c.status <- &IRCClientStatus{
		Connected: true,
		Message:   "Connected",
	}

	go c.Send()
	go c.Recv()
	go c.Ping()

	return nil
}

func (c *IRCConn) SetupConn(conn net.Conn, ssl bool) {
	if ssl {
		conf := &tls.Config{
			InsecureSkipVerify: true,
		}

		tls_conn := tls.Client(conn, conf)
		c.socket = conn
		c.tls = tls_conn
		c.reader = bufio.NewReaderSize(c.tls, 512)
	} else {
		c.socket = conn
		c.reader = bufio.NewReaderSize(c.socket, 512)
	}
}

func (c *IRCConn) Ping() {
	keepalive := time.NewTicker(1 * time.Minute)
	ping := time.NewTicker(c.pingfreq)

	for {
		select {
		case <-c.end:
			return
		case <-keepalive.C:
			if time.Since(c.lastmsg) >= c.keepalive {
				c.outgoing <- fmt.Sprintf("PING %d", time.Now().UnixNano())
			}
		case <-ping.C:
			c.outgoing <- fmt.Sprintf("PING %d", time.Now().UnixNano())
		}
	}
}

func (c *IRCConn) CheckRateLimit() bool {
	rv := c.limiter.Reserve()
	if !rv.OK() {
		return true
	}
	delay := rv.Delay()
	time.Sleep(delay)
	return false
}

func (c *IRCConn) Send() {
	for {
		select {
		case <-c.end:
			return
		case line := <-c.outgoing:
			if c.CheckRateLimit() {
				return
			}

			c.socket.SetWriteDeadline(time.Now().Add(c.timeout))
			_, err := c.socket.Write([]byte(line + "\r\n"))

			var zero time.Time
			c.socket.SetWriteDeadline(zero)

			if err != nil {
				if c.debug > 0 {
					log.Printf("%s Error writing %v", c.id, err)
				}
				c.Error(err)
			}
		}
	}
}

func (c *IRCConn) Error(err error) {
	close(c.end)
	c.Close()
	c.status <- &IRCClientStatus{
		Connected: false,
		Message:   err.Error(),
	}
}

func (c *IRCConn) Close() {
	if c.socket != nil {
		c.socket.Close()
	}
}

func (c *IRCConn) PortMap() (error, string, string) {
	if c.socket != nil {
		_, local, _ := net.SplitHostPort(c.socket.LocalAddr().String())
		_, remote, _ := net.SplitHostPort(c.socket.RemoteAddr().String())
		return nil, local, remote
	}

	return fmt.Errorf("Not connected"), "", ""
}

func (c *IRCConn) ConnFile() (error, *os.File) {
	if c.socket != nil {
		tcp_conn, ok := c.socket.(*net.TCPConn)
		if !ok {
			return fmt.Errorf("Not a TCP conn"), nil
		}

		file, err := tcp_conn.File()
		if err != nil {
			return err, nil
		}
		return nil, file
	}
	return fmt.Errorf("Not connected"), nil
}

func (c *IRCConn) Recv() {
	for {
		select {
		case <-c.end:
			return
		default:
			if c.socket != nil {
				c.socket.SetReadDeadline(time.Now().Add(c.timeout + c.pingfreq))
			}

			line, err := c.reader.ReadString('\n')

			if c.socket != nil {
				var zero time.Time
				c.socket.SetReadDeadline(zero)
			}

			if err != nil {
				if c.debug > 0 {
					log.Printf("%s Error reading %v", c.id, err)
				}
				c.Error(err)
			} else if len(line) > 0 {
				line = strings.TrimSuffix(line, "\r\n")
				message := ParseIRCMessage(line)
				c.Lock()
				c.lastmsg = time.Now()
				c.Unlock()
				c.incoming <- message
			}
		}
	}
}
