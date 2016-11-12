package lierc

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

var Multi = make(chan *IRCClientMultiMessage)
var Events = make(chan *IRCClientMessage)
var Connects = make(chan *IRCConnectMessage)

type IRCClient struct {
	conn           *IRCConn
	Config         *IRCConfig
	Channels       map[string]*IRCChannel
	Nick           string
	nickbuff       map[string][]string
	Registered     bool
	ConnectMessage *IRCConnectMessage
	Isupport       []string
	incoming       chan *IRCMessage
	connect        chan *IRCConnectMessage
	quit           chan bool
	quitting       bool
	retries        int
	debug          bool
	Id             string
	timer          *time.Timer
	mu             *sync.Mutex
}

type IRCConnectMessage struct {
	Id        string
	Connected bool
	Message   string
}

type IRCClientMultiMessage struct {
	Id       string
	Message  *IRCMessage
	Channels []string
}

type IRCClientMessage struct {
	Id      string
	Message *IRCMessage
}

func NewIRCClient(config *IRCConfig, Id string) *IRCClient {
	connect := make(chan *IRCConnectMessage)
	incoming := make(chan *IRCMessage)

	client := &IRCClient{
		conn:       NewIRCConn(incoming, connect, Id),
		Config:     config,
		Registered: false,
		Isupport:   make([]string, 0),
		Channels:   make(map[string]*IRCChannel),
		nickbuff:   make(map[string][]string),
		connect:    connect,
		incoming:   incoming,
		debug:      os.Getenv("LIERC_DEBUG") != "",
		Nick:       config.Nick,
		quit:       make(chan bool),
		quitting:   false,
		Id:         Id,
		mu:         &sync.Mutex{},
	}

	go client.Event()
	go client.conn.Connect(config.Server(), config.Ssl)

	return client
}

func (client *IRCClient) Destroy() {
	client.mu.Lock()
	client.quitting = true
	client.mu.Unlock()

	if client.timer != nil {
		client.timer.Stop()
	}

	client.Send("QUIT bye")

	time.AfterFunc(2*time.Second, func() {
		client.quit <- true
	})
}

func (client *IRCClient) Send(line string) {
	if client.debug {
		log.Printf("%s ---> %s", client.Id, line)
	}
	if client.ConnectMessage.Connected {
		client.conn.outgoing <- line
	}
}

func (client *IRCClient) Event() {
	for {
		select {
		case message := <-client.incoming:
			if client.debug {
				log.Printf("%s <--- %s", client.Id, message.Raw)
			}
			if client.Message(message) {
				clientmsg := &IRCClientMessage{
					Id:      client.Id,
					Message: message,
				}
				Events <- clientmsg
			}
		case connect := <-client.connect:
			connect.Id = client.Id

			client.mu.Lock()
			client.ConnectMessage = connect
			client.mu.Unlock()

			Connects <- connect
			if connect.Connected {
				client.Register()
			} else if client.quitting {
				client.conn.quit <- true
				return
			} else {
				client.Reconnect()
			}
		case <-client.quit:
			client.conn.quit <- true
			return
		}
	}
}

func (client *IRCClient) PortMap() (error, string, string) {
	if client.ConnectMessage == nil {
		return errors.New("not connected"), "", ""
	}

	if client.ConnectMessage.Connected && client.conn != nil {
		conn := client.conn.Conn()
		if conn != nil {
			_, local, _ := net.SplitHostPort(conn.LocalAddr().String())
			_, remote, _ := net.SplitHostPort(conn.RemoteAddr().String())
			return nil, local, remote
		}
	}

	return errors.New("not connected"), "", ""
}

func (client *IRCClient) Reconnect() {
	client.mu.Lock()
	defer client.mu.Unlock()

	client.Registered = false
	client.Isupport = make([]string, 0)
	client.retries = client.retries + 1
	delay := 15 * client.retries
	if delay > 300 {
		delay = 300
	}
	seconds := time.Duration(delay) * time.Second
	if client.debug {
		log.Printf("%s Reconnecting in %s", client.Id, seconds)
	}
	client.timer = time.AfterFunc(seconds, func() {
		config := client.Config
		client.conn = NewIRCConn(client.incoming, client.connect, client.Id)
		client.conn.Connect(config.Server(), config.Ssl)
	})
}

func (client *IRCClient) Message(message *IRCMessage) bool {
	client.mu.Lock()
	defer client.mu.Unlock()

	if handler, ok := handlers[message.Command]; ok {
		handler(client, message)
	}

	return true
}

func (client *IRCClient) NickCollision(message *IRCMessage) {
	if !client.Registered {
		client.Nick = client.Nick + "_"
		client.Send(fmt.Sprintf("NICK %s", client.Nick))
	}
}

func (client *IRCClient) Join() {
	for _, channel := range client.Config.Channels {
		if channel != "" {
			client.Send(fmt.Sprintf("JOIN %s", channel))
		}
	}
}

func (client *IRCClient) Welcome() {
	if !client.Registered {
		client.retries = 0
		client.Registered = true
		client.Join()
	}
}

func (client *IRCClient) Register() {
	if client.Config.Pass != "" {
		client.Send(fmt.Sprintf("PASS %s", client.Config.Pass))
	}

	user := client.Config.User
	if user == "" {
		user = client.Config.Nick
	}

	hostname, _ := os.Hostname()
	client.Send(fmt.Sprintf(
		"USER %s %s %s %s",
		user,
		hostname,
		client.Config.Host,
		user,
	))

	client.Send(fmt.Sprintf("NICK %s", client.Config.Nick))
}
