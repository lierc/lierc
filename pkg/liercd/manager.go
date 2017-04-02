package liercd

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lierc/lierc/pkg/lierc"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
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

func (m *ClientManager) ConnectEvent(c *lierc.IRCClient) *lierc.IRCClientMessage {
	var line = fmt.Sprintf(":%s CONNECT :%s", c.Config.Host, c.Status.Message)
	message := lierc.ParseIRCMessage(line)

	return &lierc.IRCClientMessage{
		Id:      c.Id,
		Message: message,
	}
}

func (m *ClientManager) DisconnectEvent(c *lierc.IRCClient) *lierc.IRCClientMessage {
	var line = fmt.Sprintf(":%s DISCONNECT :%s", c.Config.Host, c.Status.Message)
	message := lierc.ParseIRCMessage(line)

	return &lierc.IRCClientMessage{
		Id:      c.Id,
		Message: message,
	}
}

func (m *ClientManager) PrivmsgEvent(c *lierc.IRCClient, line string) *lierc.IRCClientMessage {
	hostname, _ := os.Hostname()
	prefix := ":" + c.Nick + "!" + c.Config.User + "@" + hostname
	message := lierc.ParseIRCMessage(prefix + " " + line)

	return &lierc.IRCClientMessage{
		Id:      c.Id,
		Message: message,
	}
}

func (m *ClientManager) CreateEvent(c *lierc.IRCClient) *lierc.IRCClientMessage {
	var line = fmt.Sprintf("CREATE %s %s", c.Nick, c.Config.Host)
	message := lierc.ParseIRCMessage(line)

	return &lierc.IRCClientMessage{
		Id:      c.Id,
		Message: message,
	}
}

func (m *ClientManager) DeleteEvent(c *lierc.IRCClient) *lierc.IRCClientMessage {
	var line = fmt.Sprintf("DELETE %s", c.Id)
	message := lierc.ParseIRCMessage(line)

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

		event := m.CreateEvent(c)
		lierc.Events <- event

		io.WriteString(w, "ok")

	case "destroy":
		c, err := m.GetClient(id)

		if err != nil {
			http.Error(w, "nok", http.StatusNotFound)
			return
		}

		log.Printf("Destroying c")
		event := m.DeleteEvent(c)
		lierc.Events <- event
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
		c.Send(line)

		if len(line) >= 7 && strings.ToUpper(line[:7]) == "PRIVMSG" {
			event := m.PrivmsgEvent(c, line)
			event.Message.Prefix.Self = true
			lierc.Events <- event
		}

	case "status":
		c, err := m.GetClient(id)

		if err != nil {
			http.Error(w, "nok", http.StatusNotFound)
			return
		}

		json, _ := json.Marshal(c.ClientData())
		io.WriteString(w, string(json))
	}
}