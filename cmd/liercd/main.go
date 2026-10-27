package main

import (
	"context"
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

	nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
	w, err := nsq.NewProducer(nsqd, nsq.NewConfig())
	if err != nil {
		log.Fatal(err)
	}

	// go-nsq reconnects on the next publish after nsqd goes away, so
	// retrying failed publishes is all the reconnect logic needed
	p := liercd.NewPublisher(w, liercd.QueueSize)
	go p.Run(context.Background())

	go func() {
		for {
			select {
			case event := <-lierc.Events:
				p.Enqueue("chats", event)
			case multi := <-lierc.Multi:
				p.Enqueue("multi", multi)
			case status := <-lierc.Status:
				p.Enqueue("chats", m.ConnectEvent(status))
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
