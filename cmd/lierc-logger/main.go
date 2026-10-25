package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lierc/lierc/pkg/logger"
)

func main() {
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
