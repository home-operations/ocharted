package oci

import (
	"encoding/json"
	"testing"

	"github.com/home-operations/ocify/internal/testchart"
)

func TestBuildDeterministic(t *testing.T) {
	chart := testchart.Tgz("demo", testchart.ChartYAML("demo", "1.2.3"), map[string]string{
		"values.yaml": "replicas: 1\n",
	})

	a, err := Build(chart, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := Build(chart, nil)
	if err != nil {
		t.Fatalf("Build (second): %v", err)
	}

	if a.ManifestDigest != b.ManifestDigest {
		t.Fatalf("manifest digest not deterministic: %s vs %s", a.ManifestDigest, b.ManifestDigest)
	}
	if string(a.Manifest) != string(b.Manifest) {
		t.Fatal("manifest bytes differ between builds")
	}
	if a.ConfigDigest != b.ConfigDigest {
		t.Fatalf("config digest not deterministic: %s vs %s", a.ConfigDigest, b.ConfigDigest)
	}
}

func TestBuildManifestShape(t *testing.T) {
	chart := testchart.Tgz("demo", testchart.ChartYAML("demo", "1.2.3"), nil)

	art, err := Build(chart, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if art.Name != "demo" || art.Version != "1.2.3" {
		t.Fatalf("unexpected identity: %s %s", art.Name, art.Version)
	}
	if art.ChartDigest != Digest(chart) {
		t.Fatal("chart layer digest must be the digest of the upstream bytes verbatim")
	}

	var m Manifest
	if err := json.Unmarshal(art.Manifest, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if m.SchemaVersion != 2 || m.MediaType != ManifestMediaType {
		t.Fatalf("unexpected manifest header: %+v", m)
	}
	if m.Config.MediaType != ConfigMediaType || m.Config.Digest != art.ConfigDigest {
		t.Fatalf("unexpected config descriptor: %+v", m.Config)
	}
	if len(m.Layers) != 1 || m.Layers[0].MediaType != ChartMediaType || m.Layers[0].Digest != art.ChartDigest {
		t.Fatalf("unexpected layers: %+v", m.Layers)
	}
	if m.Annotations["org.opencontainers.image.title"] != "demo" ||
		m.Annotations["org.opencontainers.image.version"] != "1.2.3" {
		t.Fatalf("unexpected annotations: %+v", m.Annotations)
	}

	var cfg map[string]any
	if err := json.Unmarshal(art.Config, &cfg); err != nil {
		t.Fatalf("config blob is not valid JSON: %v", err)
	}
	if cfg["name"] != "demo" || cfg["version"] != "1.2.3" {
		t.Fatalf("unexpected config blob: %v", cfg)
	}
}

func TestBuildWithProvenance(t *testing.T) {
	chart := testchart.Tgz("demo", testchart.ChartYAML("demo", "1.2.3"), nil)
	prov := []byte("-----BEGIN PGP SIGNED MESSAGE-----\nfake\n")

	art, err := Build(chart, prov)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(art.Manifest, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(m.Layers) != 2 || m.Layers[1].MediaType != ProvMediaType {
		t.Fatalf("expected provenance layer, got %+v", m.Layers)
	}

	data, mt, ok := art.Blob(art.ProvDigest)
	if !ok || mt != ProvMediaType || string(data) != string(prov) {
		t.Fatal("Blob lookup for provenance digest failed")
	}

	bare, err := Build(chart, nil)
	if err != nil {
		t.Fatalf("Build without prov: %v", err)
	}
	if bare.ManifestDigest == art.ManifestDigest {
		t.Fatal("provenance layer must change the manifest digest")
	}
}

func TestBuildRejectsChartWithoutMetadata(t *testing.T) {
	chart := testchart.Tgz("demo", "apiVersion: v2\ndescription: nameless\n", nil)
	if _, err := Build(chart, nil); err == nil {
		t.Fatal("expected error for Chart.yaml without name/version")
	}
	if _, err := Build([]byte("not a tarball"), nil); err == nil {
		t.Fatal("expected error for non-gzip input")
	}
}

func TestBuildIgnoresDependencyChartYAML(t *testing.T) {
	// A bundled dependency's Chart.yaml sits deeper (charts/dep/Chart.yaml) and
	// must not be mistaken for the chart's own metadata. Note map iteration
	// order means the dep file may be written before or after; the depth rule,
	// not order, is what protects us.
	chart := testchart.Tgz("demo", testchart.ChartYAML("demo", "1.2.3"), map[string]string{
		"charts/dep/Chart.yaml": testchart.ChartYAML("dep", "9.9.9"),
	})
	art, err := Build(chart, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if art.Name != "demo" {
		t.Fatalf("picked up wrong Chart.yaml: %s", art.Name)
	}
}

func TestVersionTagRoundTrip(t *testing.T) {
	cases := map[string]string{
		"1.2.3":        "1.2.3",
		"1.2.3+build7": "1.2.3_build7",
		"v0.1.0-rc.1":  "v0.1.0-rc.1",
	}
	for version, tag := range cases {
		if got := VersionToTag(version); got != tag {
			t.Errorf("VersionToTag(%q) = %q, want %q", version, got, tag)
		}
		if got := TagToVersion(tag); got != version {
			t.Errorf("TagToVersion(%q) = %q, want %q", tag, got, version)
		}
		if !ValidTag(tag) {
			t.Errorf("ValidTag(%q) = false, want true", tag)
		}
	}
	if ValidTag("-leading-dash") || ValidTag("") {
		t.Error("invalid tags accepted")
	}
}
