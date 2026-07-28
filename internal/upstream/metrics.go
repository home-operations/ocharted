package upstream

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// staleIndexServed counts stale-if-error activations: index requests answered
// from an expired cache entry because the upstream re-fetch failed. A nonzero
// rate means an upstream is unhealthy while ocharted keeps serving.
var staleIndexServed = promauto.NewCounter(prometheus.CounterOpts{
	Name: "ocharted_upstream_stale_index_served_total",
	Help: "Index requests answered from an expired cache entry because the upstream fetch failed (stale-if-error).",
})
