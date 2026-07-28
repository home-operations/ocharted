// Package upstream fetches Helm repository indexes and chart tarballs over
// HTTPS. It is the proxy's only source of truth: everything served downstream
// is derived from what this package returns. Indexes are cached with a TTL
// (the freshness/politeness knob); tarballs are not cached here — the registry
// layer caches derived artifacts instead.
package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Sentinel errors the registry layer maps onto OCI error codes.
var (
	// ErrNotFound covers an upstream 404/410 — no such repo, chart, or file.
	ErrNotFound = errors.New("upstream resource not found")
	// ErrTooLarge marks a response body over the configured cap.
	ErrTooLarge = errors.New("upstream response too large")
	// ErrDigestMismatch marks a tarball whose bytes don't hash to the digest
	// the index published for it (a mutated or corrupted upstream).
	ErrDigestMismatch = errors.New("upstream chart digest mismatch")
)

// maxIndexCacheEntries bounds the index cache; the working set is one entry
// per upstream repo, so hitting this means someone is enumerating repos
// through the proxy, and evicting is the right response.
const maxIndexCacheEntries = 128

// Options configures the Client. Zero values are not usable defaults — the
// caller (config) supplies every field.
type Options struct {
	// Timeout bounds one upstream HTTP exchange end to end.
	Timeout time.Duration
	// IndexTTL is how long a fetched index.yaml is reused before re-fetching.
	IndexTTL time.Duration
	// IndexStaleTTL bounds stale-if-error: when a re-fetch fails with a
	// non-authoritative error, an expired cache entry younger than this is
	// served instead of the error, riding out upstream outages. 0 disables.
	IndexStaleTTL time.Duration
	// MaxIndexBytes / MaxChartBytes cap the respective response bodies.
	MaxIndexBytes int64
	MaxChartBytes int64
	// AllowPrivate disables the private-address dial guard (for clusters that
	// proxy charts from an internal ChartMuseum).
	AllowPrivate bool
	// AllowedHosts restricts which upstream repo hosts may be proxied
	// (path.Match globs). Empty allows any public host.
	AllowedHosts []string
	// UserAgent identifies the proxy to upstreams.
	UserAgent string
	// Transport overrides the SSRF-guarded default transport. Tests only —
	// production wiring must leave it nil so the dial guard applies.
	Transport http.RoundTripper
}

// Client fetches and caches upstream repo data. Safe for concurrent use.
type Client struct {
	opts Options
	http *http.Client

	mu    sync.Mutex
	cache map[string]cachedIndex
	sf    singleflight.Group
}

type cachedIndex struct {
	idx     *Index
	fetched time.Time
}

// New builds a Client from opts.
func New(opts Options) *Client {
	transport := opts.Transport
	if transport == nil {
		transport = newTransport(opts.AllowPrivate, opts.Timeout)
	}
	return &Client{
		opts: opts,
		http: &http.Client{
			Timeout:   opts.Timeout,
			Transport: transport,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				// Redirects may hop hosts (GitHub releases → CDN) but never
				// off HTTPS.
				if req.URL.Scheme != "https" {
					return fmt.Errorf("refusing redirect to non-https URL %s", req.URL)
				}
				return nil
			},
		},
	}
}

// Index returns the parsed index.yaml for repo ("host" or "host/path"),
// cached per TTL. Concurrent misses for the same repo are collapsed into one
// upstream fetch. When the re-fetch of an expired entry fails, the stale
// entry is served instead (bounded by IndexStaleTTL) — see staleFor.
func (c *Client) Index(ctx context.Context, repo string) (*Index, error) {
	if !hostAllowed(repoHost(repo), c.opts.AllowedHosts) {
		return nil, fmt.Errorf("%w: %s", ErrHostNotAllowed, repo)
	}

	c.mu.Lock()
	entry, ok := c.cache[repo]
	c.mu.Unlock()
	if ok && time.Since(entry.fetched) < c.opts.IndexTTL {
		return entry.idx, nil
	}

	v, err, _ := c.sf.Do(repo, func() (any, error) {
		raw, err := c.fetch(ctx, "https://"+repo+"/index.yaml", c.opts.MaxIndexBytes)
		if err != nil {
			return nil, err
		}
		idx, err := ParseIndex(raw)
		if err != nil {
			return nil, err
		}
		c.store(repo, idx)
		return idx, nil
	})
	if err != nil {
		if stale := c.staleFor(repo, err); stale != nil {
			return stale, nil
		}
		return nil, err
	}
	return v.(*Index), nil
}

// staleFor implements stale-if-error: an expired cache entry younger than
// IndexStaleTTL is served when the re-fetch failed for a non-authoritative
// reason (network fault, 5xx, oversize, parse error). Authoritative answers
// are never masked: a 404 means the repo is gone and an allowlist rejection
// is policy. Serving stale trades bounded staleness for riding out upstream
// outages — the entry is derived data either way, so no statefulness rule is
// broken; a replica without the entry simply degrades to the error.
func (c *Client) staleFor(repo string, err error) *Index {
	if c.opts.IndexStaleTTL <= 0 || errors.Is(err, ErrNotFound) || errors.Is(err, ErrHostNotAllowed) {
		return nil
	}
	c.mu.Lock()
	entry, ok := c.cache[repo]
	c.mu.Unlock()
	if !ok || time.Since(entry.fetched) > c.opts.IndexStaleTTL {
		return nil
	}
	staleIndexServed.Inc()
	slog.Warn("serving stale index: upstream fetch failed",
		"repo", repo, "age", time.Since(entry.fetched).Round(time.Second).String(), "error", err)
	return entry.idx
}

// Chart downloads the tarball for entry and verifies it against the digest
// the index published, so a mutated upstream is surfaced as an error rather
// than served under a stale identity.
func (c *Client) Chart(ctx context.Context, repo string, entry Entry) ([]byte, error) {
	u, err := entryURL(repo, entry)
	if err != nil {
		return nil, err
	}
	raw, err := c.fetch(ctx, u, c.opts.MaxChartBytes)
	if err != nil {
		return nil, err
	}
	if entry.Digest != "" {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != strings.TrimPrefix(entry.Digest, "sha256:") {
			return nil, fmt.Errorf("%w: %s %s", ErrDigestMismatch, repo, entry.Version)
		}
	}
	return raw, nil
}

// Prov fetches the chart's .prov provenance file. Absence (404/410) returns
// (nil, nil); any other failure is an error — silently omitting the layer on
// a transient fault would change the manifest digest between requests.
func (c *Client) Prov(ctx context.Context, repo string, entry Entry) ([]byte, error) {
	u, err := entryURL(repo, entry)
	if err != nil {
		return nil, err
	}
	raw, err := c.fetch(ctx, u+".prov", c.opts.MaxChartBytes)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) store(repo string, idx *Index) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = map[string]cachedIndex{}
	}
	if len(c.cache) >= maxIndexCacheEntries {
		// Prefer evicting entries past even their stale-if-error window, then
		// whatever it takes to get under the cap.
		now := time.Now()
		maxAge := max(c.opts.IndexTTL, c.opts.IndexStaleTTL)
		for k, v := range c.cache {
			if now.Sub(v.fetched) > maxAge {
				delete(c.cache, k)
			}
		}
		for k := range c.cache {
			if len(c.cache) < maxIndexCacheEntries {
				break
			}
			delete(c.cache, k)
		}
	}
	c.cache[repo] = cachedIndex{idx: idx, fetched: time.Now()}
}

// fetch GETs url and returns at most limit bytes, mapping HTTP status onto
// the package's sentinel errors.
func (c *Client) fetch(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("upstream: build request for %s: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream: fetch %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, rawURL)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("upstream: fetch %s: unexpected status %s", rawURL, resp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("upstream: read %s: %w", rawURL, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, rawURL, limit)
	}
	return raw, nil
}

// entryURL resolves the entry's first URL against the repo base, handling the
// relative URLs `helm repo index` writes.
func entryURL(repo string, entry Entry) (string, error) {
	if len(entry.URLs) == 0 {
		return "", fmt.Errorf("upstream: index entry for %s %s has no URL", repo, entry.Version)
	}
	base, err := url.Parse("https://" + repo + "/")
	if err != nil {
		return "", fmt.Errorf("upstream: invalid repo %q: %w", repo, err)
	}
	ref, err := url.Parse(entry.URLs[0])
	if err != nil {
		return "", fmt.Errorf("upstream: invalid chart URL %q: %w", entry.URLs[0], err)
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "https" {
		return "", fmt.Errorf("upstream: refusing non-https chart URL %s", resolved)
	}
	return resolved.String(), nil
}

// repoHost extracts the host component of a repo name ("host/path" → "host").
func repoHost(repo string) string {
	host, _, _ := strings.Cut(repo, "/")
	return host
}
