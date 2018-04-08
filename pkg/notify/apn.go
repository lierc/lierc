package notify

import (
	"errors"
	"fmt"
	"github.com/Coccodrillo/apns"
	"net/url"
	"os"
)

type APNConfig struct {
	DeviceToken string
	User        string
}

type APNPayload struct {
	Alert   *APNAlert `json:"alert"`
	URLArgs []string  `json:"url-args"`
}

type APNAlert struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Action string `json:"action"`
}

func (n *Notifier) SendAPNS(m []*LoggedMessage, c *APNConfig) error {
	cert := os.Getenv("APN_CERT_FILE")
	key := os.Getenv("APN_KEY_FILE")
	message := m[0]

	client := apns.NewClient("gateway.push.apple.com:2195", string(cert), string(key))

	fmt.Fprintf(os.Stderr, "Sending APNS notification %s\n", c.DeviceToken)
	pn := apns.NewPushNotification()
	pn.DeviceToken = c.DeviceToken

	payload := &APNPayload{}
	payload.URLArgs = []string{
		url.QueryEscape(message.ConnectionId),
		url.QueryEscape(message.Message.Params[0]),
	}
	payload.Alert = &APNAlert{
		Title:  fmt.Sprintf("%s in %s", message.Message.Prefix.Name, message.Message.Params[0]),
		Body:   message.Message.Params[1],
		Action: "View",
	}

	pn.Set("aps", payload)

	resp := client.Send(pn)

	if !resp.Success {
		if resp.Error != nil {
			return resp.Error
		}
		return errors.New("Unknown error")
	}

	return nil
}
