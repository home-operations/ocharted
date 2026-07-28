package registry

import (
	"net/http"
	"runtime"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// labelMethod is the request-method key shared by the metric labels and the
// access-log attribute.
const labelMethod = "method"

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ocharted_http_requests_total",
		Help: "Total number of inbound HTTP requests handled, by method and status class.",
	}, []string{labelMethod, "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ocharted_http_request_duration_seconds",
		Help:    "Inbound HTTP request duration in seconds, by method.",
		Buckets: prometheus.DefBuckets,
	}, []string{labelMethod})

	// artifactBuilds counts full chart derivations (download + package): the
	// real upstream work. Compare against cache events to judge TTL tuning.
	artifactBuilds = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ocharted_artifact_builds_total",
		Help: "Chart artifacts derived from upstream (tarball download + OCI packaging).",
	})

	cacheEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ocharted_artifact_cache_events_total",
		Help: "Derived-artifact cache activity, by event (hit/miss/evict).",
	}, []string{"event"})

	// authBypassed counts requests admitted anonymously because their whole
	// connection chain fell within OCHARTED_AUTH_BYPASS_NETWORKS.
	authBypassed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ocharted_auth_bypassed_total",
		Help: "Requests that skipped basic auth via the trusted-network bypass.",
	})

	buildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ocharted_build_info",
		Help: "Build metadata; the value is always 1, the version/commit/goversion are labels.",
	}, []string{"version", "commit", "goversion"})
)

// RecordBuildInfo sets the ocharted_build_info gauge from the stamped build
// vars, so the running build is queryable from Prometheus, not just the boot
// log.
func RecordBuildInfo(version, commit string) {
	buildInfo.WithLabelValues(version, commit, runtime.Version()).Set(1)
}

// metricsHandler serves Prometheus metrics on the dedicated, optional metrics
// listener, kept off the public registry port. Health probes are NOT served
// here — /healthz and /readyz live on the registry listener, so this whole
// port can be disabled without breaking probes.
func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}

// statusClass buckets a status code into a low-cardinality label (e.g. "2xx").
func statusClass(code int) string {
	return strconv.Itoa(code/100) + "xx"
}

// knownMethods bounds the method metric label. A client can send an arbitrary
// request method, so unrecognised ones collapse to "other" — otherwise
// distinct method strings would grow the metric's cardinality without bound.
var knownMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {},
	http.MethodPut: {}, http.MethodPatch: {}, http.MethodDelete: {},
	http.MethodConnect: {}, http.MethodOptions: {}, http.MethodTrace: {},
}

func methodLabel(method string) string {
	if _, ok := knownMethods[method]; ok {
		return method
	}
	return "other"
}
