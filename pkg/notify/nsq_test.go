package notify

import (
	"errors"
	"testing"
	"time"
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
