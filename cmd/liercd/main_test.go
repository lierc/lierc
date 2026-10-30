package main

import "testing"

func TestListenAddrDefault(t *testing.T) {
	t.Setenv("LISTEN", "")
	if got := listenAddr(); got != "0.0.0.0:5005" {
		t.Errorf("listenAddr() = %q, want 0.0.0.0:5005", got)
	}
}

func TestListenAddrFromEnv(t *testing.T) {
	t.Setenv("LISTEN", "127.0.0.1:5005")
	if got := listenAddr(); got != "127.0.0.1:5005" {
		t.Errorf("listenAddr() = %q, want 127.0.0.1:5005", got)
	}
}
