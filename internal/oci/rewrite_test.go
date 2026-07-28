package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/home-operations/ocharted/internal/testchart"
	"go.yaml.in/yaml/v4"
)

const depsChartYAML = `apiVersion: v2
name: umbrella
version: 1.0.0
dependencies:
  - name: redis
    version: 18.0.0
    repository: https://charts.bitnami.com/bitnami
  - name: local
    version: 1.0.0
    repository: file://../local
  - name: aliased
    version: 1.0.0
    repository: "@myrepo"
  - name: already-oci
    version: 1.0.0
    repository: oci://ghcr.io/example/charts
  - name: plain-http
    version: 1.0.0
    repository: http://charts.internal.example/stable/
`

// chartYAMLFrom extracts and parses Chart.yaml out of a chart tarball.
func chartYAMLFrom(t *testing.T, chart []byte) map[string]any {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(chart))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			t.Fatal("no Chart.yaml in tarball")
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if !isChartYAML(hdr.Name) {
			continue
		}
		raw, _ := io.ReadAll(tr)
		var meta map[string]any
		if err := yaml.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("parse Chart.yaml: %v", err)
		}
		return meta
	}
}

func depRepos(t *testing.T, meta map[string]any) map[string]string {
	t.Helper()
	repos := map[string]string{}
	deps, _ := meta["dependencies"].([]any)
	for _, d := range deps {
		dep := d.(map[string]any)
		repos[dep["name"].(string)] = dep["repository"].(string)
	}
	return repos
}

func TestRewriteDependencies(t *testing.T) {
	chart := testchart.Tgz("umbrella", depsChartYAML, map[string]string{
		"values.yaml": "replicas: 1\n",
	})

	out, err := RewriteDependencies(chart, "ocharted.example.com")
	if err != nil {
		t.Fatalf("RewriteDependencies: %v", err)
	}
	if bytes.Equal(out, chart) {
		t.Fatal("expected rewritten bytes to differ from input")
	}

	repos := depRepos(t, chartYAMLFrom(t, out))
	want := map[string]string{
		"redis":       "oci://ocharted.example.com/charts.bitnami.com/bitnami",
		"local":       "file://../local",
		"aliased":     "@myrepo",
		"already-oci": "oci://ghcr.io/example/charts",
		"plain-http":  "oci://ocharted.example.com/charts.internal.example/stable",
	}
	for name, repo := range want {
		if repos[name] != repo {
			t.Errorf("dependency %s: repository = %q, want %q", name, repos[name], repo)
		}
	}

	// The rewritten tarball must still be a valid chart that Build accepts,
	// with identity intact and other files preserved.
	art, err := Build(out, nil)
	if err != nil {
		t.Fatalf("Build on rewritten chart: %v", err)
	}
	if art.Name != "umbrella" || art.Version != "1.0.0" {
		t.Fatalf("rewritten chart identity changed: %s %s", art.Name, art.Version)
	}
	if !tarballHasFile(t, out, "umbrella/values.yaml") {
		t.Fatal("sibling file lost during repack")
	}
}

func TestRewriteDependenciesDeterministic(t *testing.T) {
	chart := testchart.Tgz("umbrella", depsChartYAML, nil)
	a, err := RewriteDependencies(chart, "ocharted.example.com")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	b, err := RewriteDependencies(chart, "ocharted.example.com")
	if err != nil {
		t.Fatalf("rewrite (second): %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("rewriting is not deterministic")
	}
	if Digest(a) == Digest(chart) {
		t.Fatal("rewritten digest should differ from the original")
	}
}

func TestRewriteDependenciesNoopKeepsBytes(t *testing.T) {
	// A chart without HTTP dependencies must come back byte-identical, so
	// digest fidelity to upstream is preserved whenever possible.
	plain := testchart.Tgz("demo", testchart.ChartYAML("demo", "1.2.3"), nil)
	out, err := RewriteDependencies(plain, "ocharted.example.com")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatal("chart without HTTP dependencies must pass through unchanged")
	}

	ociOnly := testchart.Tgz("demo", `apiVersion: v2
name: demo
version: 1.2.3
dependencies:
  - name: dep
    version: 1.0.0
    repository: oci://ghcr.io/example/charts
`, nil)
	out, err = RewriteDependencies(ociOnly, "ocharted.example.com")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !bytes.Equal(out, ociOnly) {
		t.Fatal("chart with only OCI dependencies must pass through unchanged")
	}
}

func TestRewriteRepoURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://charts.jetstack.io", "oci://p/charts.jetstack.io", true},
		{"https://example.com/charts/", "oci://p/example.com/charts", true},
		{"http://host:8443/repo", "oci://p/host:8443/repo", true},
		{"file://../sibling", "", false},
		{"@alias", "", false},
		{"alias:stable", "", false},
		{"oci://ghcr.io/x", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := rewriteRepoURL(tc.in, "p")
		if ok != tc.ok || got != tc.want {
			t.Errorf("rewriteRepoURL(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func tarballHasFile(t *testing.T, chart []byte, name string) bool {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(chart))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if strings.TrimPrefix(hdr.Name, "./") == name {
			return true
		}
	}
}
