package main

import (
	"encoding/json"
	"fmt"
	"github.com/lierc/lierc/pkg/lierc"
	"github.com/lierc/lierc/pkg/liercd"
	"github.com/nsqio/go-nsq"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	quit := make(chan bool)
	m := liercd.NewClientManager()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM)

	go func() {
		<-c
		m.Shutdown()
		os.Exit(0)
	}()

	go func() {
		nsq_config := nsq.NewConfig()
		nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
		w, _ := nsq.NewProducer(nsqd, nsq_config)

		for {
			select {
			case event := <-lierc.Events:
				json, _ := json.Marshal(event)
				w.Publish("chats", json)
			case multi := <-lierc.Multi:
				json, _ := json.Marshal(multi)
				w.Publish("multi", json)
			case status := <-lierc.Status:
				event := m.ConnectEvent(status)
				json, _ := json.Marshal(event)
				w.Publish("chats", json)
			}
		}
	}()

	go func() {
		http.HandleFunc("/", m.HandleCommand)
		log.Print("Listening on port 5005")
		http.ListenAndServe("0.0.0.0:5005", nil)
	}()

	<-quit
}
