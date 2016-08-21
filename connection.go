package lierc

import (
	"bufio"
	"crypto/tls"
	"log"
	"net"
	"strings"
)

type IRCConn struct {
	incoming chan *IRCMessage
	outgoing chan string
	connect  chan bool
	quit     chan bool
	rw       *bufio.ReadWriter
	conn     net.Conn
	tls      *tls.Conn
	id       string
	closing  bool
}

func NewIRCConn(incoming chan *IRCMessage, connect chan bool, Id string) *IRCConn {
	irc := &IRCConn{
		incoming: incoming,
		outgoing: make(chan string),
		quit:     make(chan bool),
		connect:  connect,
		id:       Id,
	}

	return irc
}

func (conn *IRCConn) Connect(server string, ssl bool) error {
	log.Printf("%s Connecting to %s", conn.id, server)

	conf := &tls.Config{
		InsecureSkipVerify: true,
	}

	if ssl {
		c, err := tls.Dial("tcp", server, conf)

		if err != nil {
			log.Printf("%s connection failed: %v", conn.id, err)
			conn.connect <- false
			return err
		}

		conn.tls = c
		conn.rw = bufio.NewReadWriter(
			bufio.NewReader(conn.tls),
			bufio.NewWriter(conn.tls),
		)

	} else {
		c, err := net.Dial("tcp", server)

		if err != nil {
			log.Printf("%s connection failed: %v", conn.id, err)
			conn.connect <- false
			return err
		}

		conn.conn = c
		conn.rw = bufio.NewReadWriter(
			bufio.NewReader(conn.conn),
			bufio.NewWriter(conn.conn),
		)

	}

	log.Printf("%s Connected to %s", conn.id, server)

	conn.connect <- true

	go conn.Send()
	go conn.Recv()

	return nil
}

func (conn *IRCConn) Send() {
	for {
		select {
		case line := <-conn.outgoing:
			_, err := conn.rw.WriteString(line + "\r\n")

			if err != nil {
				log.Printf("%s Error writing %v", conn.id, err)
				if !conn.closing {
					conn.Close()
					conn.connect <- false
				}
				return
			}

			conn.rw.Flush()

		case <-conn.quit:
			conn.Close()
			return
		}
	}
}

func (conn *IRCConn) Close() {
	conn.closing = true

	if conn.tls != nil {
		conn.tls.Close()
	}
	if conn.conn != nil {
		conn.conn.Close()
	}
}

func (conn *IRCConn) Recv() {
	for {
		line, err := conn.rw.ReadString('\n')

		if err != nil {
			log.Printf("%s Error reading %v", conn.id, err)
			if !conn.closing {
				conn.Close()
				conn.connect <- false
			}
			return
		}

		line = strings.TrimSuffix(line, "\r\n")
		message := ParseIRCMessage(line)
		conn.incoming <- message
	}
}
