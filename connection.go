package lierc

import (
	"bufio"
	"crypto/tls"
	"log"
	"net"
	"os"
	"strings"
)

type IRCConn struct {
	incoming chan *IRCMessage
	outgoing chan string
	connect  chan *IRCConnectMessage
	quit     chan bool
	reader   *bufio.Reader
	conn     net.Conn
	id       string
	debug    bool
}

func NewIRCConn(incoming chan *IRCMessage, connect chan *IRCConnectMessage, Id string) *IRCConn {
	irc := &IRCConn{
		incoming: incoming,
		outgoing: make(chan string),
		quit:     make(chan bool),
		connect:  connect,
		id:       Id,
		debug:    os.Getenv("LIERC_DEBUG") != "",
	}

	return irc
}

func (conn *IRCConn) Connect(server string, ssl bool) error {
	if conn.debug {
		log.Printf("%s Connecting to %s", conn.id, server)
	}

	if ssl {
		conf := &tls.Config{
			InsecureSkipVerify: true,
		}

		c, err := tls.Dial("tcp", server, conf)

		if err != nil {
			if conn.debug {
				log.Printf("%s connection failed: %v", conn.id, err)
			}
			conn.connect <- &IRCConnectMessage{
				Connected: false,
				Message:   err.Error(),
			}
			return err
		}

		conn.conn = c

	} else {
		c, err := net.Dial("tcp", server)

		if err != nil {
			if conn.debug {
				log.Printf("%s connection failed: %v", conn.id, err)
			}
			conn.connect <- &IRCConnectMessage{
				Connected: false,
				Message:   err.Error(),
			}
			return err
		}

		conn.conn = c
	}

	conn.reader = bufio.NewReader(conn.conn)

	if conn.debug {
		log.Printf("%s Connected to %s", conn.id, server)
	}

	conn.connect <- &IRCConnectMessage{
		Connected: true,
	}

	go conn.Send()
	go conn.Recv()

	return nil
}

func (conn *IRCConn) Send() {
	for {
		select {
		case line := <-conn.outgoing:
			_, err := conn.conn.Write([]byte(line + "\r\n"))

			if err != nil {
				if conn.debug {
					log.Printf("%s Error writing %v", conn.id, err)
				}
				conn.Close()
				conn.connect <- &IRCConnectMessage{
					Connected: false,
					Message:   err.Error(),
				}
				return
			}

		case <-conn.quit:
			conn.Close()
			return
		}
	}
}

func (conn *IRCConn) Close() {
	if conn.conn != nil {
		conn.conn.Close()
	}
}

func (conn *IRCConn) Conn() net.Conn {
	return conn.conn
}

func (conn *IRCConn) Recv() {
	for {
		line, err := conn.reader.ReadString('\n')

		if err != nil {
			if conn.debug {
				log.Printf("%s Error reading %v", conn.id, err)
			}
			conn.Close()
			conn.connect <- &IRCConnectMessage{
				Connected: false,
				Message:   err.Error(),
			}
			return
		}

		line = strings.TrimSuffix(line, "\r\n")
		message := ParseIRCMessage(line)
		conn.incoming <- message
	}
}
