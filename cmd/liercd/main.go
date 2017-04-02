package main

import (
	"encoding/json"
	"fmt"
	"github.com/lierc/lierc/pkg/lierc"
	"github.com/lierc/lierc/pkg/liercd"
	"github.com/nsqio/go-nsq"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-state-from-stdin" {
		state, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(os.Stderr, "Read state: %s\n", string(state))
	}

	quit := make(chan bool)
	manager := liercd.NewClientManager()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM)
	signal.Notify(c, syscall.SIGHUP)

	go func() {
		signal := <-c

		if signal == syscall.SIGTERM {
			var wg = &sync.WaitGroup{}
			var timer = time.AfterFunc(3*time.Second, func() {
				fmt.Fprintf(os.Stderr, "Failed to gracefully close all connections.\n")
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
			fmt.Fprintf(os.Stderr, "Exited cleanly\n")
			os.Exit(0)
		} else if signal == syscall.SIGHUP {
			fmt.Fprintf(os.Stderr, "got HUP\n")
			manager.Exec()
		}
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
			case client := <-lierc.Status:
				event := manager.ConnectEvent(client)
				json, _ := json.Marshal(event)
				w.Publish("chats", json)
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
