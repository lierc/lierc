package liercd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"github.com/lierc/lierc/pkg/lierc"
)

var Privmsg = make(chan *ClientPrivmsg)

type ClientPrivmsg struct {
	Id   string
	Line string
}

type ClientManager struct {
	Clients map[string]*lierc.IRCClient
	sync.RWMutex
}

func (m *ClientManager) Shutdown() {
	var wg = &sync.WaitGroup{}

	var timer = time.AfterFunc(3*time.Second, func() {
		fmt.Fprintf(os.Stderr, "Failed to gracefully close all connections.")
		os.Exit(1)
	})

	for _, c := range m.Clients {
		wg.Add(1)
		go func() {
			c.Destroy()
			wg.Done()
		}()
	}

	wg.Wait()
	timer.Stop()
	fmt.Fprintf(os.Stderr, "Exited cleanly")
}

func NewClientManager() *ClientManager {
	m := &ClientManager{
		Clients: make(map[string]*lierc.IRCClient),
	}

	return m
}

func (m *ClientManager) GetClient(uuid string) (*lierc.IRCClient, error) {
	m.RLock()
	defer m.RUnlock()

	if c, ok := m.Clients[uuid]; ok {
		return c, nil
	}

	log.Printf("[Manager] missing c %s", uuid)
	return nil, errors.New("Missing c")
}

func (m *ClientManager) AddClient(c *lierc.IRCClient) {
	m.Lock()
	defer m.Unlock()
	m.Clients[c.Id] = c
}

func (m *ClientManager) RemoveClient(c *lierc.IRCClient) {
	m.Lock()
	defer m.Unlock()
	delete(m.Clients, c.Id)
}

func (m *ClientManager) ConnectEvent(s *lierc.IRCClientStatus) *lierc.IRCClientMessage {
	var event string
	if s.Connected {
		event = "CONNECT"
	} else {
		event = "DISCONNECT"
	}

	var line = fmt.Sprintf(":%s %s :%s", s.Host, event, s.Message)
	message, err := lierc.ParseIRCMessage(line)

	if err != nil {
		log.Printf("[Manager] unparseable event %q: %v", line, err)
		return nil
	}

	return &lierc.IRCClientMessage{
		Id:      s.Id,
		Message: message,
	}
}

func (m *ClientManager) PrivmsgEvent(c *lierc.IRCClient, line string) *lierc.IRCClientMessage {
	message, err := lierc.ParseIRCMessage(":" + c.Source() + " " + line)

	if err != nil {
		log.Printf("[Manager] unparseable event %q: %v", line, err)
		return nil
	}

	return &lierc.IRCClientMessage{
		Id:      c.Id,
		Message: message,
	}
}

func (m *ClientManager) CreateEvent(c *lierc.IRCClient) *lierc.IRCClientMessage {
	var line = fmt.Sprintf("CREATE %s :%s", c.CurrentNick(), c.Config.Display())
	message, err := lierc.ParseIRCMessage(line)

	if err != nil {
		log.Printf("[Manager] unparseable event %q: %v", line, err)
		return nil
	}

	return &lierc.IRCClientMessage{
		Id:      c.Id,
		Message: message,
	}
}

func (m *ClientManager) DeleteEvent(c *lierc.IRCClient) *lierc.IRCClientMessage {
	var line = fmt.Sprintf("DELETE %s", c.Id)
	message, err := lierc.ParseIRCMessage(line)

	if err != nil {
		log.Printf("[Manager] unparseable event %q: %v", line, err)
		return nil
	}

	return &lierc.IRCClientMessage{
		Id:      c.Id,
		Message: message,
	}
}

func (m *ClientManager) HandleCommand(w http.ResponseWriter, r *http.Request) {
	log.Print("[HTTP] " + r.Method + " " + r.URL.Path)
	parts := strings.SplitN(r.URL.Path, "/", 3)

	if r.URL.Path == "/state" {
		pprof.Lookup("goroutine").WriteTo(os.Stdout, 1)
		io.WriteString(w, "ok")
		return
	}

	if r.URL.Path == "/portmap" {
		m.RLock()
		defer m.RUnlock()

		portmap := make([][]string, 0)

		for _, c := range m.Clients {
			err, local, remote := c.PortMap()
			if err == nil {
				user := c.Config.User
				if user == "" {
					user = c.Config.Nick
				}
				portmap = append(portmap, []string{user, local, remote})
			}
		}

		json, _ := json.Marshal(portmap)
		io.WriteString(w, string(json))
		return
	}

	if len(parts) < 3 {
		io.WriteString(w, "Invalid request")
		return
	}

	id := parts[1]
	action := parts[2]

	switch action {
	case "create":
		decoder := json.NewDecoder(r.Body)
		config := lierc.IRCConfig{}
		err := decoder.Decode(&config)

		if err != nil {
			log.Printf("%v", err)
			http.Error(w, "nok", http.StatusBadRequest)
			return
		}

		exists, _ := m.GetClient(id)
		if exists != nil {
			log.Printf("[Manager] adding c failed, %s", exists.Id)
			http.Error(w, "nok", http.StatusBadRequest)
			return
		}

		c := lierc.NewIRCClient(&config, id)

		log.Printf("[Manager] adding c %s", c.Id)
		m.AddClient(c)

		if event := m.CreateEvent(c); event != nil {
			lierc.Events <- event
		}

		io.WriteString(w, "ok")

	case "destroy":
		c, err := m.GetClient(id)

		if err != nil {
			http.Error(w, "nok", http.StatusNotFound)
			return
		}

		log.Printf("Destroying c")
		if event := m.DeleteEvent(c); event != nil {
			lierc.Events <- event
		}
		c.Destroy()
		m.RemoveClient(c)

		log.Printf("[Manager] destroyed c %s", id)
		io.WriteString(w, "ok")

	case "raw":
		c, err := m.GetClient(id)

		if err != nil {
			http.Error(w, "nok", http.StatusNotFound)
			return
		}

		raw, err := ioutil.ReadAll(r.Body)

		if err != nil {
			http.Error(w, "nok", http.StatusNotFound)
			return
		}

		line := string(raw)
		// until the connection is set up we can't tell whether the
		// server will echo a message, or save a nick change it confirms
		if needsRegistration(line) && !c.IsRegistered() {
			http.Error(w, notRegistered, http.StatusConflict)
			return
		}

		// before sending, so the server's reply can't arrive first
		if nick, ok := requestedNick(line); ok {
			c.RequestNick(nick)
		}
		queued := c.Send(line)

		if shouldEcho(line, queued, c.CapEnabled("echo-message")) {
			if event := m.PrivmsgEvent(c, line); event != nil {
				event.Message.Prefix.Self = true
				lierc.Events <- event
			}
		}

	case "status":
		c, err := m.GetClient(id)

		if err != nil {
			http.Error(w, "nok", http.StatusNotFound)
			return
		}

		// the handlers change channels and config with c locked
		c.RLock()
		json, _ := json.Marshal(c.ClientData())
		c.RUnlock()
		io.WriteString(w, string(json))
	}
}

// requestedNick is the nick a raw "NICK <nick>" line asks for.
func requestedNick(line string) (string, bool) {
	f := strings.Fields(line)
	if len(f) < 2 || !strings.EqualFold(f[0], "NICK") {
		return "", false
	}
	nick := strings.TrimPrefix(f[1], ":")
	return nick, nick != ""
}

// notRegistered is the error for a message or nick change sent before the
// connection is set up. lierc-api shows it to the user as it is.
const notRegistered = "Not connected yet, try again in a moment"

// command is a raw line's command, uppercased, after any tags and
// prefix; "" if the line doesn't parse.
func command(line string) string {
	m, err := lierc.ParseIRCMessage(line)
	if err != nil {
		return ""
	}
	return m.Command
}

// isMessage reports whether a raw line is a PRIVMSG or NOTICE.
func isMessage(line string) bool {
	cmd := command(line)
	return cmd == "PRIVMSG" || cmd == "NOTICE"
}

// needsRegistration reports whether a raw line from the API is refused
// until the connection is set up: a PRIVMSG or NOTICE, which we can't yet
// tell whether to echo, or a NICK. Other lines (typing notifications,
// JOIN, raw commands) are sent if connected and dropped otherwise, as
// before.
func needsRegistration(line string) bool {
	cmd := command(line)
	return cmd == "PRIVMSG" || cmd == "NOTICE" || cmd == "NICK"
}

// shouldEcho reports whether a raw line from the API is echoed back as our
// own message: a PRIVMSG or NOTICE that was queued for the server, which
// won't echo it (echo-message not acked). A line dropped while
// disconnected was never sent, so it isn't echoed.
func shouldEcho(line string, queued, echoAcked bool) bool {
	return queued && isMessage(line) && !echoAcked
}
