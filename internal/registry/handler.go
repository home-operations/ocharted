package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/home-operations/ocharted/internal/oci"
)

// immutableCacheControl marks by-digest responses, which can never change for
// a given URL, as cacheable forever. Inert behind a plain ingress (neither
// envoy nor nginx caches by default), but it lets a deliberate caching layer —
// a Cloudflare cache rule, nginx proxy_cache, any CDN — serve the heavy
// endpoints from its edge. By-tag responses instead get a max-age tied to the
// index TTL: the tag is the mutable reference through which new upstream
// state becomes visible, so it must not outlive the proxy's own freshness
// horizon.
const immutableCacheControl = "public, max-age=31536000, immutable"

var (
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	// segmentPattern accepts registry-name path segments. Deliberately looser
	// than the distribution spec's grammar: an upstream host may carry a
	// nonstandard port ("chartmuseum.internal:8443"), so ':' is admitted.
	segmentPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._:-]*[a-z0-9])?$`)
)

// handler builds the public mux: the OCI distribution API under /v2/ (behind
// optional basic auth), the probe pair on the same port, everything else 404.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v2/", s.basicAuth(http.HandlerFunc(s.handleV2)))
	// The org pair standard: /healthz = liveness, /readyz = readiness, both on
	// the main port so the optional metrics listener can be disabled without
	// touching the probes. ocharted has no serving condition beyond being up
	// (upstreams are per-request dependencies), so readyz aliases healthz.
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /readyz", handleHealth)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeOCIError(w, http.StatusNotFound, "UNSUPPORTED", "not found")
	})

	var h http.Handler = mux
	h = recoverer(s.log)(h)
	h = securityHeaders(h)
	h = s.observe(h)
	return h
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleV2 routes the read-only OCI distribution API. The repo/chart name is
// multi-segment ("charts.jetstack.io/cert-manager"), which ServeMux patterns
// can't split, so paths are parsed by hand:
//
//	GET  /v2/                                  API version check
//	GET  /v2/<host[/path]>/<chart>/tags/list   versions as tags
//	GET  /v2/<host[/path]>/<chart>/manifests/<tag|digest>
//	GET  /v2/<host[/path]>/<chart>/blobs/<digest>
func (s *Server) handleV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeOCIError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "registry is read-only")
		return
	}
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v2"), "/")
	if rest == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("{}"))
		return
	}

	segs := strings.Split(rest, "/")
	n := len(segs)
	var (
		name []string
		next func(http.ResponseWriter, *http.Request, string, string)
	)
	switch {
	case n >= 2 && segs[n-2] == "tags" && segs[n-1] == "list":
		name, next = segs[:n-2], s.handleTags
	case n >= 2 && segs[n-2] == "manifests":
		ref := segs[n-1]
		name = segs[:n-2]
		next = func(w http.ResponseWriter, r *http.Request, repo, chart string) {
			s.handleManifest(w, r, repo, chart, ref)
		}
	case n >= 2 && segs[n-2] == "blobs":
		digest := segs[n-1]
		name = segs[:n-2]
		next = func(w http.ResponseWriter, r *http.Request, repo, chart string) {
			s.handleBlob(w, r, repo, chart, digest)
		}
	default:
		writeOCIError(w, http.StatusNotFound, "UNSUPPORTED", "unsupported registry path")
		return
	}
	s.dispatch(w, r, name, next)
}

// dispatch validates and splits the multi-segment name into (repo, chart): the
// final segment is the chart, everything before it the upstream repo. A name
// therefore needs at least two segments — a bare "/v2/<chart>/..." has no
// upstream to proxy.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, name []string, next func(http.ResponseWriter, *http.Request, string, string)) {
	if len(name) < 2 {
		writeOCIError(w, http.StatusBadRequest, "NAME_INVALID",
			"name must be <upstream-host[/path]>/<chart>, e.g. charts.jetstack.io/cert-manager")
		return
	}
	for _, seg := range name {
		if !segmentPattern.MatchString(seg) {
			writeOCIError(w, http.StatusBadRequest, "NAME_INVALID", fmt.Sprintf("invalid name segment %q", seg))
			return
		}
	}
	repo := strings.Join(name[:len(name)-1], "/")
	next(w, r, repo, name[len(name)-1])
}

// handleTags serves tags/list with the spec's n/last pagination and Link
// header.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request, repo, chart string) {
	tags, err := s.res.Tags(r.Context(), repo, chart)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	name := repo + "/" + chart
	if last := r.URL.Query().Get("last"); last != "" {
		for len(tags) > 0 && tags[0] <= last {
			tags = tags[1:]
		}
	}
	truncated := false
	if nParam := r.URL.Query().Get("n"); nParam != "" {
		limit, err := strconv.Atoi(nParam)
		if err != nil || limit < 0 {
			writeOCIError(w, http.StatusBadRequest, "UNSUPPORTED", "invalid n parameter")
			return
		}
		if len(tags) > limit {
			tags = tags[:limit]
			truncated = true
		}
	}
	if truncated && len(tags) > 0 {
		w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?n=%s&last=%s>; rel="next"`,
			name, r.URL.Query().Get("n"), url.QueryEscape(tags[len(tags)-1])))
	}

	w.Header().Set("Cache-Control", s.mutableCacheControl())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "tags": tags})
}

// mutableCacheControl is the Cache-Control for by-tag and listing responses.
func (s *Server) mutableCacheControl() string {
	return fmt.Sprintf("public, max-age=%d", int(s.cfg.IndexTTL.Seconds()))
}

// handleManifest serves manifests by tag or digest, GET and HEAD alike (the
// spec requires identical headers on both). Cosign's sha256-<digest>.sig tag
// convention is intercepted before ordinary tag handling.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request, repo, chart, ref string) {
	if target, ok := sigTagTarget(ref); ok {
		s.handleSignatureManifest(w, r, repo, chart, target)
		return
	}

	var (
		art          *oci.Artifact
		err          error
		cacheControl string
	)
	switch {
	case digestPattern.MatchString(ref):
		art, err = s.res.ByManifestDigest(r.Context(), repo, chart, ref)
		cacheControl = immutableCacheControl
	case oci.ValidTag(ref):
		art, err = s.res.ByVersion(r.Context(), repo, chart, oci.TagToVersion(ref))
		cacheControl = s.mutableCacheControl()
	default:
		writeOCIError(w, http.StatusBadRequest, "MANIFEST_INVALID", fmt.Sprintf("invalid reference %q", ref))
		return
	}
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}
	writeContent(w, r, cacheControl, oci.ManifestMediaType, art.ManifestDigest, art.Manifest)
}

// handleSignatureManifest serves the cosign signature manifest for a target
// manifest digest. With signing disabled this answers MANIFEST_UNKNOWN, which
// cosign-aware clients read as "unsigned".
func (s *Server) handleSignatureManifest(w http.ResponseWriter, r *http.Request, repo, chart, target string) {
	sig, err := s.res.Signature(r.Context(), repo, chart, target)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}
	// The .sig tag's content shifts if the signing key rotates, so it gets the
	// mutable lifetime even though its target digest is fixed.
	writeContent(w, r, s.mutableCacheControl(), oci.ManifestMediaType, sig.ManifestDigest, sig.Manifest)
}

// sigTagTarget parses cosign's signature tag convention
// (sha256-<hex>.sig → sha256:<hex>).
func sigTagTarget(ref string) (string, bool) {
	hex, ok := strings.CutPrefix(ref, "sha256-")
	if !ok {
		return "", false
	}
	hex, ok = strings.CutSuffix(hex, ".sig")
	if !ok {
		return "", false
	}
	digest := "sha256:" + hex
	if !digestPattern.MatchString(digest) {
		return "", false
	}
	return digest, true
}

// handleBlob serves config, chart, and provenance blobs by digest.
func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request, repo, chart, digest string) {
	if !digestPattern.MatchString(digest) {
		writeOCIError(w, http.StatusBadRequest, "DIGEST_INVALID", fmt.Sprintf("invalid digest %q", digest))
		return
	}
	data, _, err := s.res.Blob(r.Context(), repo, chart, digest)
	if err != nil && errors.Is(err, ErrBlobUnknown) && s.res.SigningEnabled() {
		// Not a chart blob — it may be a signature payload/config blob being
		// fetched after a .sig manifest pull (possibly by a different replica).
		data, _, err = s.res.SignatureBlob(r.Context(), repo, chart, digest)
	}
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}
	writeContent(w, r, immutableCacheControl, "application/octet-stream", digest, data)
}

// writeContent writes the shared content headers for manifest and blob
// responses and, on GET (never HEAD), the body. The spec requires identical
// headers on both methods.
func writeContent(w http.ResponseWriter, r *http.Request, cacheControl, contentType, digest string, body []byte) {
	h := w.Header()
	h.Set("Cache-Control", cacheControl)
	h.Set("Content-Type", contentType)
	h.Set("Docker-Content-Digest", digest)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}
