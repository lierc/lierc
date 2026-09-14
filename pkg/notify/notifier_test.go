package notify

import (
	"testing"
	"time"
)

func TestSetUserRateLimit(t *testing.T) {
	c := &NotifierConfig{}

	if err := c.SetUserRateLimit(""); err != nil {
		t.Fatalf("empty limit: %s", err)
	}

	if c.UserRateLimit != DefaultUserRateLimit {
		t.Errorf("expected default %s, got %s", DefaultUserRateLimit, c.UserRateLimit)
	}

	if err := c.SetUserRateLimit("30s"); err != nil {
		t.Fatalf("30s: %s", err)
	}

	if c.UserRateLimit != 30*time.Second {
		t.Errorf("expected 30s, got %s", c.UserRateLimit)
	}

	if err := c.SetUserRateLimit("nope"); err == nil {
		t.Errorf("expected error on invalid duration")
	}

	if err := c.SetUserRateLimit("-5m"); err == nil {
		t.Errorf("expected error on negative duration")
	}
}
