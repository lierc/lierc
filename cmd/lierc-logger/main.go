package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lierc/lierc/pkg/envfile"
	"github.com/lierc/lierc/pkg/logger"
	"github.com/lierc/lierc/pkg/metrics"
)

func main() {
	// Production's secrets are files, named by NAME_FILE.
	getenv, err := envfile.WithFiles(os.Environ(), os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	metrics.Serve(getenv("METRICS_LISTEN"))
	conf := &logger.Config{
		DBUser:   getenv("POSTGRES_USER"),
		DBPass:   getenv("POSTGRES_PASSWORD"),
		DBHost:   getenv("POSTGRES_HOST"),
		DBName:   getenv("POSTGRES_DB"),
		NSQDHost: getenv("NSQD_HOST"),
	}

	l, err := logger.New(conf)
	if err != nil {
		log.Fatal(err)
	}

	// Only a non-nil notifier is set: a nil *notify.Notifier would make a
	// non-nil logger.Notifier.
	if n := newNotifier(getenv, l.DB()); n != nil {
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
