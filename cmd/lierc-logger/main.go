package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lierc/lierc/pkg/logger"
	"github.com/lierc/lierc/pkg/metrics"
)

func main() {
	metrics.Serve(os.Getenv("METRICS_LISTEN"))
	conf := &logger.Config{
		DBUser:   os.Getenv("POSTGRES_USER"),
		DBPass:   os.Getenv("POSTGRES_PASSWORD"),
		DBHost:   os.Getenv("POSTGRES_HOST"),
		DBName:   os.Getenv("POSTGRES_DB"),
		NSQDHost: os.Getenv("NSQD_HOST"),
	}

	l, err := logger.New(conf)
	if err != nil {
		log.Fatal(err)
	}

	// Only a non-nil notifier is set: a nil *notify.Notifier would make a
	// non-nil logger.Notifier.
	if n := newNotifier(os.Getenv, l.DB()); n != nil {
		go n.Accumulate()
		l.SetNotifier(n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := l.Start(ctx); err != nil {
		if ctx.Err() != nil {
			log.Print("stopped while starting")
			return
		}
		log.Fatal(err)
	}

	fmt.Print("Ready!\n")
	<-ctx.Done()

	log.Print("stopping")
	l.Stop()
}
