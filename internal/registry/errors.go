package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/home-operations/ocify/internal/upstream"
)

// ociError is one element of the OCI distribution spec's error response body.
type ociError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeOCIError writes the spec's {"errors": [...]} body with the given
// status. no-store overrides any Cache-Control set earlier on the happy path:
// an intermediary cache must never pin an error — least of all a 401.
func writeOCIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string][]ociError{
		"errors": {{Code: code, Message: message}},
	})
}

// writeResolveError maps a resolution failure onto the spec's error codes.
// Upstream fault detail goes to the log, not the client: the message a puller
// sees names the reference, never the internal fetch that failed.
func (s *Server) writeResolveError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, upstream.ErrNotFound), errors.Is(err, ErrChartUnknown):
		writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
	case errors.Is(err, ErrVersionUnknown):
		writeOCIError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
	case errors.Is(err, ErrBlobUnknown):
		writeOCIError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
	case errors.Is(err, upstream.ErrHostNotAllowed):
		writeOCIError(w, http.StatusForbidden, "DENIED", "upstream host not permitted by this proxy")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client went away or the upstream exchange timed out; 504 is the
		// closest honest answer for the latter and harmless for the former.
		writeOCIError(w, http.StatusGatewayTimeout, "UNKNOWN", "upstream timeout")
	default:
		s.log.Error("upstream resolution failed", "path", r.URL.Path, "error", err)
		writeOCIError(w, http.StatusBadGateway, "UNKNOWN", "upstream unavailable")
	}
}
