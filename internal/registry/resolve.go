package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"

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
	up         *upstream.Client
	provenance bool
	scanLimit  int
	cache      *artifactCache
	sf         singleflight.Group
	signer     *sign.Signer
}

// NewResolver builds a Resolver. scanLimit bounds cold by-digest scans;
// provenance enables the optional .prov passthrough layer; signer (nil to
// disable) serves cosign signature artifacts for every manifest.
func NewResolver(up *upstream.Client, provenance bool, scanLimit int, cacheBytes int64, signer *sign.Signer) *Resolver {
	return &Resolver{
		up:         up,
		provenance: provenance,
		scanLimit:  scanLimit,
		cache:      newArtifactCache(cacheBytes),
		signer:     signer,
	}
}

// SigningEnabled reports whether cosign signature serving is on.
func (r *Resolver) SigningEnabled() bool { return r.signer != nil }

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
	if r.signer == nil {
		return nil, fmt.Errorf("%w: signing disabled", ErrVersionUnknown)
	}
	if _, err := r.ByManifestDigest(ctx, repo, chart, targetDigest); err != nil {
		return nil, err
	}
	return r.signer.Artifact(repo+"/"+chart, targetDigest)
}

// SignatureBlob resolves a signature payload/config blob by digest — the
// cold-replica path for the blob fetches that follow a .sig manifest pull.
// Signature blobs are derived per candidate manifest (cached artifacts, then
// the bounded scan), so this stays a pure function of the request.
func (r *Resolver) SignatureBlob(ctx context.Context, repo, chart, digest string) ([]byte, string, error) {
	if r.signer == nil {
		return nil, "", fmt.Errorf("%w: signing disabled", ErrBlobUnknown)
	}
	match := func(art *oci.Artifact) ([]byte, string, bool) {
		sig, err := r.signer.Artifact(repo+"/"+chart, art.ManifestDigest)
		if err != nil {
			return nil, "", false
		}
		data, mt, ok := sig.Blob(digest)
		return data, mt, ok
	}

	idx, err := r.up.Index(ctx, repo)
	if err != nil {
		return nil, "", err
	}
	entries := idx.Versions(chart)
	if len(entries) == 0 {
		return nil, "", fmt.Errorf("%w: %s/%s", ErrChartUnknown, repo, chart)
	}
	if len(entries) > r.scanLimit {
		entries = entries[:r.scanLimit]
	}
	for _, entry := range entries {
		art, err := r.ByVersion(ctx, repo, chart, entry.Version)
		if err != nil {
			return nil, "", err
		}
		if data, mt, ok := match(art); ok {
			return data, mt, nil
		}
	}
	return nil, "", fmt.Errorf("%w: %s/%s %s", ErrBlobUnknown, repo, chart, digest)
}

// Tags lists the chart's versions as OCI tags, lexically sorted (the
// distribution spec's tags/list order). Versions that can't be expressed as a
// tag are skipped.
func (r *Resolver) Tags(ctx context.Context, repo, chart string) ([]string, error) {
	idx, err := r.up.Index(ctx, repo)
	if err != nil {
		return nil, err
	}
	entries := idx.Versions(chart)
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: %s/%s", ErrChartUnknown, repo, chart)
	}
	tags := make([]string, 0, len(entries))
	for _, e := range entries {
		if tag := oci.VersionToTag(e.Version); oci.ValidTag(tag) {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
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
		idx, err := r.up.Index(ctx, repo)
		if err != nil {
			return nil, err
		}
		if len(idx.Versions(chart)) == 0 {
			return nil, fmt.Errorf("%w: %s/%s", ErrChartUnknown, repo, chart)
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

	idx, err := r.up.Index(ctx, repo)
	if err != nil {
		return nil, "", err
	}
	if len(idx.Versions(chart)) == 0 {
		return nil, "", fmt.Errorf("%w: %s/%s", ErrChartUnknown, repo, chart)
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

// scan derives candidate versions newest-first until match says stop, giving
// up (nil, nil) after scanLimit candidates. Real clients request blobs
// milliseconds after the manifest that referenced them, so this cold path is
// rare — the bound keeps its worst case (a digest that matches nothing)
// proportional to scanLimit downloads, not the whole version history.
func (r *Resolver) scan(ctx context.Context, repo, chart string, match func(*oci.Artifact) bool) (*oci.Artifact, error) {
	idx, err := r.up.Index(ctx, repo)
	if err != nil {
		return nil, err
	}
	entries := idx.Versions(chart)
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: %s/%s", ErrChartUnknown, repo, chart)
	}
	if len(entries) > r.scanLimit {
		entries = entries[:r.scanLimit]
	}
	for _, entry := range entries {
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

// build downloads and derives one chart version.
func (r *Resolver) build(ctx context.Context, repo string, entry upstream.Entry) (*oci.Artifact, error) {
	chart, err := r.up.Chart(ctx, repo, entry)
	if err != nil {
		return nil, err
	}
	var prov []byte
	if r.provenance {
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
