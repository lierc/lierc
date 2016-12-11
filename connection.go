package lierc

import (
	"bufio"
	"crypto/tls"
	"fmt"
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
	connect   chan *IRCConnectMessage
	quit      chan struct{}
	reader    *bufio.Reader
	socket    net.Conn
	id        string
	debug     bool
	pingfreq  time.Duration
	timeout   time.Duration
	keepalive time.Duration
	lastmsg   time.Time
}

func NewIRCConn(incoming chan *IRCMessage, connect chan *IRCConnectMessage, Id string) *IRCConn {
	irc := &IRCConn{
		incoming:  incoming,
		outgoing:  make(chan string),
		quit:      make(chan struct{}),
		pingfreq:  15 * time.Minute,
		keepalive: 4 * time.Minute,
		timeout:   1 * time.Minute,
		connect:   connect,
		id:        Id,
		debug:     os.Getenv("LIERC_DEBUG") != "",
	}

	return irc
}

func (irc *IRCConn) Connect(server string, ssl bool) error {
	if irc.debug {
		log.Printf("%s Connecting to %s", irc.id, server)
	}

	if ssl {
		conf := &tls.Config{
			InsecureSkipVerify: true,
		}

		c, err := tls.Dial("tcp", server, conf)

		if err != nil {
			if irc.debug {
				log.Printf("%s connection failed: %v", irc.id, err)
			}
			irc.connect <- &IRCConnectMessage{
				Connected: false,
				Message:   err.Error(),
			}
			return err
		}

		irc.socket = c

	} else {
		c, err := net.Dial("tcp", server)

		if err != nil {
			if irc.debug {
				log.Printf("%s connection failed: %v", irc.id, err)
			}
			irc.connect <- &IRCConnectMessage{
				Connected: false,
				Message:   err.Error(),
			}
			return err
		}

		irc.socket = c
	}

	irc.reader = bufio.NewReaderSize(irc.socket, 512)

	if irc.debug {
		log.Printf("%s Connected to %s", irc.id, server)
	}

	irc.connect <- &IRCConnectMessage{
		Connected: true,
	}

	go irc.Send()
	go irc.Recv()

	return nil
}

func (irc *IRCConn) Send() {
	keepalive := time.NewTicker(1 * time.Minute)
	ping := time.NewTicker(irc.pingfreq)

	for {
		select {

		case <-irc.quit:
			return

		case line := <-irc.outgoing:
			irc.socket.SetWriteDeadline(time.Now().Add(irc.timeout))
			_, err := irc.socket.Write([]byte(line + "\r\n"))

			var zero time.Time
			irc.socket.SetWriteDeadline(zero)

			if err != nil {
				if irc.debug {
					log.Printf("%s Error writing %v", irc.id, err)
				}
				irc.connect <- &IRCConnectMessage{
					Connected: false,
					Message:   err.Error(),
				}
				irc.Close()
				return
			}

		case <-keepalive.C:
			if time.Since(irc.lastmsg) >= irc.keepalive {
				irc.outgoing <- fmt.Sprintf("PING %d", time.Now().UnixNano())
			}

		case <-ping.C:
			irc.outgoing <- fmt.Sprintf("PING %d", time.Now().UnixNano())
		}
	}
}

func (irc *IRCConn) Close() {
	close(irc.quit)
	if irc.socket != nil {
		irc.socket.Close()
	}
}

func (irc *IRCConn) Socket() net.Conn {
	return irc.socket
}

func (irc *IRCConn) Recv() {
	for {
		select {

		case <-irc.quit:
			return

		default:
			if irc.socket != nil {
				irc.socket.SetReadDeadline(time.Now().Add(irc.timeout + irc.pingfreq))
			}

			line, err := irc.reader.ReadString('\n')

			if irc.socket != nil {
				var zero time.Time
				irc.socket.SetReadDeadline(zero)
			}

			if err != nil {
				if irc.debug {
					log.Printf("%s Error reading %v", irc.id, err)
				}
				irc.Close()
				irc.connect <- &IRCConnectMessage{
					Connected: false,
					Message:   err.Error(),
				}
				return
			}

			line = strings.TrimSuffix(line, "\r\n")
			message := ParseIRCMessage(line)
			irc.Lock()
			irc.lastmsg = time.Now()
			irc.Unlock()
			irc.incoming <- message
		}
	}
}
