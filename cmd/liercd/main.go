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
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	quit := make(chan bool)
	manager := liercd.NewClientManager()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM)

	go func() {
		<-c
		var wg = &sync.WaitGroup{}
		var timer = time.AfterFunc(3*time.Second, func() {
			fmt.Fprintf(os.Stderr, "Failed to gracefully close all connections.")
			os.Exit(1)
		})
		for _, client := range manager.Clients {
			wg.Add(1)
			go func() {
				client.Destroy()
				wg.Done()
			}()
		}
		wg.Wait()
		timer.Stop()
		fmt.Fprintf(os.Stderr, "Exited cleanly")
		os.Exit(0)
	}()

	go func() {
		nsq_config := nsq.NewConfig()
		nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
		w, _ := nsq.NewProducer(nsqd, nsq_config)

		type LiercEvent struct {
			Type string
			Data interface{}
		}

		for {
			select {
			case event := <-lierc.Events:
				e := &LiercEvent{
					Type: "chats",
					Data: event,
				}
				json, _ := json.Marshal(e)
				w.Publish("liercd", json)
			case multi := <-lierc.Multi:
				e := &LiercEvent{
					Type: "multi",
					Data: multi,
				}
				json, _ := json.Marshal(e)
				w.Publish("liercd", json)
			case connect := <-lierc.Connects:
				e := &LiercEvent{
					Type: "connect",
					Data: connect,
				}
				json, _ := json.Marshal(e)
				w.Publish("liercd", json)
			case privmsg := <-liercd.Privmsg:
				e := &LiercEvent{
					Type: "privmsg",
					Data: privmsg,
				}
				json, _ := json.Marshal(e)
				w.Publish("liercd", json)
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
