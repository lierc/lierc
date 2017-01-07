package main

import (
	"encoding/json"
	"fmt"
	"github.com/lierc/lierc/lierc"
	"github.com/lierc/lierc/liercd"
	"github.com/nsqio/go-nsq"
	"log"
	"net/http"
	"os"
)

func main() {
	quit := make(chan bool)
	manager := liercd.NewClientManager()

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
			case connect := <-lierc.Connects:
				json, _ := json.Marshal(connect)
				w.Publish("connect", json)
			case privmsg := <-liercd.Privmsg:
				json, _ := json.Marshal(privmsg)
				w.Publish("privmsg", json)
			}
		}
	}()

	go func() {
		http.HandleFunc("/", manager.HandleCommand)
		log.Print("Listening on port 5005")
		http.ListenAndServe("0.0.0.0:5005", nil)
	}()

	<-quit
}
