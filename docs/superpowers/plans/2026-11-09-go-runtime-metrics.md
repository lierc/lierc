# Go Runtime Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** liercd, the logger, the notifier and identd serve Go runtime and process metrics on loopback ports, and the existing vmagent sends them to Grafana Cloud.

**Architecture:** A new `pkg/metrics` in lierc has `Serve(addr)`, which serves `GET /metrics` from `github.com/VictoriaMetrics/metrics` on a loopback-only listener. Each process calls it with `METRICS_LISTEN`. relaychat-infra sets `METRICS_LISTEN` per unit and adds vmagent scrape jobs.

**Tech Stack:** Go 1.27, `github.com/VictoriaMetrics/metrics` v1.44.1, Ansible, vmagent.

**Spec:** `docs/superpowers/specs/2026-11-09-go-runtime-metrics-design.md` (lierc)

## Global Constraints

- Library: `github.com/VictoriaMetrics/metrics` at exactly `v1.44.1`. Import it as `vm` inside `pkg/metrics`, whose own package name is `metrics`.
- `Serve` listens only on a loopback IP: `127.0.0.0/8` or `::1`. It refuses anything else, including host names such as `localhost` and empty hosts such as `:9205`. It logs why and serves nothing.
- A failure in `Serve` never stops the process: it logs and returns.
- Ports, all on 127.0.0.1:
  - liercd `9201`
  - liercd-next `9202`
  - lierc-logger `9203`
  - lierc-notifier `9204`
  - lierc-identd `9205`
- vmagent job names are `liercd-next`, `lierc-logger`, `lierc-notifier` and `lierc-identd`, each with `labels: {instance: relaychat}`. There's no job for the stopped main liercd.
- liercd-next is restarted by hand only (`manual_restart`). Its scrape target is down until the user restarts it.
- Commit lierc on `master` and relaychat-infra on `main`. Push and deploy only with the user's go-ahead.

## Review Focus

1. **`METRICS_LISTEN` set to a non-loopback address,** such as `:9205`, `0.0.0.0:9205` or a public IP. The process keeps running and serves no metrics. Task 1: `TestListenRefusesNonLoopback`.
2. **The metrics port already in use.** The process keeps running, and a log line says so. Task 1: `TestServeLogsAndReturnsOnBindFailure`.
3. **identd's public port 113 still answers** after the change, and nothing new is public. Task 3 checks `ss` and both public addresses.
4. **Series budget.** Total series stay under 1,000 after deploying. Task 3: a count in vmagent.
5. **Methods other than GET on `/metrics`** don't serve anything or change state. Task 1: `TestHandlerRoutes`.

---

### Task 1: `pkg/metrics`

**Files:**
- Create: `pkg/metrics/metrics.go`
- Test: `pkg/metrics/metrics_test.go`
- Modify: `go.mod`, `go.sum` (add `github.com/VictoriaMetrics/metrics v1.44.1`)

**Interfaces:**
- Produces:
  - `func Serve(addr string)` starts a goroutine and returns at once. With `addr == ""` it does nothing.
  - Unexported helpers: `listen(addr string) (net.Listener, error)` and `handler() http.Handler`.

- [ ] **Step 1: Write the failing tests** in `metrics_test.go`:
  - `TestListenRefusesNonLoopback`: for each of `":0"`, `"0.0.0.0:0"`, `"[::]:0"`, `"10.0.0.1:0"`, `"localhost:0"` and `"not-an-addr"`, `listen` returns an error whose text contains `loopback` or `invalid`, and no listener.
  - `TestListenLoopback`: `listen("127.0.0.1:0")` succeeds. `listen("[::1]:0")` also succeeds, unless the host has no IPv6 loopback, in which case it's skipped. Close the listeners.
  - `TestHandlerRoutes`: using `httptest` on `handler()`:
    - `GET /metrics` returns 200, `Content-Type` starting `text/plain`, and a body containing `go_goroutines ` and `process_resident_memory_bytes `.
    - `GET /`, `GET /metrics/x` and `GET /debug/pprof/` return 404.
    - `POST /metrics` returns 405.
  - `TestServeLogsAndReturnsOnBindFailure`: hold a listener on `127.0.0.1:0`, capture `log` output, and call `Serve` with its address. A log line containing the address appears within 2s, and the test process is still running.
  - `TestServeRefusalLogged`: `Serve("0.0.0.0:0")` logs a line containing `loopback`.
- [ ] **Step 2: Run the tests and watch them fail.** Run `go test ./pkg/metrics/`. Expected: a build failure, since there's no package yet.
- [ ] **Step 3: Implement `listen`, `handler` and `Serve`** in `pkg/metrics/metrics.go`:
  - **`listen`** parses with `net.SplitHostPort` and `netip.ParseAddr`, requiring `IsLoopback()`.
  - **`handler`** is an `http.ServeMux` with `GET /metrics`, which writes `vm.WritePrometheus(w, true)` with `Content-Type: text/plain; version=0.0.4`. Every other path gets the mux's 404, and the method mismatch gets its 405.
  - **`Serve`** logs one line on success, `metrics on <addr>`, or one line on any error, then serves with `http.Server{ReadHeaderTimeout: 10 * time.Second}` in a goroutine.
  - Add the dependency with `go get github.com/VictoriaMetrics/metrics@v1.44.1`.
- [ ] **Step 4: Run the tests and watch them pass.** Run `go test -race ./pkg/metrics/ && go vet ./pkg/metrics/`. Expected: `ok`.
- [ ] **Step 5: Commit** with the message "metrics: pkg/metrics serves Go runtime and process metrics on a loopback listener".

### Task 2: Serve metrics from the four processes

**Files:**
- Modify: `cmd/liercd/main.go`, `cmd/lierc-logger/main.go`, `cmd/lierc-notify/main.go` and `cmd/lierc-identd/main.go`. Each `main` gets `metrics.Serve(os.Getenv("METRICS_LISTEN"))` as its first statement, importing `github.com/lierc/lierc/pkg/metrics`.

**Interfaces:**
- Consumes: Task 1's `metrics.Serve(addr string)`.

- [ ] **Step 1: Write the failing test.** Add `cmd/lierc-identd/metrics_test.go` with `TestMainServesMetrics`. It builds the identd binary to a temp dir with `go build`, runs it with `METRICS_LISTEN=127.0.0.1:<free port>`, polls `GET /metrics` for up to 5s, and expects 200 with `go_goroutines`. Then it kills the process. identd's own `:113` bind may fail without privilege; the metrics endpoint must come up first regardless, which is why `Serve` is the first statement.
- [ ] **Step 2: Run it and watch it fail.** Run `go test ./cmd/lierc-identd/ -run TestMainServesMetrics`. Expected: FAIL, because nothing listens.
- [ ] **Step 3: Add the `metrics.Serve` call** to each of the four `main`s.
- [ ] **Step 4: Run the full suite.** Run `go vet ./... && go test ./...`. Expected: all `ok`, including `TestMainServesMetrics`.
- [ ] **Step 5: Commit and push** (with the go-ahead) with the message "metrics: liercd, logger, notifier and identd serve /metrics on METRICS_LISTEN". Then wait for CI, and confirm `ghcr.io/lierc/lierc:sha-<short>` exists.

### Task 3: relaychat-infra (units, scrape jobs, pins), deploy and check

**Files (relaychat-infra):**
- Modify: `roles/services/templates/liercd.service.j2` (`-e METRICS_LISTEN=127.0.0.1:9201`), `liercd-next.service.j2` (`9202`), `lierc-logger.service.j2` (`9203`), `lierc-notifier.service.j2` (`9204`) and `lierc-identd.service.j2` (`9205`). Put each next to the unit's other `-e` lines.
- Modify: `roles/services/templates/vmagent-scrape.yaml.j2`: add the four jobs from Global Constraints.
- Modify: `group_vars/relaychat_hosts/vars.yaml`: `lierc` and `lierc_next` move to `sha-<Task 2 commit>`.

- [ ] **Step 0: Commit and push** relaychat-infra (with the go-ahead) with the message "metrics: scrape Go runtime metrics from the lierc processes". The playbook deploys the pushed commit.
- [ ] **Step 1: Dry run.** Run `ansible-playbook --vault-password-file ~/.config/relaychat-vault-pass site.yaml --check --diff`. Expected: the five unit files change (image and `METRICS_LISTEN`), `scrape.yaml` gains four jobs, and nothing else.
- [ ] **Step 2: Deploy** (with the user's go-ahead) using the same command without `--check --diff`. Expected: `failed=0`. The logger, notifier, identd and vmagent restart; liercd-next only gets a warning.
- [ ] **Step 3: Check on the server.**
  - `curl -s 127.0.0.1:920{3,4,5}/metrics | grep -c "^go_goroutines "` gives 3.
  - `ss -ltn` shows `9203`–`9205` on 127.0.0.1 only.
  - Each port refuses on the public IPv4 and IPv6 addresses.
  - identd still listens on `:113`.
  - vmagent's `/api/v1/targets` shows the three new jobs up and `liercd-next` down.
  - Sends still return 2XX only.
- [ ] **Step 4: Count the series.** Run `curl -s 127.0.0.1:8429/api/v1/targets`, summing each target's series, or count the samples from each `/metrics` endpoint. Expected: under 1,000 total.
- [ ] **Step 5: Tell the user** that liercd-next's metrics appear after its manual restart.
