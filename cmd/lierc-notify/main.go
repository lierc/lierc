package main

import (
	"fmt"
	"github.com/lierc/lierc/pkg/notify"
	"golang.org/x/time/rate"
	"net/http"
	"os"
	"sync"
	"time"
)

func main() {
	wg := &sync.WaitGroup{}
	wg.Add(1)

	conf := &notify.NotifierConfig{
		DBUser:          os.Getenv("POSTGRES_USER"),
		DBPass:          os.Getenv("POSTGRES_PASSWORD"),
		DBHost:          os.Getenv("POSTGRES_HOST"),
		DBName:          os.Getenv("POSTGRES_DB"),
		APIStats:        os.Getenv("API_STATS"),
		APIKey:          os.Getenv("API_KEY"),
		APIURL:          os.Getenv("API_URL"),
		VAPIDPrivateKey: os.Getenv("VAPID_PRIVATE"),
		NSQDHost:        os.Getenv("NSQD_HOST"),
		Delay:           15 * time.Second,
	}

	notifier := &notify.Notifier{
		Client:        &http.Client{},
		Notifications: make(map[string]*notify.Notification),
		Last:          make(map[string]time.Time),
		Mu:            &sync.Mutex{},
		Limiter:       rate.NewLimiter(1, 5),
		Config:        conf,
		Queue:         make(chan *notify.LoggedMessage),
	}

	go notifier.Accumulate()
	notifier.ListenNSQ()

	fmt.Print("Ready!\n")
	wg.Wait()
}
