//go:build e2e

// Package e2e exercises ocify with the real helm and cosign CLIs against a
// real public upstream (charts.jetstack.io). It needs network access and both
// binaries on PATH — run it via `mise run test-e2e`; regular `go test ./...`
// never builds it (the e2e tag).
package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/ocify/internal/config"
	"github.com/home-operations/ocify/internal/registry"
	"github.com/home-operations/ocify/internal/sign"
	"github.com/home-operations/ocify/internal/upstream"
)

const (
	upstreamRepo = "charts.jetstack.io"
	chartName    = "cert-manager"
	chartVersion = "v1.18.2"
)

// startProxy runs the registry handler on a loopback listener with a real
// (guarded, no transport override) upstream client — the same wiring as
// cmd/ocify, minus the process shell.
func startProxy(t *testing.T, signer *sign.Signer, rewriteHost string) *httptest.Server {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	up := upstream.New(upstream.Options{
		Timeout:       cfg.UpstreamTimeout,
		IndexTTL:      cfg.IndexTTL,
		MaxIndexBytes: cfg.MaxIndexBytes,
		MaxChartBytes: cfg.MaxChartBytes,
		UserAgent:     "ocify-e2e",
	})
	res := registry.NewResolver(up, registry.ResolverOptions{
		Provenance:  cfg.ProvenanceEnabled,
		ScanLimit:   cfg.ResolveScanLimit,
		CacheBytes:  cfg.CacheMaxBytes,
		Signer:      signer,
		RewriteHost: rewriteHost,
	})
	srv := httptest.NewServer(registry.New(cfg, res, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH; skipping", name)
	}
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestHelmPullThroughProxy pulls a real chart through ocify with the helm CLI
// and checks the tarball is byte-identical to what upstream published.
func TestHelmPullThroughProxy(t *testing.T) {
	requireTool(t, "helm")
	srv := startProxy(t, nil, "")
	host := strings.TrimPrefix(srv.URL, "http://")
	dir := t.TempDir()

	run(t, dir, "helm", "pull",
		"oci://"+host+"/"+upstreamRepo+"/"+chartName,
		"--version", chartVersion, "--plain-http")

	pulled, err := os.ReadFile(filepath.Join(dir, chartName+"-"+chartVersion+".tgz"))
	if err != nil {
		t.Fatalf("pulled chart missing: %v", err)
	}

	// The proxy serves the upstream tarball verbatim, so the pulled bytes must
	// hash to the digest the upstream index publishes for this version.
	up := upstream.New(upstream.Options{
		Timeout: 30 * time.Second, IndexTTL: time.Minute,
		MaxIndexBytes: 64 << 20, MaxChartBytes: 32 << 20, UserAgent: "ocify-e2e",
	})
	idx, err := up.Index(t.Context(), upstreamRepo)
	if err != nil {
		t.Fatalf("upstream index: %v", err)
	}
	entry, ok := idx.FindVersion(chartName, chartVersion)
	if !ok {
		t.Fatalf("version %s missing from upstream index", chartVersion)
	}
	sum := sha256.Sum256(pulled)
	if got, want := hex.EncodeToString(sum[:]), strings.TrimPrefix(entry.Digest, "sha256:"); got != want {
		t.Fatalf("pulled tarball digest %s != upstream index digest %s", got, want)
	}
}

// TestCosignVerifyThroughProxy verifies a proxied chart with the cosign CLI
// against the proxy's Ed25519 key — the exact flow a Flux
// `verify.provider: cosign` consumer depends on.
func TestCosignVerifyThroughProxy(t *testing.T) {
	requireTool(t, "cosign")

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	signer, err := sign.Load(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}))
	if err != nil {
		t.Fatalf("sign.Load: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "cosign.pub")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o600); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	srv := startProxy(t, signer, "")
	host := strings.TrimPrefix(srv.URL, "http://")

	out := run(t, dir, "cosign", "verify",
		"--key", pubPath,
		"--insecure-ignore-tlog=true",
		"--allow-http-registry",
		host+"/"+upstreamRepo+"/"+chartName+":"+chartVersion)
	if !strings.Contains(out, "cosign claims were validated") {
		t.Fatalf("unexpected cosign verify output:\n%s", out)
	}
}

// TestDependencyRewriteThroughProxy pulls a real chart that declares HTTP
// dependencies (kube-prometheus-stack) through a rewrite-enabled proxy and
// checks the unpacked Chart.yaml points its dependencies back through the
// proxy's canonical name.
func TestDependencyRewriteThroughProxy(t *testing.T) {
	requireTool(t, "helm")
	const (
		depsRepo    = "prometheus-community.github.io/helm-charts"
		depsChart   = "kube-prometheus-stack"
		depsVersion = "65.0.0"
	)

	srv := startProxy(t, nil, "ocify.example.com")
	host := strings.TrimPrefix(srv.URL, "http://")
	dir := t.TempDir()

	run(t, dir, "helm", "pull",
		"oci://"+host+"/"+depsRepo+"/"+depsChart,
		"--version", depsVersion, "--plain-http", "--untar")

	raw, err := os.ReadFile(filepath.Join(dir, depsChart, "Chart.yaml"))
	if err != nil {
		t.Fatalf("unpacked Chart.yaml missing: %v", err)
	}
	if !strings.Contains(string(raw), "oci://ocify.example.com/grafana.github.io/helm-charts") {
		t.Fatalf("dependencies not rewritten through the proxy:\n%s", raw)
	}
	if strings.Contains(string(raw), "repository: https://") {
		t.Fatalf("HTTP dependency repositories survived rewriting:\n%s", raw)
	}
}
