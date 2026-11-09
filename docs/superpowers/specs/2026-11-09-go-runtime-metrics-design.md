# Go runtime metrics for the lierc processes

## Intent

Go runtime and process metrics (memory, GC, goroutines, CPU, open files)
from liercd, the notifier, the logger and identd, scraped by the vmagent
that already sends host and nginx metrics to Grafana Cloud
(relaychat-infra `docs/superpowers/specs/2026-11-09-metrics-design.md`).
Application metrics, such as IRC lines and notifications, come later, on
the same endpoint.

## Approaches (the user picked B)

- **A.** `prometheus/client_golang` on a loopback listener per process.
  The standard, and Grafana's Go dashboards expect its names. Many
  dependencies.
- **B (chosen).** `github.com/VictoriaMetrics/metrics` on a loopback
  listener per process. One dependency with none of its own, mostly the
  same `go_*` and `process_*` names, vmagent's native format, and simple
  counters for the later application metrics. Some dashboard panels that
  expect client_golang-only series will be empty.
- **C.** `/metrics` on liercd's existing control API. Rejected: the other
  three still need listeners, so it's inconsistent.

## Design

- **The library:** `github.com/VictoriaMetrics/metrics` v1.44.1
  (2026-09-28; v1.45.0 is under two weeks old).
- **The package:** `pkg/metrics` in lierc, with one function,
  `Serve(addr string)`. With `addr` empty it does nothing. Otherwise it
  serves `GET /metrics` on `addr` in a goroutine, writing
  `metrics.WritePrometheus(w, true)`, which includes the process metrics.
  Every other path is a 404.
- **Loopback only** (my call): `Serve` refuses an address whose host isn't
  a loopback IP. It logs why and serves nothing. identd's own port is
  public, so a mistyped `METRICS_LISTEN` mustn't expose metrics.
- **Failures don't stop the process** (my call): if the listener can't
  bind, `Serve` logs and the process carries on. Metrics are optional;
  IRC, logging and notifications are not.
- **Each `cmd/*/main.go`** (liercd, lierc-logger, lierc-notify,
  lierc-identd) calls `metrics.Serve(os.Getenv("METRICS_LISTEN"))` at
  start.
- **Ports** (my call), all on 127.0.0.1:
  - liercd 9201 (the main instance, stopped today)
  - liercd-next 9202
  - logger 9203
  - notifier 9204
  - identd 9205
- **relaychat-infra:**
  - **Units:** each unit gets `-e METRICS_LISTEN=127.0.0.1:<port>`.
  - **Scrape jobs:** vmagent's scrape config gets jobs `liercd-next`,
    `lierc-logger`, `lierc-notifier` and `lierc-identd`, each with
    `instance: relaychat`. There's no job for the stopped main liercd
    until it runs again.
  - **The image:** the `lierc` image pins move to the new commit, after CI
    passes.
- **Series:** about 50 per process (`go_*` plus `process_*`), so about 200
  in all; the total stays under 1,000.

## Constraints

- Nothing new reachable from outside: check `ss` and both public
  addresses after deploying, as for the host metrics.
- liercd-next is restarted by hand only, because a restart drops every IRC
  connection. Its metrics appear only after that restart.

## Non-goals

Application metrics. The lierc-api-go stream server. Alerts.

## Left to the builder

- Test style: a Go test for `Serve`'s loopback check and its 404s, plus
  the live checks.
- The exact log wording.
