package upstream

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Index is the subset of a Helm repository index.yaml the proxy needs.
// Repo-added fields like created are deliberately not modeled: they drift on
// index regeneration and must never influence anything digest-bearing.
type Index struct {
	Entries map[string][]Entry `yaml:"entries"`
}

// Entry is one chart version in the index.
type Entry struct {
	Version string   `yaml:"version"`
	URLs    []string `yaml:"urls"`
	// Digest is the sha256 of the chart tarball as published by the repo
	// (bare hex by Helm convention). Optional in the spec, present in
	// practice; when set it both verifies downloads and lets a cold blob
	// request resolve a layer digest without downloading candidates.
	Digest string `yaml:"digest"`
}

// ParseIndex parses index.yaml bytes.
func ParseIndex(raw []byte) (*Index, error) {
	idx := &Index{}
	if err := yaml.Unmarshal(raw, idx); err != nil {
		return nil, fmt.Errorf("upstream: parse index.yaml: %w", err)
	}
	return idx, nil
}

// Versions returns the index entries for chart, in index order — Helm writes
// indexes newest-first, which cold-cache scans rely on as a heuristic (not for
// correctness).
func (i *Index) Versions(chart string) []Entry {
	return i.Entries[chart]
}

// FindVersion returns the entry for an exact chart version.
func (i *Index) FindVersion(chart, version string) (Entry, bool) {
	for _, e := range i.Entries[chart] {
		if e.Version == version {
			return e, true
		}
	}
	return Entry{}, false
}

// FindLayerDigest returns the entry whose published tarball digest matches the
// OCI layer digest ("sha256:<hex>").
func (i *Index) FindLayerDigest(chart, digest string) (Entry, bool) {
	hex := strings.TrimPrefix(digest, "sha256:")
	for _, e := range i.Entries[chart] {
		if e.Digest != "" && strings.TrimPrefix(e.Digest, "sha256:") == hex {
			return e, true
		}
	}
	return Entry{}, false
}
