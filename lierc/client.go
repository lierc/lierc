package lierc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

var Multi = make(chan *IRCClientMultiMessage)
var Events = make(chan *IRCClientMessage)
var Connects = make(chan *IRCConnectMessage)

type IRCClient struct {
	sync.Mutex
	Id             string
	Config         *IRCConfig
	Channels       map[string]*IRCChannel
	Nick           string
	Registered     bool
	ConnectMessage *IRCConnectMessage
	Isupport       []string
	irc            *IRCConn
	prefix         [][]byte
	chantypes      []byte
	incoming       chan *IRCMessage
	connect        chan *IRCConnectMessage
	quit           chan struct{}
	quitting       bool
	Retries        int
	debug          int64
	timer          *time.Timer
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

func LogLevel(env string) int64 {
	if env == "" {
		return 0
	} else {
		i, err := strconv.ParseInt(env, 10, 0)
		if err != nil {
			panic(err)
		}
		return i
	}
}

func NewIRCClient(config *IRCConfig, Id string) *IRCClient {
	connect := make(chan *IRCConnectMessage)
	incoming := make(chan *IRCMessage)

	client := &IRCClient{
		Config:     config,
		Registered: false,
		Isupport:   make([]string, 0),
		Channels:   make(map[string]*IRCChannel),
		ConnectMessage: &IRCConnectMessage{
			Connected: false,
		},
		connect:  connect,
		incoming: incoming,
		debug:    LogLevel(os.Getenv("LIERC_DEBUG")),
		Nick:     config.Nick,
		quit:     make(chan struct{}),
		quitting: false,
		Id:       Id,
		prefix: [][]byte{
			[]byte{'@', 'o'},
			[]byte{'+', 'v'},
			[]byte{'%', 'h'},
		},
		chantypes: []byte{'#', '&'},
	}

	client.irc = client.CreateConn()
	Connects <- client

	go client.Event()
	go client.irc.Connect(config.Server(), config.Ssl)

	return client
}

func (client *IRCClient) CreateConn() *IRCConn {
	return &IRCConn{
		incoming:  client.incoming,
		outgoing:  make(chan string),
		end:       make(chan struct{}),
		pingfreq:  15 * time.Minute,
		keepalive: 4 * time.Minute,
		timeout:   1 * time.Minute,
		connect:   client.connect,
		id:        client.Id,
		debug:     client.debug,
	}
}

func (client *IRCClient) Destroy() {
	client.Lock()
	client.quitting = true
	client.Unlock()

	if client.timer != nil {
		client.timer.Stop()
	}

	time.AfterFunc(2*time.Second, func() {
		client.irc.Close()
		close(client.quit)
	})

	client.Send("QUIT bye")
}

func (client *IRCClient) Send(line string) {
	if client.debug > 1 {
		log.Printf("%s ---> %s", client.Id, line)
	}
	if client.ConnectMessage.Connected {
		client.irc.outgoing <- line
	}
}

func (client *IRCClient) Event() {
	for {
		select {
		case message := <-client.incoming:
			if client.debug > 1 {
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

			client.Lock()
			client.ConnectMessage = connect
			client.Unlock()

			Connects <- client
			if connect.Connected {
				client.Register()
			} else if !client.quitting {
				client.Reconnect()
			}
		case <-client.quit:
			return
		}
	}
}

func (client *IRCClient) PortMap() (error, string, string) {
	if client.irc != nil {
		return client.irc.PortMap()
	}
	return fmt.Errorf("Not connected"), "", ""
}

func (client *IRCClient) Reconnect() {
	client.Lock()
	defer client.Unlock()

	client.Registered = false
	client.Isupport = make([]string, 0)
	client.Retries = client.Retries + 1
	delay := 15 * client.Retries
	if delay > 300 {
		delay = 300
	}
	seconds := time.Duration(delay) * time.Second
	if client.debug > 0 {
		log.Printf("%s Reconnecting in %s", client.Id, seconds)
	}
	client.timer = time.AfterFunc(seconds, func() {
		config := client.Config
		client.irc = client.CreateConn()
		client.irc.Connect(config.Server(), config.Ssl)
	})
}

func (client *IRCClient) Message(message *IRCMessage) bool {
	client.Lock()
	defer client.Unlock()

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
		client.Retries = 0
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

func (client *IRCClient) Nicks(channel *IRCChannel) []string {
	names := make([]string, 0)
	for nick, mode := range channel.Nicks {
		names = append(names, client.NickPrefix(mode)+nick)
	}
	return names
}

func (client *IRCClient) NickPrefix(mode []byte) string {
	for _, mapping := range client.prefix {
		if bytes.IndexByte(mode, mapping[1]) != -1 {
			return string(mapping[0])
		}
	}
	return ""
}

func (client *IRCClient) NickPrefixMode(prefix byte) (byte, bool) {
	for _, mapping := range client.prefix {
		if prefix == mapping[0] {
			return mapping[1], true
		}
	}
	return 0, false
}

func (client *IRCClient) IsNickMode(mode byte) bool {
	for _, mapping := range client.prefix {
		if mode == mapping[1] {
			return true
		}
	}
	return false
}

type IRCChannelJSON struct {
	Name  string
	Nicks []string
	Topic *IRCTopic
	Mode  string
}

func (client *IRCClient) MarshalJSON() ([]byte, error) {
	channels := make([]*IRCChannelJSON, 0)

	for _, channel := range client.Channels {
		data := &IRCChannelJSON{
			Name:  channel.Name,
			Nicks: client.Nicks(channel),
			Topic: channel.Topic,
			Mode:  "+" + string(channel.Mode),
		}
		channels = append(channels, data)
	}

	return json.Marshal(&struct {
		Id             string
		Config         *IRCConfig
		Nick           string
		Channels       []*IRCChannelJSON
		Registered     bool
		ConnectMessage *IRCConnectMessage
		Isupport       []string
	}{
		Id:             client.Id,
		Config:         client.Config,
		Nick:           client.Nick,
		Channels:       channels,
		Registered:     client.Registered,
		ConnectMessage: client.ConnectMessage,
		Isupport:       client.Isupport,
	})
}
