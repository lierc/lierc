package lierc

import (
	"bytes"
	"fmt"
	"golang.org/x/time/rate"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

var Multi = make(chan *IRCClientMultiMessage)
var Events = make(chan *IRCClientMessage)
var Status = make(chan *IRCClient)

type IRCClient struct {
	sync.Mutex
	Id         string
	Config     *IRCConfig
	Channels   map[string]*IRCChannel
	Nick       string
	Registered bool
	Status     *IRCClientStatus
	Isupport   []string
	Prefix     [][]byte
	Chantypes  []byte
	Quitting   bool
	Retries    int
	Debug      int64
	irc        *IRCConn
	incoming   chan *IRCMessage
	status     chan *IRCClientStatus
	quit       chan struct{}
	timer      *time.Timer
	wg         *sync.WaitGroup
}

type IRCClientStatus struct {
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
	c := &IRCClient{
		Config:     config,
		Registered: false,
		Isupport:   make([]string, 0),
		Channels:   make(map[string]*IRCChannel),
		Status: &IRCClientStatus{
			Connected: false,
			Message:   "Connecting",
		},
		Debug:    LogLevel(os.Getenv("LIERC_DEBUG")),
		Nick:     config.Nick,
		Quitting: false,
		Id:       Id,
		Prefix: [][]byte{
			[]byte{'@', 'o'},
			[]byte{'+', 'v'},
			[]byte{'%', 'h'},
		},
		Chantypes: []byte{'#', '&'},
	}

	c.Init()
	Status <- c

	go c.irc.Connect(config.Server(), config.Ssl)

	return c
}

func (c *IRCClient) Init() {
	c.status = make(chan *IRCClientStatus)
	c.incoming = make(chan *IRCMessage)
	c.quit = make(chan struct{})

	c.irc = c.CreateConn()
	go c.Event()
}

func (c *IRCClient) Resume(conn net.Conn) {
	go c.irc.Resume(conn, c.Config.Ssl)
}

func (c *IRCClient) CreateConn() *IRCConn {
	return &IRCConn{
		incoming:  c.incoming,
		outgoing:  make(chan string),
		end:       make(chan struct{}),
		pingfreq:  15 * time.Minute,
		keepalive: 4 * time.Minute,
		timeout:   1 * time.Minute,
		status:    c.status,
		id:        c.Id,
		debug:     c.Debug,
		limiter:   rate.NewLimiter(1, 4),
	}
}

func (c *IRCClient) Destroy() {
	c.Lock()
	c.Quitting = true
	c.wg = &sync.WaitGroup{}
	c.Status.Message = "Closing connection"
	c.Unlock()

	c.wg.Add(1)

	if c.timer != nil {
		c.timer.Stop()
	}

	timer := time.AfterFunc(2*time.Second, func() {
		c.irc.Close()
		c.wg.Done()
	})

	Status <- c

	c.Send("QUIT bye")
	c.wg.Wait()
	timer.Stop()
	close(c.quit)
}

func (c *IRCClient) Send(line string) {
	if c.Debug > 1 {
		log.Printf("%s ---> %s", c.Id, line)
	}
	if c.Status.Connected {
		c.irc.outgoing <- line
	}
}

func (c *IRCClient) Event() {
	for {
		select {
		case message := <-c.incoming:
			if c.Debug > 1 {
				log.Printf("%s <--- %s", c.Id, message.Raw)
			}
			if c.Message(message) {
				clientmsg := &IRCClientMessage{
					Id:      c.Id,
					Message: message,
				}
				Events <- clientmsg
			}
		case status := <-c.status:
			c.Lock()
			c.Status = status
			c.Unlock()

			Status <- c
			if status.Connected {
				c.Register()
			} else if !c.Quitting {
				c.Reconnect()
			} else {
				c.wg.Done()
			}
		case <-c.quit:
			return
		}
	}
}

func (c *IRCClient) PortMap() (error, string, string) {
	if c.irc != nil {
		return c.irc.PortMap()
	}
	return fmt.Errorf("Not connected"), "", ""
}

func (c *IRCClient) ConnFile() (error, *os.File) {
	return c.irc.ConnFile()
}

func (c *IRCClient) Reconnect() {
	c.Lock()
	defer c.Unlock()

	c.Registered = false
	c.Isupport = make([]string, 0)
	c.Retries = c.Retries + 1
	delay := 15 * c.Retries
	if delay > 300 {
		delay = 300
	}
	seconds := time.Duration(delay) * time.Second
	if c.Debug > 0 {
		log.Printf("%s Reconnecting in %s", c.Id, seconds)
	}

	c.Status = &IRCClientStatus{
		Connected: false,
		Message:   fmt.Sprintf("Reconnecting in %s", seconds),
	}

	Status <- c

	c.timer = time.AfterFunc(seconds, func() {
		config := c.Config
		c.irc = c.CreateConn()
		c.irc.Connect(config.Server(), config.Ssl)
	})
}

func (c *IRCClient) Message(message *IRCMessage) bool {
	c.Lock()
	defer c.Unlock()

	if handler, ok := handlers[message.Command]; ok {
		handler(c, message)
	}

	return true
}

func (c *IRCClient) NickCollision(message *IRCMessage) {
	if !c.Registered {
		c.Nick = c.Nick + "_"
		c.Send(fmt.Sprintf("NICK %s", c.Nick))
	}
}

func (c *IRCClient) Join() {
	for _, channel := range c.Config.Channels {
		if channel != "" {
			c.Send(fmt.Sprintf("JOIN %s", channel))
		}
	}
}

func (c *IRCClient) Welcome() {
	if !c.Registered {
		c.Retries = 0
		c.Registered = true
		c.Join()
	}
}

func (c *IRCClient) Register() {
	if c.Config.Pass != "" {
		c.Send(fmt.Sprintf("PASS %s", c.Config.Pass))
	}

	user := c.Config.User
	if user == "" {
		user = c.Config.Nick
	}

	hostname, _ := os.Hostname()
	c.Send(fmt.Sprintf(
		"USER %s %s %s %s",
		user,
		hostname,
		c.Config.Host,
		user,
	))

	c.Send(fmt.Sprintf("NICK %s", c.Config.Nick))
}

func (c *IRCClient) Nicks(channel *IRCChannel) []string {
	names := make([]string, 0)
	for nick, mode := range channel.Nicks {
		names = append(names, c.NickPrefix(mode)+nick)
	}
	return names
}

func (c *IRCClient) NickPrefix(mode []byte) string {
	for _, mapping := range c.Prefix {
		if bytes.IndexByte(mode, mapping[1]) != -1 {
			return string(mapping[0])
		}
	}
	return ""
}

func (c *IRCClient) NickPrefixMode(prefix byte) (byte, bool) {
	for _, mapping := range c.Prefix {
		if prefix == mapping[0] {
			return mapping[1], true
		}
	}
	return 0, false
}

func (c *IRCClient) IsNickMode(mode byte) bool {
	for _, mapping := range c.Prefix {
		if mode == mapping[1] {
			return true
		}
	}
	return false
}

type IRCClientData struct {
	Id         string
	Config     *IRCConfig
	Nick       string
	Channels   []*IRCChannelData
	Registered bool
	Status     *IRCClientStatus
	Isupport   []string
}

type IRCChannelData struct {
	Name  string
	Nicks []string
	Topic *IRCTopic
	Mode  string
}

func (c *IRCClient) ClientData() *IRCClientData {
	channels := make([]*IRCChannelData, 0)

	for _, channel := range c.Channels {
		data := &IRCChannelData{
			Name:  channel.Name,
			Nicks: c.Nicks(channel),
			Topic: channel.Topic,
			Mode:  "+" + string(channel.Mode),
		}
		channels = append(channels, data)
	}

	return &IRCClientData{
		Id:         c.Id,
		Config:     c.Config,
		Nick:       c.Nick,
		Channels:   channels,
		Registered: c.Registered,
		Status:     c.Status,
		Isupport:   c.Isupport,
	}
}
