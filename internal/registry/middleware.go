package registry

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"
)

// statusRecorder captures the response status code for the access log and
// metrics, defaulting to 200 if the handler writes a body without an explicit
// WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap exposes the wrapped ResponseWriter to http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// basicAuth guards the /v2/ API with HTTP basic auth when users are
// configured. The 401 carries the Basic challenge, which is what makes the
// standard registry credential flow work: clients ping /v2/, get challenged,
// and retry with the dockerconfigjson/hostRule credentials. With no users
// configured the registry is anonymous and this is a pass-through. Requests
// whose whole connection chain lies within the bypass networks (see
// chainWithin) skip auth, so in-cluster clients pull anonymously through the
// same hostname external clients must authenticate to.
func (s *Server) basicAuth(next http.Handler) http.Handler {
	if len(s.cfg.Users) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.AuthBypassNets) > 0 && chainWithin(r, s.cfg.AuthBypassNets) {
			authBypassed.Inc()
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		want, known := s.cfg.Users[user]
		if !ok || !known || subtle.ConstantTimeCompare([]byte(pass), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="ocharted"`)
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			writeOCIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// chainWithin reports whether the request's entire connection chain — the TCP
// peer plus every X-Forwarded-For hop — lies within nets. Any hop outside
// means an external party was in the path: either as the real client, or as
// the forger of an XFF entry whose own address a trusted gateway then
// appended (Envoy and Cloudflare append truthfully, which the bypass model
// requires of every hop). Order therefore doesn't matter, and anything
// malformed fails closed to "not within".
func chainWithin(r *http.Request, nets []netip.Prefix) bool {
	if !addrWithin(r.RemoteAddr, nets) {
		return false
	}
	for _, xff := range r.Header.Values("X-Forwarded-For") {
		for entry := range strings.SplitSeq(xff, ",") {
			if !addrWithin(strings.TrimSpace(entry), nets) {
				return false
			}
		}
	}
	return true
}

// addrWithin parses s ("ip" or "ip:port") and reports whether it falls in any
// of nets. Unparseable input is "not within" — fail closed.
func addrWithin(s string, nets []netip.Prefix) bool {
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, n := range nets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// securityHeaders makes any JSON response inert in a browser: nosniff stops
// content-type guessing. Cache-Control is intentionally NOT set here — blob
// and manifest responses are immutable by digest and clients may cache them.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a handler panic into a 500 instead of crashing the process.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("recovered panic", "error", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
					writeOCIError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// observe records Prometheus metrics and emits the per-request access log. It
// wraps the public registry listener.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Record in a defer so a panic recovered by the inner recoverer (which
		// writes 500 to rec) is still counted and access-logged, with its status.
		defer func() {
			duration := time.Since(start)
			method := methodLabel(r.Method)
			httpRequests.WithLabelValues(method, statusClass(rec.status)).Inc()
			httpDuration.WithLabelValues(method).Observe(duration.Seconds())
			s.logRequest(r, rec.status, duration)
		}()

		next.ServeHTTP(rec, r)
	})
}

// accessLog emits the per-request access log WITHOUT recording request
// metrics. It wraps the monitoring listener, so /metrics scrapes are logged
// (at debug — see logRequest) without inflating the request counters.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.logRequest(r, rec.status, time.Since(start))
	})
}

// monitoringPaths are the scrape/probe endpoints whose access log is noise at
// the scrape/probe cadence, so it is emitted at Debug — visible only under
// OCHARTED_LOG_LEVEL=debug — rather than at Info with real traffic.
var monitoringPaths = map[string]struct{}{
	"/metrics": {}, "/healthz": {}, "/readyz": {},
}

// logRequest emits the access log for one request at the path-appropriate
// level (Debug for the monitoring endpoints, Info otherwise), unless request
// logs are disabled entirely.
func (s *Server) logRequest(r *http.Request, status int, d time.Duration) {
	if s.cfg.DisableRequestLogs {
		return
	}
	level := slog.LevelInfo
	if _, ok := monitoringPaths[r.URL.Path]; ok {
		level = slog.LevelDebug
	}
	s.log.Log(r.Context(), level, "request",
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"remote", r.RemoteAddr,
		"duration", d.String(),
	)
}
