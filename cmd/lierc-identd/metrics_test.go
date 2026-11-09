package main

import (
	"bufio"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each lierc process serves metrics on METRICS_LISTEN as the first thing
// in main: run each binary and wait for metrics.Serve's log line. Most of
// them can't get further here (no :113, no nsqd, no Postgres), which is
// why the metrics call has to come first.
func TestMainServesMetrics(t *testing.T) {
	dir := t.TempDir()
	for _, cmd := range []string{"liercd", "lierc-logger", "lierc-notify", "lierc-identd"} {
		t.Run(cmd, func(t *testing.T) {
			bin := filepath.Join(dir, cmd)
			if out, err := exec.Command("go", "build", "-o", bin, "../"+cmd).CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, out)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			ln.Close()

			p := exec.Command(bin)
			p.Env = []string{"METRICS_LISTEN=" + addr, "NSQD_HOST=127.0.0.2", "POSTGRES_HOST=127.0.0.1:1", "LISTEN=127.0.0.1:0"}
			out, err := p.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			p.Stdout = p.Stderr
			if err := p.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { p.Process.Kill(); p.Wait() }()

			found := make(chan bool, 1)
			go func() {
				sc := bufio.NewScanner(out)
				for sc.Scan() {
					if strings.Contains(sc.Text(), "metrics on "+addr) {
						found <- true
						return
					}
				}
				found <- false
			}()
			select {
			case ok := <-found:
				if !ok {
					t.Fatalf("%s exited without serving metrics on %s", cmd, addr)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: no metrics log line", cmd)
			}
		})
	}
}
