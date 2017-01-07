package liercd

import (
	"encoding/json"
	"errors"
	"github.com/lierc/lierc/lierc"
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
	mu      *sync.RWMutex
	clients map[string]*lierc.IRCClient
}

func NewClientManager() *ClientManager {
	manager := &ClientManager{
		clients: make(map[string]*lierc.IRCClient),
		mu:      &sync.RWMutex{},
	}

	return manager
}

func (manager *ClientManager) GetClient(uuid string) (*lierc.IRCClient, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()

	if client, ok := manager.clients[uuid]; ok {
		return client, nil
	}

	log.Printf("[Manager] missing client %s", uuid)
	return nil, errors.New("Missing client")
}

func (manager *ClientManager) AddClient(client *lierc.IRCClient) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.clients[client.Id] = client
}

func (manager *ClientManager) RemoveClient(client *lierc.IRCClient) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	delete(manager.clients, client.Id)
}

func (manager *ClientManager) HandleCommand(w http.ResponseWriter, r *http.Request) {
	log.Print("[HTTP] " + r.Method + " " + r.URL.Path)
	parts := strings.SplitN(r.URL.Path, "/", 3)

	if r.URL.Path == "/state" {
		pprof.Lookup("goroutine").WriteTo(os.Stdout, 1)
		io.WriteString(w, "ok")
		return
	}

	if r.URL.Path == "/portmap" {
		manager.mu.RLock()
		defer manager.mu.RUnlock()

		portmap := make([][]string, 0)

		for _, client := range manager.clients {
			err, local, remote := client.PortMap()
			if err == nil {
				user := client.Config.User
				if user == "" {
					user = client.Config.Nick
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

		exists, _ := manager.GetClient(id)
		if exists != nil {
			log.Printf("[Manager] adding client failed, %s", exists.Id)
			http.Error(w, "nok", http.StatusBadRequest)
			return
		}

		client := lierc.NewIRCClient(&config, id)

		log.Printf("[Manager] adding client %s", client.Id)
		manager.AddClient(client)

		io.WriteString(w, "ok")

	case "destroy":
		client, err := manager.GetClient(id)

		if err != nil {
			http.Error(w, "nok", http.StatusNotFound)
			return
		}

		log.Printf("Destroying client")
		client.Destroy()
		manager.RemoveClient(client)

		log.Printf("[Manager] destroyed client %s", id)
		io.WriteString(w, "ok")

	case "raw":
		client, err := manager.GetClient(id)

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
		client.Send(line)

		if len(line) >= 7 && strings.ToUpper(line[:7]) == "PRIVMSG" {
			hostname, _ := os.Hostname()
			prefix := ":" + client.Nick + "!" + client.Config.User + "@" + hostname
			Privmsg <- &ClientPrivmsg{
				Id:   id,
				Line: prefix + " " + line,
			}
		}

	case "status":
		client, err := manager.GetClient(id)

		if err != nil {
			http.Error(w, "nok", http.StatusNotFound)
			return
		}

		json, _ := json.Marshal(client)
		io.WriteString(w, string(json))
	}
}
