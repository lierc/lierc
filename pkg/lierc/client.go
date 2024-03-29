package lierc

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

var hostname, _ = os.Hostname()

var Multi = make(chan *IRCClientMultiMessage)
var Events = make(chan *IRCClientMessage)
var Status = make(chan *IRCClientStatus)

const (
	CAP_ECHO_MESSAGE = "echo-message"
	CAP_MESSAGE_TAGS = "message-tags"
	CAP_SASL         = "sasl"
)

func getSupportedCaps() []string {
	return []string{CAP_ECHO_MESSAGE, CAP_MESSAGE_TAGS, CAP_SASL}
}

type IRCClient struct {
	sync.RWMutex
	Id            string
	Config        *IRCConfig
	Channels      map[string]*IRCChannel
	Nick          string
	User          string
	Host          string
	Registered    bool
	Caps          map[string]string
	CapsAcked     map[string]struct{}
	Connected     bool
	StatusMessage string
	Isupport      []string
	irc           *IRCConn
	prefix        [][]byte
	chantypes     []byte
	incoming      chan *IRCMessage
	status        chan *IRCClientStatus
	quit          chan struct{}
	quitting      bool
	Retries       int
	debug         int64
	timer         *time.Timer
	wg            *sync.WaitGroup
}

type IRCClientStatus struct {
	Connected bool
	Message   string
	Id        string
	Host      string
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
	status := make(chan *IRCClientStatus)
	incoming := make(chan *IRCMessage)

	c := &IRCClient{
		Config:        config,
		Registered:    false,
		Isupport:      make([]string, 0),
		Channels:      make(map[string]*IRCChannel),
		Connected:     false,
		Caps:          make(map[string]string),
		CapsAcked:     make(map[string]struct{}),
		StatusMessage: "Connecting",
		status:        status,
		incoming:      incoming,
		debug:         LogLevel(os.Getenv("LIERC_DEBUG")),
		Nick:          config.Nick,
		User:          config.User,
		Host:          hostname,
		quit:          make(chan struct{}),
		quitting:      false,
		Id:            Id,
		prefix: [][]byte{
			[]byte{'@', 'o'},
			[]byte{'+', 'v'},
			[]byte{'%', 'h'},
		},
		chantypes: []byte{'#', '&'},
	}

	c.irc = c.CreateConn()
	Status <- c.Status()

	go c.Event()
	go c.irc.Connect(config.Server(), config.Ssl)

	return c
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
		debug:     c.debug,
		limiter:   rate.NewLimiter(1, 4),
	}
}

func (c *IRCClient) Destroy() {
	c.Lock()
	c.quitting = true
	c.StatusMessage = "Closing connection"
	c.wg = &sync.WaitGroup{}
	c.Unlock()

	c.wg.Add(1)

	if c.timer != nil {
		c.timer.Stop()
	}

	timer := time.AfterFunc(2*time.Second, func() {
		c.irc.Close()
		c.wg.Done()
	})

	Status <- c.Status()

	c.Send("QUIT bye")
	c.wg.Wait()
	timer.Stop()
	close(c.quit)
}

func (c *IRCClient) Status() *IRCClientStatus {
	return &IRCClientStatus{
		Id:        c.Id,
		Message:   c.StatusMessage,
		Connected: c.Connected,
		Host:      c.Config.Host,
	}
}

func (c *IRCClient) Send(line string) {
	if c.debug > 1 {
		log.Printf("%s ---> %s", c.Id, line)
	}
	if c.irc.Connected {
		c.irc.outgoing <- line
	}
}

func (c *IRCClient) Event() {
	for {
		select {
		case m := <-c.incoming:
			if c.debug > 1 {
				log.Printf("%s <--- %s", c.Id, m.Raw)
			}
			if c.Message(m) {
				cmsg := &IRCClientMessage{
					Id:      c.Id,
					Message: m,
				}
				Events <- cmsg
			}
		case status := <-c.status:
			c.Lock()
			c.Connected = status.Connected
			c.StatusMessage = status.Message
			c.Unlock()

			Status <- c.Status()

			if c.Connected {
				c.Register()
			} else if !c.quitting {
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
	c.Connected = false
	c.StatusMessage = fmt.Sprintf("Reconnecting in %s", seconds)

	Status <- c.Status()

	if c.debug > 0 {
		log.Printf("%s %s", c.Id, c.StatusMessage)
	}

	c.timer = time.AfterFunc(seconds, func() {
		c.Lock()
		config := c.Config
		c.irc = c.CreateConn()
		c.Unlock()

		c.irc.Connect(config.Server(), config.Ssl)
	})
}

func (c *IRCClient) Message(m *IRCMessage) bool {
	c.Lock()
	defer c.Unlock()

	if handler, ok := handlers[m.Command]; ok {
		handler(c, m)
	}

	return true
}

func (c *IRCClient) NickCollision(m *IRCMessage) {
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
	c.CapStart()

	user := c.Config.User
	if user == "" {
		user = c.Config.Nick
	}

	if !c.Config.SASL && c.Config.Pass != "" {
		c.Send(fmt.Sprintf("PASS %s", c.Config.Pass))
	}

	c.Send(fmt.Sprintf("NICK %s", c.Config.Nick))
	c.Send(fmt.Sprintf(
		"USER %s %s %s %s",
		user,
		hostname,
		c.Config.Host,
		user,
	))
}

func (c *IRCClient) CapNotSupported() {
	if c.Config.SASL {
		c.irc.Close()
	}
}

func (c *IRCClient) CapAdd(caps []string) {
	for _, v := range caps {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) == 2 {
			c.Caps[parts[0]] = parts[1]
		} else {
			c.Caps[v] = ""
		}
	}
}

func (c *IRCClient) CapDel(caps []string) {
	for _, v := range caps {
		delete(c.Caps, v)
	}
}

func (c *IRCClient) CapListDone() {
	var capReq []string

	if c.Config.SASL && !c.CapAvailable(CAP_SASL) {
		c.irc.Close()
		return
	}

	for _, cp := range getSupportedCaps() {
		if c.CapAvailable(cp) {
			capReq = append(capReq, cp)
		}
	}

	if len(capReq) > 0 {
		c.Send("CAP REQ :" + strings.Join(capReq, " "))
	} else {
		c.Send("CAP END")
	}
}

func (c *IRCClient) CapAvailable(name string) bool {
	_, ok := c.Caps[name]
	return ok
}

func (c *IRCClient) CapEnabled(name string) bool {
	_, ok := c.CapsAcked[name]
	return ok
}

func (c *IRCClient) CapAck(caps []string) {
	for _, v := range caps {
		if strings.HasPrefix(v, "-") {
			delete(c.CapsAcked, v[1:])
		} else {
			c.CapsAcked[v] = struct{}{}
		}
	}

	if !c.Registered && c.Config.SASL {
		if c.CapEnabled(CAP_SASL) {
			c.Send("AUTHENTICATE PLAIN")
		} else {
			c.irc.Close()
		}
	} else {
		c.Send("CAP END")
	}
}

func (c *IRCClient) SASLAuthSuccess() {
	c.Send("CAP END")
}

func (c *IRCClient) SASLAuthFailed(s string) {
	c.irc.Close()
}

func (c *IRCClient) CapStart() {
	c.Send("CAP LS 302")
}

func (c *IRCClient) SASLAuth(s string) {
	if s != "+" {
		log.Printf("Unexpected SASL response")
		c.irc.Close()
		return
	}

	in := []byte(c.Config.User)

	in = append(in, 0x0)
	in = append(in, []byte(c.Config.User)...)
	in = append(in, 0x0)
	in = append(in, []byte(c.Config.Pass)...)

	c.Send(fmt.Sprintf("AUTHENTICATE %s", base64.StdEncoding.EncodeToString(in)))
}

func (c *IRCClient) CapNak(caps []string) {
	for _, cp := range caps {
		delete(c.Caps, cp)
	}

	if !c.Registered && c.Config.SASL {
		if !c.CapEnabled(CAP_SASL) {
			c.irc.Close()
		}
	}
}

func (c *IRCClient) NicksWithPrefix(channel *IRCChannel) []string {
	names := make([]string, 0)
	for nick, mode := range channel.Nicks {
		names = append(names, c.NickPrefix(mode)+nick)
	}
	return names
}

func (c *IRCClient) NickPrefix(mode []byte) string {
	for _, mapping := range c.prefix {
		if bytes.IndexByte(mode, mapping[1]) != -1 {
			return string(mapping[0])
		}
	}
	return ""
}

func (c *IRCClient) NickPrefixMode(prefix byte) (byte, bool) {
	for _, mapping := range c.prefix {
		if prefix == mapping[0] {
			return mapping[1], true
		}
	}
	return 0, false
}

func (c *IRCClient) IsNickMode(mode byte) bool {
	for _, mapping := range c.prefix {
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
	Caps       map[string]string
	CapsAcked  []string
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
			Nicks: c.NicksWithPrefix(channel),
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
		Status:     c.Status(),
		Isupport:   c.Isupport,
		Caps:       c.Caps,
		CapsAcked:  c.capsAcked(),
	}
}

func (c *IRCClient) capsAcked() []string {
	var caps []string
	for k, _ := range c.CapsAcked {
		caps = append(caps, k)
	}
	return caps
}
