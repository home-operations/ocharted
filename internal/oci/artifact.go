// Package oci turns a Helm chart tarball into an OCI artifact — the manifest,
// config blob, and digests a registry client expects. Every function here is a
// pure transformation of its input bytes: no clocks, no randomness, no I/O.
// That determinism is what lets any replica (or a restarted one) re-derive
// byte-identical answers, which the whole stateless design rests on.
package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Media types for Helm charts distributed as OCI artifacts, per the Helm OCI
// convention, plus the standard OCI image manifest type.
const (
	ManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	ConfigMediaType   = "application/vnd.cncf.helm.config.v1+json"
	ChartMediaType    = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	ProvMediaType     = "application/vnd.cncf.helm.chart.provenance.v1.prov"
)

// maxDecompressedChartYAML caps the Chart.yaml read out of the tarball, so a
// crafted gzip bomb can't balloon memory past the compressed-size cap applied
// at download time.
const maxDecompressedChartYAML = 4 << 20

// Descriptor is an OCI content descriptor.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Manifest is an OCI image manifest. Field order is fixed by the struct, so
// marshaling is deterministic.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Artifact is the fully derived OCI form of one chart version: everything a
// registry response for it needs, addressable by digest.
type Artifact struct {
	Name    string
	Version string

	Manifest       []byte
	ManifestDigest string

	Config       []byte
	ConfigDigest string

	Chart       []byte
	ChartDigest string

	// Prov is the upstream .tgz.prov file when one exists (nil otherwise),
	// carried through as the standard Helm provenance layer so the upstream
	// maintainer's signature survives the OCI translation.
	Prov       []byte
	ProvDigest string
}

// Blob returns the blob content matching digest, if this artifact holds it.
func (a *Artifact) Blob(digest string) ([]byte, string, bool) {
	switch digest {
	case a.ConfigDigest:
		return a.Config, ConfigMediaType, true
	case a.ChartDigest:
		return a.Chart, ChartMediaType, true
	case a.ProvDigest:
		if a.Prov != nil {
			return a.Prov, ProvMediaType, true
		}
	}
	return nil, "", false
}

// Digest returns the canonical sha256 digest string for b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Build derives the OCI artifact for a chart tarball. prov may be nil. The
// output is a pure function of (chart, prov): identical inputs yield
// byte-identical manifests on every call, on every replica.
func Build(chart, prov []byte) (*Artifact, error) {
	meta, err := chartMetadata(chart)
	if err != nil {
		return nil, err
	}

	config, err := configBlob(meta)
	if err != nil {
		return nil, err
	}

	name, _ := meta["name"].(string)
	version, _ := meta["version"].(string)
	if name == "" || version == "" {
		return nil, errors.New("oci: Chart.yaml is missing name or version")
	}

	art := &Artifact{
		Name:         name,
		Version:      version,
		Config:       config,
		ConfigDigest: Digest(config),
		Chart:        chart,
		ChartDigest:  Digest(chart),
	}

	manifest := Manifest{
		SchemaVersion: 2,
		MediaType:     ManifestMediaType,
		Config: Descriptor{
			MediaType: ConfigMediaType,
			Digest:    art.ConfigDigest,
			Size:      int64(len(config)),
		},
		Layers: []Descriptor{{
			MediaType: ChartMediaType,
			Digest:    art.ChartDigest,
			Size:      int64(len(chart)),
		}},
		Annotations: annotations(meta),
	}
	if prov != nil {
		art.Prov = prov
		art.ProvDigest = Digest(prov)
		manifest.Layers = append(manifest.Layers, Descriptor{
			MediaType: ProvMediaType,
			Digest:    art.ProvDigest,
			Size:      int64(len(prov)),
		})
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("oci: marshal manifest: %w", err)
	}
	art.Manifest = raw
	art.ManifestDigest = Digest(raw)
	return art, nil
}

// annotations derives the manifest annotations from chart metadata only —
// never from the index or a clock, both of which can drift between requests.
func annotations(meta map[string]any) map[string]string {
	ann := map[string]string{}
	if v, _ := meta["name"].(string); v != "" {
		ann["org.opencontainers.image.title"] = v
	}
	if v, _ := meta["version"].(string); v != "" {
		ann["org.opencontainers.image.version"] = v
	}
	if v, _ := meta["description"].(string); v != "" {
		ann["org.opencontainers.image.description"] = v
	}
	return ann
}

// configBlob renders chart metadata as the Helm OCI config blob. Determinism
// note: encoding/json marshals map keys sorted, which is the canonical form
// relied on here — the same Chart.yaml bytes always produce the same blob.
func configBlob(meta map[string]any) ([]byte, error) {
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("oci: marshal config blob: %w", err)
	}
	return raw, nil
}

// chartMetadata extracts and parses Chart.yaml from the chart tarball. The
// config blob is sourced from the tarball — not the repo index entry — because
// index entries grow repo-added fields (created, urls, digest) that change on
// index regeneration and would silently shift digests.
func chartMetadata(chart []byte) (map[string]any, error) {
	gz, err := gzip.NewReader(bytes.NewReader(chart))
	if err != nil {
		return nil, fmt.Errorf("oci: chart is not a gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("oci: chart tarball has no Chart.yaml")
		}
		if err != nil {
			return nil, fmt.Errorf("oci: read chart tarball: %w", err)
		}
		if !isChartYAML(hdr.Name) {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(tr, maxDecompressedChartYAML))
		if err != nil {
			return nil, fmt.Errorf("oci: read Chart.yaml: %w", err)
		}
		var meta map[string]any
		if err := yaml.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("oci: parse Chart.yaml: %w", err)
		}
		return normalize(meta).(map[string]any), nil
	}
}

// isChartYAML matches the chart's own Chart.yaml (<chartdir>/Chart.yaml),
// not one belonging to a bundled dependency under charts/.
func isChartYAML(name string) bool {
	name = strings.TrimPrefix(name, "./")
	dir, file, ok := strings.Cut(name, "/")
	return ok && dir != "" && file == "Chart.yaml"
}

// normalize rewrites YAML's map[any]any containers into map[string]any so the
// metadata survives json.Marshal regardless of how the YAML library shaped it.
func normalize(v any) any {
	switch t := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[fmt.Sprint(k)] = normalize(val)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = normalize(val)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, val := range t {
			s[i] = normalize(val)
		}
		return s
	default:
		return v
	}
}
