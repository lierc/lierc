package liercd

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lierc/lierc/pkg/lierc"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
)

var Privmsg = make(chan *ClientPrivmsg)

type ClientWithFd struct {
	Fd     int
	Name   string
	Client *lierc.IRCClient
}

type ClientPrivmsg struct {
	Id   string
	Line string
}

type ClientManager struct {
	Clients map[string]*lierc.IRCClient
	sync.RWMutex
}

func NewClientManager() *ClientManager {
	return &ClientManager{
		Clients: make(map[string]*lierc.IRCClient),
	}
}

func (m *ClientManager) GetClient(uuid string) (*lierc.IRCClient, error) {
	m.RLock()
	defer m.RUnlock()

	if c, ok := m.Clients[uuid]; ok {
		return c, nil
	}

	log.Printf("[Manager] missing client %s", uuid)
	return nil, errors.New("Missing client")
}

func (m *ClientManager) AddClient(c *lierc.IRCClient) {
	m.Lock()
	defer m.Unlock()
	log.Printf("[Manager] adding client %s", c.Id)
	m.Clients[c.Id] = c
}

func (m *ClientManager) RemoveClient(c *lierc.IRCClient) {
	m.Lock()
	defer m.Unlock()
	log.Printf("[Manager] destroyed client %s", c.Id)
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
			log.Printf("[Manager] adding client failed, %s", exists.Id)
			http.Error(w, "nok", http.StatusBadRequest)
			return
		}

		c := lierc.NewIRCClient(&config, id)

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

		log.Printf("Destroying client")
		event := m.DeleteEvent(c)
		lierc.Events <- event
		c.Destroy()
		m.RemoveClient(c)

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

func (m *ClientManager) Load(state []byte) {
	clients := []*ClientWithFd{}
	err := json.Unmarshal(state, &clients)

	if err != nil {
		panic(err)
	}

	for _, c := range clients {
		c.Client.Init()
		m.AddClient(c.Client)

		if c.Fd != -1 {
			file := os.NewFile(uintptr(c.Fd), c.Name)
			conn, err := net.FileConn(file)
			if err != nil {
				panic(err)
			}
			c.Client.Resume(conn)
		}
	}
}

func (m *ClientManager) Exec() {
	ex, err := os.Executable()
	if err != nil {
		panic(err)
	}

	dir := path.Dir(ex)
	binary := fmt.Sprintf("%s/liercd-state", dir)
	fork_args := []string{}

	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}

	r2, w2, err := os.Pipe()
	if err != nil {
		panic(err)
	}

	files := []uintptr{r.Fd(), w2.Fd()}
	sys := &syscall.SysProcAttr{}
	env := os.Environ()

	fork_attr := &syscall.ProcAttr{
		Dir:   dir,
		Env:   env,
		Files: files,
		Sys:   sys,
	}

	_, err = syscall.ForkExec(binary, fork_args, fork_attr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to fork: %s", err)
		os.Exit(2)
	}

	m.Lock()
	clients := []*ClientWithFd{}

	for _, c := range m.Clients {
		c.Lock()

		client_data := &ClientWithFd{
			Client: c,
		}

		err, file := c.ConnFile()
		defer file.Close()

		if err != nil {
			fmt.Fprintf(os.Stderr, "%s", err)
			client_data.Fd = -1
		} else {
			syscall.RawSyscall(
				syscall.SYS_FCNTL,
				file.Fd(),
				syscall.F_SETFD,
				0,
			)
			client_data.Fd = int(file.Fd())
			client_data.Name = file.Name()
		}

		clients = append(clients, client_data)
	}

	json, err := json.Marshal(clients)
	if err != nil {
		panic(err)
	}

	w.Write(json)
	w.Close()

	syscall.Dup2(int(r2.Fd()), int(os.Stdin.Fd()))
	exec_args := []string{ex, "-state-from-stdin"}
	err = syscall.Exec(ex, exec_args, env)
	panic(err)
}
