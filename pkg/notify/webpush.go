package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	webpush "github.com/sherclockholmes/webpush-go"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
)

type WebPushConfig struct {
	Endpoint string
	Key      string
	Auth     string
	User     string
}

type WebPush struct {
	To   string           `json:"to"`
	Data []*LoggedMessage `json:"data"`
}

func (n *Notifier) SendWebPush(m []*LoggedMessage, c *WebPushConfig) (*http.Response, error) {
	parts := strings.Split(c.Endpoint, "/")
	id := parts[len(parts)-1]

	keys := &webpush.Keys{
		Auth:   c.Auth,
		P256dh: c.Key,
	}

	sub := &webpush.Subscription{
		Endpoint: c.Endpoint,
		Keys:     *keys,
	}

	payload := &WebPush{
		To:   id,
		Data: m,
	}

	var buf bytes.Buffer
	err := json.NewEncoder(&buf).Encode(&payload)

	if err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "sending webpush '%s'\n", c.Endpoint)

	res, err := webpush.SendNotification(buf.Bytes(), sub, &webpush.Options{
		Subscriber:      n.Config.APIURL,
		TTL:             10,
		VAPIDPrivateKey: n.Config.VAPIDPrivateKey,
	})

	if err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "%s\n", res.Status)
	body, err := ioutil.ReadAll(res.Body)

	if err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "%s\n", body)

	return res, nil
}
