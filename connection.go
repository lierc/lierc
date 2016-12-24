package lierc

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type IRCConn struct {
	sync.Mutex
	incoming  chan *IRCMessage
	outgoing  chan string
	connect   chan *IRCConnectMessage
	end       chan struct{}
	reader    *bufio.Reader
	socket    net.Conn
	id        string
	debug     int64
	pingfreq  time.Duration
	timeout   time.Duration
	keepalive time.Duration
	lastmsg   time.Time
}

func (irc *IRCConn) Connect(server string, ssl bool) error {
	if irc.debug > 0 {
		log.Printf("%s Connecting to %s", irc.id, server)
	}

	var c net.Conn
	var err error

	if ssl {
		conf := &tls.Config{
			InsecureSkipVerify: true,
		}
		c, err = tls.Dial("tcp", server, conf)
	} else {
		c, err = net.Dial("tcp", server)
	}

	if err != nil {
		if irc.debug > 0 {
			log.Printf("%s connection failed: %v", irc.id, err)
		}
		irc.connect <- &IRCConnectMessage{
			Connected: false,
			Message:   err.Error(),
		}
		return err
	}

	irc.socket = c
	irc.reader = bufio.NewReaderSize(irc.socket, 512)

	if irc.debug > 1 {
		log.Printf("%s Connected to %s", irc.id, server)
	}

	irc.connect <- &IRCConnectMessage{
		Connected: true,
	}

	go irc.Send()
	go irc.Recv()
	go irc.Ping()

	return nil
}

func (irc *IRCConn) Ping() {
	keepalive := time.NewTicker(1 * time.Minute)
	ping := time.NewTicker(irc.pingfreq)

	for {
		select {
		case <-irc.end:
			return
		case <-keepalive.C:
			if time.Since(irc.lastmsg) >= irc.keepalive {
				irc.outgoing <- fmt.Sprintf("PING %d", time.Now().UnixNano())
			}
		case <-ping.C:
			irc.outgoing <- fmt.Sprintf("PING %d", time.Now().UnixNano())
		}
	}
}

func (irc *IRCConn) Send() {
	for {
		select {
		case <-irc.end:
			return
		case line := <-irc.outgoing:
			irc.socket.SetWriteDeadline(time.Now().Add(irc.timeout))
			_, err := irc.socket.Write([]byte(line + "\r\n"))

			var zero time.Time
			irc.socket.SetWriteDeadline(zero)

			if err != nil {
				if irc.debug > 1 {
					log.Printf("%s Error writing %v", irc.id, err)
				}
				irc.Error(err)
			}
		}
	}
}

func (irc *IRCConn) Error(err error) {
	close(irc.end)
	irc.connect <- &IRCConnectMessage{
		Connected: false,
		Message:   err.Error(),
	}
}

func (irc *IRCConn) Close() {
	if irc.socket != nil {
		irc.socket.Close()
	}
}

func (irc *IRCConn) PortMap() (error, string, string) {
	if irc.socket != nil {
		_, local, _ := net.SplitHostPort(irc.socket.LocalAddr().String())
		_, remote, _ := net.SplitHostPort(irc.socket.RemoteAddr().String())
		return nil, local, remote
	}

	return fmt.Errorf("Not connected"), "", ""
}

func (irc *IRCConn) Recv() {
	for {
		select {
		case <-irc.end:
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
				if irc.debug > 1 {
					log.Printf("%s Error reading %v", irc.id, err)
				}
				irc.Error(err)
			} else {
				line = strings.TrimSuffix(line, "\r\n")
				message := ParseIRCMessage(line)
				irc.Lock()
				irc.lastmsg = time.Now()
				irc.Unlock()
				irc.incoming <- message
			}
		}
	}
}
