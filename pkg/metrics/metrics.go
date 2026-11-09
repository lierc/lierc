// Package metrics serves a process's Go runtime and process metrics (and
// any metrics registered with github.com/VictoriaMetrics/metrics) for
// vmagent to scrape, on a loopback-only listener.
package metrics

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
)

// Serve serves GET /metrics on addr (METRICS_LISTEN), in the background.
// An empty addr serves nothing. An addr that isn't a loopback IP is
// refused: identd's own port is public, and these must never be. Any
// failure is logged and Serve returns: metrics are optional, the process's
// real work isn't.
func Serve(addr string) {
	if addr == "" {
		return
	}
	ln, err := listen(addr)
	if err != nil {
		log.Printf("metrics: not serving on %s: %v", addr, err)
		return
	}
	log.Printf("metrics on %s", ln.Addr())
	srv := &http.Server{Handler: handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Printf("metrics: serving on %s: %v", ln.Addr(), err)
		}
	}()
}

// listen listens on addr, which must be a loopback IP and port.
func listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %v", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("%q is not a loopback IP", host)
	}
	return net.Listen("tcp", addr)
}

func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		vm.WritePrometheus(w, true)
	})
	return mux
}
