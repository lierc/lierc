package main

import vm "github.com/VictoriaMetrics/metrics"

// queries counts ident queries by how they were answered, served on
// METRICS_LISTEN (pkg/metrics). Created up front, so each series exists
// from zero.
var queries = map[string]*vm.Counter{
	"user":    vm.NewCounter(`identd_queries_total{result="user"}`),
	"no_user": vm.NewCounter(`identd_queries_total{result="no_user"}`),
	"error":   vm.NewCounter(`identd_queries_total{result="error"}`),
	"invalid": vm.NewCounter(`identd_queries_total{result="invalid"}`),
}
