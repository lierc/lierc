package main

import (
	"bufio"
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
	for _, cmd := range []string{"liercd", "lierc-identd"} {
		t.Run(cmd, func(t *testing.T) {
			bin := filepath.Join(dir, cmd)
			if out, err := exec.Command("go", "build", "-o", bin, "../"+cmd).CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, out)
			}
			// Port 0: the binary picks a free port and logs it, so no other
			// process can take it between choosing and listening.
			p := exec.Command(bin)
			p.Env = []string{"METRICS_LISTEN=127.0.0.1:0", "NSQD_HOST=127.0.0.2", "POSTGRES_HOST=127.0.0.1:1", "LISTEN=127.0.0.1:0"}
			out, err := p.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			p.Stdout = p.Stderr
			if err := p.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { p.Process.Kill(); p.Wait() }()

			const marker = "metrics on "
			found := make(chan string, 1)
			go func() {
				sc := bufio.NewScanner(out)
				for sc.Scan() {
					line := sc.Text()
					i := strings.Index(line, marker+"127.0.0.1:")
					if i < 0 {
						continue
					}
					// identd logs without timestamps (journald adds
					// them), from its first line on
					if cmd == "lierc-identd" && i != 0 {
						t.Errorf("identd's line has a prefix: %q", line)
					}
					found <- line[i+len(marker):]
					return
				}
				found <- ""
			}()
			select {
			case addr := <-found:
				if addr == "" {
					t.Fatalf("%s exited without serving metrics", cmd)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: no metrics log line", cmd)
			}
		})
	}
}
