package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/home-operations/ocify/internal/oci"
	"github.com/home-operations/ocify/internal/sign"
	"github.com/home-operations/ocify/internal/upstream"
	"golang.org/x/sync/singleflight"
)

// Sentinel errors the handlers map onto OCI error codes.
var (
	// ErrChartUnknown means the upstream index has no entry for the chart.
	ErrChartUnknown = errors.New("chart not found in upstream index")
	// ErrVersionUnknown means the chart exists but the requested version or
	// manifest digest does not.
	ErrVersionUnknown = errors.New("chart version not found")
	// ErrBlobUnknown means no candidate version's blobs match the digest.
	ErrBlobUnknown = errors.New("blob not found")
)

// Resolver turns registry references (tag, manifest digest, blob digest) into
// artifacts, deriving them from upstream on demand. All lookups are pure
// functions of the request plus upstream state, so any replica resolves any
// reference — the cache only decides how much upstream work that takes.
type Resolver struct {
	up   *upstream.Client
	opts ResolverOptions

	cache *artifactCache
	sf    singleflight.Group
}

// ResolverOptions configures optional Resolver behavior.
type ResolverOptions struct {
	// Provenance enables the .prov passthrough layer.
	Provenance bool
	// ScanLimit bounds cold by-digest candidate scans.
	ScanLimit int
	// CacheBytes bounds the derived-artifact cache.
	CacheBytes int64
	// Signer (nil to disable) serves cosign signature artifacts for every
	// manifest.
	Signer *sign.Signer
	// RewriteHost, when non-empty, rewrites HTTP(S) dependency repository
	// URLs in served charts to oci://<RewriteHost>/… — see
	// oci.RewriteDependencies for the constraints and trade-offs.
	RewriteHost string
}

// NewResolver builds a Resolver.
func NewResolver(up *upstream.Client, opts ResolverOptions) *Resolver {
	return &Resolver{
		up:    up,
		opts:  opts,
		cache: newArtifactCache(opts.CacheBytes),
	}
}

// entries fetches the upstream index and the chart's version entries,
// translating an absent chart into ErrChartUnknown. Every lookup goes through
// here so that check lives in exactly one place.
func (r *Resolver) entries(ctx context.Context, repo, chart string) (*upstream.Index, []upstream.Entry, error) {
	idx, err := r.up.Index(ctx, repo)
	if err != nil {
		return nil, nil, err
	}
	entries := idx.Versions(chart)
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("%w: %s/%s", ErrChartUnknown, repo, chart)
	}
	return idx, entries, nil
}

// Tags lists the chart's versions as OCI tags, lexically sorted (the
// distribution spec's tags/list order). Versions that can't be expressed as a
// tag are skipped.
func (r *Resolver) Tags(ctx context.Context, repo, chart string) ([]string, error) {
	_, entries, err := r.entries(ctx, repo, chart)
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(entries))
	for _, e := range entries {
		if tag := oci.VersionToTag(e.Version); oci.ValidTag(tag) {
			tags = append(tags, tag)
		}
	}
	slices.Sort(tags)
	return tags, nil
}

// ByVersion resolves one exact chart version to its derived artifact,
// building (and caching) it from upstream on a miss. Concurrent misses for
// the same version collapse into one upstream download.
func (r *Resolver) ByVersion(ctx context.Context, repo, chart, version string) (*oci.Artifact, error) {
	key := versionKey(repo, chart, version)
	if art, ok := r.cache.get(key); ok {
		return art, nil
	}

	v, err, _ := r.sf.Do(key, func() (any, error) {
		if art, ok := r.cache.get(key); ok {
			return art, nil
		}
		idx, _, err := r.entries(ctx, repo, chart)
		if err != nil {
			return nil, err
		}
		entry, ok := idx.FindVersion(chart, version)
		if !ok {
			return nil, fmt.Errorf("%w: %s/%s %s", ErrVersionUnknown, repo, chart, version)
		}
		art, err := r.build(ctx, repo, entry)
		if err != nil {
			return nil, err
		}
		r.cache.add(repo, chart, art)
		return art, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*oci.Artifact), nil
}

// ByManifestDigest resolves a manifest pulled by digest. Cold path: derive
// candidate versions (newest first, bounded by scanLimit) until one's
// manifest digest matches — each candidate lands in the cache, so the scan
// pays down future requests.
func (r *Resolver) ByManifestDigest(ctx context.Context, repo, chart, digest string) (*oci.Artifact, error) {
	if art, ok := r.cache.get(digestKey(repo, chart, digest)); ok && art.ManifestDigest == digest {
		return art, nil
	}
	art, err := r.scan(ctx, repo, chart, func(a *oci.Artifact) bool {
		return a.ManifestDigest == digest
	})
	if err != nil {
		return nil, err
	}
	if art == nil {
		return nil, fmt.Errorf("%w: %s/%s@%s", ErrVersionUnknown, repo, chart, digest)
	}
	return art, nil
}

// Blob resolves blob content by digest. The layer fast path matches the
// digest against the tarball digests the index publishes; config and
// provenance blobs (not in the index) fall back to the bounded scan.
func (r *Resolver) Blob(ctx context.Context, repo, chart, digest string) ([]byte, string, error) {
	if art, ok := r.cache.get(digestKey(repo, chart, digest)); ok {
		if data, mt, ok := art.Blob(digest); ok {
			return data, mt, nil
		}
	}

	idx, _, err := r.entries(ctx, repo, chart)
	if err != nil {
		return nil, "", err
	}
	if entry, ok := idx.FindLayerDigest(chart, digest); ok {
		art, err := r.ByVersion(ctx, repo, chart, entry.Version)
		if err != nil {
			return nil, "", err
		}
		if data, mt, ok := art.Blob(digest); ok {
			return data, mt, nil
		}
	}

	art, err := r.scan(ctx, repo, chart, func(a *oci.Artifact) bool {
		_, _, ok := a.Blob(digest)
		return ok
	})
	if err != nil {
		return nil, "", err
	}
	if art != nil {
		if data, mt, ok := art.Blob(digest); ok {
			return data, mt, nil
		}
	}
	return nil, "", fmt.Errorf("%w: %s/%s %s", ErrBlobUnknown, repo, chart, digest)
}

// SigningEnabled reports whether cosign signature serving is on.
func (r *Resolver) SigningEnabled() bool { return r.opts.Signer != nil }

// Signature derives the cosign signature artifact for targetDigest, after
// confirming the digest actually resolves to a manifest this proxy serves —
// ocify must never sign a digest it cannot derive. The payload's
// docker-reference is the bare name (repo/chart) without a registry host: the
// same chart is addressed through several hostnames (cluster-internal
// Service, public ingress), and signature bytes must be identical through all
// of them. Cosign's claim verification checks the digest, not the reference.
// Signatures are not cached: derivation is pure CPU once the target artifact
// is known.
func (r *Resolver) Signature(ctx context.Context, repo, chart, targetDigest string) (*sign.Signature, error) {
	if r.opts.Signer == nil {
		return nil, fmt.Errorf("%w: signing disabled", ErrVersionUnknown)
	}
	if _, err := r.ByManifestDigest(ctx, repo, chart, targetDigest); err != nil {
		return nil, err
	}
	return r.opts.Signer.Artifact(repo+"/"+chart, targetDigest)
}

// SignatureBlob resolves a signature payload/config blob by digest — the
// cold-replica path for the blob fetches that follow a .sig manifest pull.
// Signature blobs are derived per candidate manifest via the same bounded
// scan as chart blobs, so this stays a pure function of the request.
func (r *Resolver) SignatureBlob(ctx context.Context, repo, chart, digest string) ([]byte, string, error) {
	if r.opts.Signer == nil {
		return nil, "", fmt.Errorf("%w: signing disabled", ErrBlobUnknown)
	}
	art, err := r.scan(ctx, repo, chart, func(a *oci.Artifact) bool {
		sig, err := r.opts.Signer.Artifact(repo+"/"+chart, a.ManifestDigest)
		if err != nil {
			return false
		}
		_, _, ok := sig.Blob(digest)
		return ok
	})
	if err != nil {
		return nil, "", err
	}
	if art != nil {
		sig, err := r.opts.Signer.Artifact(repo+"/"+chart, art.ManifestDigest)
		if err != nil {
			return nil, "", err
		}
		if data, mt, ok := sig.Blob(digest); ok {
			return data, mt, nil
		}
	}
	return nil, "", fmt.Errorf("%w: %s/%s %s", ErrBlobUnknown, repo, chart, digest)
}

// scan derives candidate versions newest-first until match says stop, giving
// up (nil, nil) after scanLimit candidates. Real clients request blobs
// milliseconds after the manifest that referenced them, so this cold path is
// rare — the bound keeps its worst case (a digest that matches nothing)
// proportional to scanLimit downloads, not the whole version history.
func (r *Resolver) scan(ctx context.Context, repo, chart string, match func(*oci.Artifact) bool) (*oci.Artifact, error) {
	_, entries, err := r.entries(ctx, repo, chart)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries[:min(len(entries), r.opts.ScanLimit)] {
		art, err := r.ByVersion(ctx, repo, chart, entry.Version)
		if err != nil {
			return nil, err
		}
		if match(art) {
			return art, nil
		}
	}
	return nil, nil
}

// build downloads and derives one chart version. Rewriting (when enabled)
// happens after the download is verified against the upstream index digest,
// so integrity against upstream still holds even though the served bytes
// then diverge from it.
func (r *Resolver) build(ctx context.Context, repo string, entry upstream.Entry) (*oci.Artifact, error) {
	chart, err := r.up.Chart(ctx, repo, entry)
	if err != nil {
		return nil, err
	}
	if r.opts.RewriteHost != "" {
		if chart, err = oci.RewriteDependencies(chart, r.opts.RewriteHost); err != nil {
			return nil, err
		}
	}
	var prov []byte
	if r.opts.Provenance {
		if prov, err = r.up.Prov(ctx, repo, entry); err != nil {
			return nil, err
		}
	}
	art, err := oci.Build(chart, prov)
	if err != nil {
		return nil, err
	}
	artifactBuilds.Inc()
	return art, nil
}
