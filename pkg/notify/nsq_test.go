package notify

import (
	"errors"
	"testing"
	"time"

	"github.com/nsqio/go-nsq"
)

func TestConsumerConfigReconnectsSoon(t *testing.T) {
	if got := consumerConfig().LookupdPollInterval; got != 5*time.Second {
		t.Fatalf("LookupdPollInterval = %s, want 5s", got)
	}
	if err := consumerConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryUntilSuccess(t *testing.T) {
	saved := reconnectInterval
	reconnectInterval = time.Millisecond
	defer func() { reconnectInterval = saved }()

	calls := 0
	retry("test", func() error {
		calls++
		if calls < 3 {
			return errors.New("connection refused")
		}
		return nil
	})
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestHandleHighlight(t *testing.T) {
	n := &Notifier{Queue: make(chan *LoggedMessage, 1)}

	if err := n.handleHighlight(&nsq.Message{Body: []byte("not json")}); err != nil {
		t.Fatalf("unreadable message: %v", err)
	}
	if len(n.Queue) != 0 {
		t.Fatal("unreadable message was queued")
	}

	// Without a sender or text, email and web push can't be built.
	for _, body := range []string{
		`{"ConnectionId":"c1","MessageId":7}`,
		`{"ConnectionId":"c1","Message":{"Params":["#c","hi"]}}`,
		`{"ConnectionId":"c1","Message":{"Prefix":{"Name":"bob"},"Params":["#c"]}}`,
	} {
		if err := n.handleHighlight(&nsq.Message{Body: []byte(body)}); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if len(n.Queue) != 0 {
			t.Fatalf("queued %s", body)
		}
	}

	if err := n.handleHighlight(&nsq.Message{Body: []byte(`{"ConnectionId":"c1","MessageId":7,"Message":{"Prefix":{"Name":"bob"},"Params":["#c","hi"]}}`)}); err != nil {
		t.Fatal(err)
	}
	if m := <-n.Queue; m.ConnectionId != "c1" || m.MessageId != 7 {
		t.Fatalf("queued %+v", m)
	}
}
