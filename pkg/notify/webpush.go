package notify

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type WebPushConfig struct {
	Endpoint string
	Key      string
	Auth     string
	User     string
}

// WebPush is the push payload read by the UI's service-worker.js. Data
// holds as many of the messages as fit the push size limit, trimmed to
// what the service worker shows; Count is how many there were in all.
type WebPush struct {
	To    string        `json:"to"`
	Count int           `json:"count"`
	Data  []pushMessage `json:"data"`
}

// pushMessage is the part of a LoggedMessage the service worker reads.
type pushMessage struct {
	ConnectionId string
	MessageId    int
	Message      pushIRCMessage
}

type pushIRCMessage struct {
	Prefix  pushPrefix
	Command string
	Params  []string
	Direct  bool
	Time    float64
}

type pushPrefix struct {
	Name string
}

const (
	// maxPushPayload is the largest payload webpush-go sends: a 4096 byte
	// record less the 86 byte header, 16 byte tag and 1 byte delimiter.
	maxPushPayload = 4096 - 86 - 16 - 1
	// maxPushText is how much of a message's text goes in a push, in runes.
	maxPushText = 300
	// pushTTL is how long the push service keeps trying to reach a device
	// that's asleep or offline.
	pushTTL = 4 * time.Hour
)

// pushPayload encodes ms for one push, keeping as many messages as fit in
// maxPushPayload. The first message is always kept.
func pushPayload(to string, ms []*LoggedMessage) ([]byte, error) {
	payload := &WebPush{To: to, Count: len(ms), Data: []pushMessage{}}
	var body []byte

	for _, m := range ms {
		payload.Data = append(payload.Data, trimForPush(m))
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		if len(b) > maxPushPayload && len(payload.Data) > 1 {
			break
		}
		body = b
	}

	if len(body) > maxPushPayload {
		return nil, fmt.Errorf("push payload is %d bytes, over %d", len(body), maxPushPayload)
	}
	return body, nil
}

func trimForPush(m *LoggedMessage) pushMessage {
	params := []string{}
	if len(m.Message.Params) > 0 {
		params = append(params, m.Message.Params[0])
	}
	if len(m.Message.Params) > 1 {
		text := m.Message.Params[1]
		if r := []rune(text); len(r) > maxPushText {
			text = string(r[:maxPushText]) + "…"
		}
		params = append(params, text)
	}
	pm := pushMessage{
		ConnectionId: m.ConnectionId,
		MessageId:    m.MessageId,
		Message: pushIRCMessage{
			Command: m.Message.Command,
			Params:  params,
			Direct:  m.Message.Direct,
			Time:    m.Message.Time,
		},
	}
	if m.Message.Prefix != nil {
		pm.Message.Prefix.Name = m.Message.Prefix.Name
	}
	return pm
}

// SendWebPush sends m to one subscription and returns the push service's
// response, with its body already read.
func (n *Notifier) SendWebPush(m []*LoggedMessage, c *WebPushConfig) (*http.Response, []byte, error) {
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

	payload, err := pushPayload(id, m)
	if err != nil {
		return nil, nil, err
	}

	res, err := webpush.SendNotification(payload, sub, &webpush.Options{
		Subscriber:      n.Config.APIURL,
		TTL:             int(pushTTL / time.Second),
		Urgency:         webpush.UrgencyHigh,
		VAPIDPrivateKey: n.Config.VAPIDPrivateKey,
		VAPIDPublicKey:  n.Config.VAPIDPublicKey,
	})

	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, nil, err
	}

	return res, body, nil
}

// WebPushGone reports whether a push service's response status means the
// subscription no longer exists and should be deleted. Other failures, such
// as rate limiting, outages or a rejected VAPID key, leave it in place.
func WebPushGone(status int) bool {
	return status == http.StatusNotFound || status == http.StatusGone
}
