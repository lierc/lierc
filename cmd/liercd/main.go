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
	"time"
)

// How long to keep publishing queued events after closing the IRC
// connections. docker stop kills the container 10s after SIGTERM, and
// closing the connections can take 3s.
const flushTimeout = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	m := liercd.NewClientManager()

	nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
	w, err := nsq.NewProducer(nsqd, nsq.NewConfig())
	if err != nil {
		log.Fatal(err)
	}

	// go-nsq reconnects on the next publish after nsqd goes away, so
	// retrying failed publishes is all the reconnect logic needed
	p := liercd.NewPublisher(w, liercd.QueueSize)

	// Run stops only once the IRC connections are closed, so that their
	// DISCONNECT events are queued before the final flush
	running, stopRunning := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		p.Run(running)
		close(ran)
	}()

	// Keeps going through shutdown: closing a client blocks until its
	// status is received here
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

	<-ctx.Done()
	log.Print("stopping")
	m.Shutdown()

	stopRunning()
	<-ran

	flush, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if left := p.Flush(flush); left > 0 {
		log.Printf("exiting with %d events unpublished", left)
	}
}
