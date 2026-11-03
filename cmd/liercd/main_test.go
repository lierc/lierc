package main

import (
	"net"
	"strings"
	"testing"
)

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

// An address already in use is an error, not a liercd running without its
// command API.
func TestListenAddressInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if ln, err := listen(taken.Addr().String()); err == nil {
		ln.Close()
		t.Fatal("listen on a taken address succeeded")
	} else if !strings.Contains(err.Error(), "command API") {
		t.Errorf("err = %v", err)
	}
	ln, err := listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}
