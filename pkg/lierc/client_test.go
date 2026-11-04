package lierc

import (
	"bufio"
	"strings"
	"testing"
	"time"
)

// A server that never closes the connection after QUIT, so Destroy closes
// it itself after destroyTimeout.
func TestDestroyWhenServerIgnoresQuit(t *testing.T) {
	old := retryUnit
	retryUnit = 20 * time.Millisecond
	t.Cleanup(func() { retryUnit = old })
	srv := newFakeServer(t)
	d := drain(t)
	c := newTestClient(t, d, srv.config("me"))
	fc := srv.accept(t)
	fc.register(t, "me")
	waitRegistered(t, c)

	quit := make(chan struct{})
	go func() {
		r := bufio.NewReader(fc)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "QUIT") {
				close(quit)
			}
		}
	}()

	start := time.Now()
	c.Destroy()
	if elapsed := time.Since(start); elapsed > destroyTimeout+time.Second {
		t.Errorf("Destroy took %s", elapsed)
	}
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Error("server never got QUIT")
	}
}
