package main

import "testing"

func TestNewNotifier(t *testing.T) {
	if n := newNotifier(func(string) string { return "" }, nil); n == nil {
		t.Fatal("empty env: notifier is nil, want one with defaults")
	}
}

func TestNewNotifierBadConfig(t *testing.T) {
	env := func(k string) string {
		if k == "SMTP_OPTS" {
			return "Host"
		}
		return ""
	}
	if n := newNotifier(env, nil); n != nil {
		t.Fatal("bad SMTP_OPTS: got a notifier, want notifications off")
	}
}
