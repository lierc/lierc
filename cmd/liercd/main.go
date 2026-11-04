package main

import (
	"context"
	"fmt"
	"github.com/lierc/lierc/pkg/liercd"
	"github.com/nsqio/go-nsq"
	"log"
	"net"
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

// listen binds the command API's address, before anything else starts,
// so an address already in use (another liercd) is an error at startup.
func listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("command API: %w", err)
	}
	return ln, nil
}

// The address for the command API, from LISTEN (e.g. 127.0.0.1:5005).
func listenAddr() string {
	if addr := os.Getenv("LISTEN"); addr != "" {
		return addr
	}
	return "0.0.0.0:5005"
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	nsqd := fmt.Sprintf("%s:4150", os.Getenv("NSQD_HOST"))
	w, err := nsq.NewProducer(nsqd, nsq.NewConfig())
	if err != nil {
		log.Fatal(err)
	}

	// go-nsq reconnects on the next publish after nsqd goes away, so
	// retrying failed publishes is all the reconnect logic needed
	p := liercd.NewPublisher(w, liercd.QueueSize)
	m := liercd.NewClientManager(p)

	// Run stops only once the IRC connections are closed, so that their
	// DISCONNECT events are queued before the final flush
	running, stopRunning := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		p.Run(running)
		close(ran)
	}()

	http.HandleFunc("/", m.HandleCommand)
	addr := listenAddr()
	ln, err := listen(addr)
	if err != nil {
		// without the command API nothing can create or reach a
		// connection, so don't run on silently
		log.Fatal(err)
	}
	log.Printf("Listening on %s", addr)
	go func() {
		// the API failing later is as bad as it failing to start
		log.Fatal(http.Serve(ln, nil))
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
