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

// Destroy quits with QUIT_MESSAGE, or "bye" without it.
func TestQuitMessage(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"https://relaychat.party", "QUIT https://relaychat.party"},
		{"see you later", "QUIT :see you later"},
		{"", "QUIT bye"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Setenv("QUIT_MESSAGE", tc.env)
			fastRetries(t)
			srv := newFakeServer(t)
			d := drain(t)
			c := newTestClient(t, d, srv.config("me"))
			fc := srv.accept(t)
			fc.register(t, "me")
			waitRegistered(t, c)

			done := make(chan struct{})
			go func() {
				c.Destroy()
				close(done)
			}()
			if got := fc.expect(t, "QUIT"); got != tc.want {
				t.Errorf("sent %q, want %q", got, tc.want)
			}
			fc.close()
			<-done
		})
	}
}
