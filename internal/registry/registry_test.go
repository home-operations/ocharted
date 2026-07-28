package registry

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/home-operations/ocharted/internal/config"
	"github.com/home-operations/ocharted/internal/oci"
	"github.com/home-operations/ocharted/internal/sign"
	"github.com/home-operations/ocharted/internal/testchart"
	"github.com/home-operations/ocharted/internal/upstream"
)

// fixture is a running registry handler backed by a fake upstream Helm repo
// serving chart "demo" in versions 1.0.0, 1.1.0, and 2.0.0+meta.
type fixture struct {
	srv         *httptest.Server
	upSrv       *httptest.Server
	cfg         *config.Config
	signer      *sign.Signer
	rewriteHost string
	repo        string // upstream host:port, i.e. the first name segment(s)
	charts      map[string][]byte
}

func newFixture(t *testing.T, mutate func(*config.Config)) *fixture {
	t.Helper()

	versions := []string{"1.0.0", "1.1.0", "2.0.0+meta"}
	charts := map[string][]byte{}
	entries := &strings.Builder{}
	mux := http.NewServeMux()
	for _, v := range versions {
		tgz := testchart.Tgz("demo", testchart.ChartYAML("demo", v), nil)
		charts[v] = tgz
		sum := sha256.Sum256(tgz)
		file := fmt.Sprintf("demo-%s.tgz", v)
		fmt.Fprintf(entries, "    - version: %q\n      digest: %s\n      urls: [%q]\n",
			v, hex.EncodeToString(sum[:]), "charts/"+file)
		mux.HandleFunc("/charts/"+file, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(tgz)
		})
	}
	// One extra chart carrying an HTTP dependency, for the rewrite tests.
	withDeps := testchart.Tgz("withdeps", `apiVersion: v2
name: withdeps
version: 1.0.0
dependencies:
  - name: redis
    version: 18.0.0
    repository: https://charts.bitnami.com/bitnami
`, nil)
	charts["withdeps"] = withDeps
	withDepsSum := sha256.Sum256(withDeps)
	mux.HandleFunc("/charts/withdeps-1.0.0.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(withDeps)
	})

	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `entries:
  demo:
%s  withdeps:
    - version: "1.0.0"
      digest: %s
      urls: ["charts/withdeps-1.0.0.tgz"]
`, entries.String(), hex.EncodeToString(withDepsSum[:]))
	})

	upstreamSrv := httptest.NewTLSServer(mux)
	t.Cleanup(upstreamSrv.Close)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if mutate != nil {
		mutate(cfg)
	}

	f := &fixture{
		upSrv:  upstreamSrv,
		cfg:    cfg,
		repo:   strings.TrimPrefix(upstreamSrv.URL, "https://"),
		charts: charts,
	}
	f.srv = f.newProxy(t)
	return f
}

// newProxy starts a fresh, cache-cold registry handler over the fixture's
// upstream — a "new replica".
func (f *fixture) newProxy(t *testing.T) *httptest.Server {
	t.Helper()
	up := upstream.New(upstream.Options{
		Timeout:       5 * time.Second,
		IndexTTL:      f.cfg.IndexTTL,
		IndexStaleTTL: f.cfg.IndexStaleTTL,
		MaxIndexBytes: f.cfg.MaxIndexBytes,
		MaxChartBytes: f.cfg.MaxChartBytes,
		AllowPrivate:  true,
		AllowedHosts:  f.cfg.UpstreamAllowlist,
		UserAgent:     "ocharted-test",
		Transport:     f.upSrv.Client().Transport,
	})
	res := NewResolver(up, ResolverOptions{
		Provenance:  f.cfg.ProvenanceEnabled,
		ScanLimit:   f.cfg.ResolveScanLimit,
		CacheBytes:  f.cfg.CacheMaxBytes,
		Signer:      f.signer,
		RewriteHost: f.rewriteHost,
	})
	s := New(f.cfg, res, slog.New(slog.NewTextHandler(io.Discard, nil)))
	proxy := httptest.NewServer(s.handler())
	t.Cleanup(proxy.Close)
	return proxy
}

// TestAccessLogClientChain asserts the access log carries the attribution
// evidence issue #9 asked for: the raw X-Forwarded-For chain (omitted for
// direct connections) and the auth outcome (denied/bypassed/authenticated,
// with the user when authenticated).
func TestAccessLogClientChain(t *testing.T) {
	f := newFixture(t, func(cfg *config.Config) {
		cfg.Users = map[string]string{"flux": "hunter2"}
		cfg.AuthBypassNets = []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}
	})
	var logBuf bytes.Buffer
	up := upstream.New(upstream.Options{
		Timeout: 5 * time.Second, IndexTTL: f.cfg.IndexTTL,
		MaxIndexBytes: f.cfg.MaxIndexBytes, MaxChartBytes: f.cfg.MaxChartBytes,
		AllowPrivate: true, UserAgent: "ocharted-test",
		Transport: f.upSrv.Client().Transport,
	})
	res := NewResolver(up, ResolverOptions{ScanLimit: f.cfg.ResolveScanLimit, CacheBytes: f.cfg.CacheMaxBytes})
	handler := New(f.cfg, res, slog.New(slog.NewJSONHandler(&logBuf, nil))).Handler()

	lastLog := func() map[string]any {
		t.Helper()
		lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
		var entry map[string]any
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
			t.Fatalf("parse log line %q: %v", lines[len(lines)-1], err)
		}
		return entry
	}

	// External client behind the gateway, no credentials: denied, chain logged.
	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "10.42.1.171:51212"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.42.0.1")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	entry := lastLog()
	if entry["auth"] != "denied" {
		t.Fatalf("auth = %v, want denied", entry["auth"])
	}
	if xff, _ := entry["xff"].([]any); len(xff) != 2 || xff[0] != "203.0.113.7" {
		t.Fatalf("xff = %v, want [203.0.113.7 10.42.0.1]", entry["xff"])
	}

	// In-cluster hairpin: bypassed.
	req = httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "10.42.1.171:51212"
	req.Header.Set("X-Forwarded-For", "10.42.0.7")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if entry = lastLog(); entry["auth"] != "bypassed" {
		t.Fatalf("auth = %v, want bypassed", entry["auth"])
	}

	// Authenticated external client, direct connection: user logged, no xff key.
	req = httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "203.0.113.7:52000"
	req.SetBasicAuth("flux", "hunter2")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	entry = lastLog()
	if entry["auth"] != "authenticated" || entry["user"] != "flux" {
		t.Fatalf("auth/user = %v/%v, want authenticated/flux", entry["auth"], entry["user"])
	}
	if _, present := entry["xff"]; present {
		t.Fatal("xff must be omitted for direct connections")
	}
}

// TestAuthBypassNetworks exercises the trusted-network bypass with crafted
// peers and X-Forwarded-For chains — a request is anonymous iff every hop
// lies within the bypass networks; anything else (or with valid credentials)
// follows the normal auth path.
func TestAuthBypassNetworks(t *testing.T) {
	f := newFixture(t, func(cfg *config.Config) {
		cfg.Users = map[string]string{"flux": "hunter2"}
		cfg.AuthBypassNets = []netip.Prefix{
			netip.MustParsePrefix("10.42.0.0/16"),
			netip.MustParsePrefix("192.168.0.0/16"),
		}
	})
	up := upstream.New(upstream.Options{
		Timeout: 5 * time.Second, IndexTTL: f.cfg.IndexTTL,
		MaxIndexBytes: f.cfg.MaxIndexBytes, MaxChartBytes: f.cfg.MaxChartBytes,
		AllowPrivate: true, UserAgent: "ocharted-test",
		Transport: f.upSrv.Client().Transport,
	})
	res := NewResolver(up, ResolverOptions{ScanLimit: f.cfg.ResolveScanLimit, CacheBytes: f.cfg.CacheMaxBytes})
	handler := New(f.cfg, res, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	cases := []struct {
		name  string
		peer  string
		xff   []string
		creds bool
		want  int
	}{
		{"in-cluster peer, no XFF", "10.42.0.5:41000", nil, false, http.StatusOK},
		{"in-cluster peer as IPv4-mapped IPv6 (dual-stack listener)", "[::ffff:10.42.0.5]:41000", nil, false, http.StatusOK},
		{"gateway peer, internal client in XFF", "10.42.0.5:41000", []string{"192.168.1.20"}, false, http.StatusOK},
		{"gateway peer, external client in XFF", "10.42.0.5:41000", []string{"203.0.113.9, 10.42.0.1"}, false, http.StatusUnauthorized},
		{"external peer, forged internal XFF", "203.0.113.9:52000", []string{"10.42.0.1"}, false, http.StatusUnauthorized},
		{"gateway peer, malformed XFF fails closed", "10.42.0.5:41000", []string{"not-an-ip"}, false, http.StatusUnauthorized},
		{"external hop hidden in second XFF header", "10.42.0.5:41000", []string{"10.42.0.7", "203.0.113.9"}, false, http.StatusUnauthorized},
		{"external peer with credentials", "203.0.113.9:52000", nil, true, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
			req.RemoteAddr = tc.peer
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if tc.creds {
				req.SetBasicAuth("flux", "hunter2")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("401 must carry the Basic challenge")
			}
		})
	}
}

func (f *fixture) get(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (f *fixture) name() string { return f.repo + "/demo" }

func TestPing(t *testing.T) {
	f := newFixture(t, nil)
	resp := f.get(t, "/v2/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ping status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Distribution-API-Version"); got != "registry/2.0" {
		t.Fatalf("missing API version header, got %q", got)
	}
}

func TestTagsList(t *testing.T) {
	f := newFixture(t, nil)
	resp := f.get(t, "/v2/"+f.name()+"/tags/list")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Name != f.name() {
		t.Fatalf("name = %q, want %q", body.Name, f.name())
	}
	want := []string{"1.0.0", "1.1.0", "2.0.0_meta"}
	if fmt.Sprint(body.Tags) != fmt.Sprint(want) {
		t.Fatalf("tags = %v, want %v", body.Tags, want)
	}
}

func TestTagsListPagination(t *testing.T) {
	f := newFixture(t, nil)
	resp := f.get(t, "/v2/"+f.name()+"/tags/list?n=2")
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tags) != 2 || body.Tags[1] != "1.1.0" {
		t.Fatalf("page 1 = %v", body.Tags)
	}
	if link := resp.Header.Get("Link"); !strings.Contains(link, `last=1.1.0`) {
		t.Fatalf("expected Link header with last=1.1.0, got %q", link)
	}

	resp = f.get(t, "/v2/"+f.name()+"/tags/list?n=2&last=1.1.0")
	body.Tags = nil
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tags) != 1 || body.Tags[0] != "2.0.0_meta" {
		t.Fatalf("page 2 = %v", body.Tags)
	}
	if resp.Header.Get("Link") != "" {
		t.Fatal("final page must not carry a Link header")
	}
}

// TestFullPullFlow walks the exact sequence an OCI client performs: manifest
// by tag, then config and layer blobs by digest, verifying every digest.
func TestFullPullFlow(t *testing.T) {
	f := newFixture(t, nil)

	resp := f.get(t, "/v2/"+f.name()+"/manifests/1.1.0")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != oci.ManifestMediaType {
		t.Fatalf("manifest content-type = %q", ct)
	}
	raw, _ := io.ReadAll(resp.Body)
	if got := resp.Header.Get("Docker-Content-Digest"); got != oci.Digest(raw) {
		t.Fatalf("manifest digest header %q != body digest %q", got, oci.Digest(raw))
	}

	var m oci.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest decode: %v", err)
	}

	for _, desc := range append([]oci.Descriptor{m.Config}, m.Layers...) {
		blobResp := f.get(t, "/v2/"+f.name()+"/blobs/"+desc.Digest)
		if blobResp.StatusCode != http.StatusOK {
			t.Fatalf("blob %s status = %d", desc.Digest, blobResp.StatusCode)
		}
		data, _ := io.ReadAll(blobResp.Body)
		if oci.Digest(data) != desc.Digest {
			t.Fatalf("blob %s content does not match its digest", desc.Digest)
		}
	}

	if string(f.charts["1.1.0"]) == "" {
		t.Fatal("fixture chart missing")
	}
	if m.Layers[0].Digest != oci.Digest(f.charts["1.1.0"]) {
		t.Fatal("layer digest must be the upstream tarball digest verbatim")
	}
}

// TestColdCacheByDigest simulates the scattered-replica / restart case: a
// second, cache-cold replica must serve the manifest and every blob requested
// purely by digest, byte-identical to the warm replica's answers. This is the
// property the whole stateless design rests on.
func TestColdCacheByDigest(t *testing.T) {
	f := newFixture(t, nil)

	resp := f.get(t, "/v2/"+f.name()+"/manifests/2.0.0_meta")
	manifest, _ := io.ReadAll(resp.Body)
	digest := resp.Header.Get("Docker-Content-Digest")

	var m oci.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}

	cold := f.newProxy(t)
	coldGet := func(path string) (*http.Response, []byte) {
		resp, err := http.Get(cold.URL + path)
		if err != nil {
			t.Fatalf("cold GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}

	coldResp, coldManifest := coldGet("/v2/" + f.name() + "/manifests/" + digest)
	if coldResp.StatusCode != http.StatusOK {
		t.Fatalf("cold manifest by digest: %d", coldResp.StatusCode)
	}
	if string(coldManifest) != string(manifest) {
		t.Fatal("cold replica derived a different manifest for the same digest")
	}

	for _, desc := range append([]oci.Descriptor{m.Config}, m.Layers...) {
		blobResp, body := coldGet("/v2/" + f.name() + "/blobs/" + desc.Digest)
		if blobResp.StatusCode != http.StatusOK {
			t.Fatalf("cold blob %s: %d", desc.Digest, blobResp.StatusCode)
		}
		if oci.Digest(body) != desc.Digest {
			t.Fatalf("cold blob %s content mismatch", desc.Digest)
		}
	}
}

func TestManifestByDigestAndHead(t *testing.T) {
	f := newFixture(t, nil)

	resp := f.get(t, "/v2/"+f.name()+"/manifests/1.0.0")
	manifest, _ := io.ReadAll(resp.Body)
	digest := resp.Header.Get("Docker-Content-Digest")

	resp = f.get(t, "/v2/"+f.name()+"/manifests/"+digest)
	byDigest, _ := io.ReadAll(resp.Body)
	if string(byDigest) != string(manifest) {
		t.Fatal("manifest by digest differs from manifest by tag")
	}

	req, _ := http.NewRequest(http.MethodHead, f.srv.URL+"/v2/"+f.name()+"/manifests/1.0.0", nil)
	headResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer func() { _ = headResp.Body.Close() }()
	if headResp.Header.Get("Docker-Content-Digest") != digest {
		t.Fatal("HEAD digest differs from GET digest")
	}
	if body, _ := io.ReadAll(headResp.Body); len(body) != 0 {
		t.Fatal("HEAD must not carry a body")
	}
}

func TestErrorMapping(t *testing.T) {
	f := newFixture(t, nil)
	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"/v2/" + f.name() + "/manifests/9.9.9", http.StatusNotFound, "MANIFEST_UNKNOWN"},
		{"/v2/" + f.repo + "/nosuchchart/tags/list", http.StatusNotFound, "NAME_UNKNOWN"},
		{"/v2/" + f.name() + "/blobs/sha256:" + strings.Repeat("0", 64), http.StatusNotFound, "BLOB_UNKNOWN"},
		{"/v2/" + f.name() + "/blobs/notadigest", http.StatusBadRequest, "DIGEST_INVALID"},
		{"/v2/onlyonesegment/tags/list", http.StatusBadRequest, "NAME_INVALID"},
		{"/v2/" + f.name() + "/manifests/bad..ref!", http.StatusBadRequest, "MANIFEST_INVALID"},
	}
	for _, tc := range cases {
		resp := f.get(t, tc.path)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.path, resp.StatusCode, tc.status)
			continue
		}
		var body struct {
			Errors []ociError `json:"errors"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || len(body.Errors) == 0 {
			t.Errorf("%s: bad error body: %v", tc.path, err)
			continue
		}
		if body.Errors[0].Code != tc.code {
			t.Errorf("%s: code = %s, want %s", tc.path, body.Errors[0].Code, tc.code)
		}
	}
}

func TestReadOnly(t *testing.T) {
	f := newFixture(t, nil)
	resp, err := http.Post(f.srv.URL+"/v2/"+f.name()+"/blobs/uploads/", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("push attempt status = %d, want 405", resp.StatusCode)
	}
}

func TestBasicAuth(t *testing.T) {
	f := newFixture(t, func(cfg *config.Config) {
		cfg.Users = map[string]string{"flux": "hunter2"}
	})

	resp := f.get(t, "/v2/")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}
	if ch := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(ch, "Basic ") {
		t.Fatalf("missing Basic challenge, got %q", ch)
	}

	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/v2/"+f.name()+"/tags/list", nil)
	req.SetBasicAuth("flux", "hunter2")
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed request: %v", err)
	}
	defer func() { _ = authed.Body.Close() }()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("authed status = %d, want 200", authed.StatusCode)
	}

	req.SetBasicAuth("flux", "wrong")
	denied, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("denied request: %v", err)
	}
	defer func() { _ = denied.Body.Close() }()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-password status = %d, want 401", denied.StatusCode)
	}

	// Probes stay reachable without credentials.
	if resp := f.get(t, "/healthz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz behind auth: %d", resp.StatusCode)
	}
}

func TestUpstreamAllowlist(t *testing.T) {
	f := newFixture(t, func(cfg *config.Config) {
		cfg.UpstreamAllowlist = []string{"charts.example.com"}
	})
	resp := f.get(t, "/v2/"+f.name()+"/tags/list")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("allowlisted-out upstream status = %d, want 403", resp.StatusCode)
	}
}

// testSigner generates an Ed25519 key and returns the signer plus the raw
// public key for verification.
func testSigner(t *testing.T) (*sign.Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	signer, err := sign.Load(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatalf("sign.Load: %v", err)
	}
	return signer, pub
}

// TestCosignSignatureFlow walks cosign's lookup convention end to end: pull a
// manifest, fetch sha256-<digest>.sig, verify the Ed25519 signature over the
// payload, and confirm a cache-cold replica serves the same signature blobs
// purely by digest.
func TestCosignSignatureFlow(t *testing.T) {
	f := newFixture(t, nil)
	f.signer, _ = testSigner(t)
	signed := f.newProxy(t)
	pubSigner := f.signer

	get := func(srv *httptest.Server, path string) (*http.Response, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}

	resp, _ := get(signed, "/v2/"+f.name()+"/manifests/1.1.0")
	target := resp.Header.Get("Docker-Content-Digest")
	sigTag := "sha256-" + strings.TrimPrefix(target, "sha256:") + ".sig"

	sigResp, sigManifest := get(signed, "/v2/"+f.name()+"/manifests/"+sigTag)
	if sigResp.StatusCode != http.StatusOK {
		t.Fatalf("signature manifest status = %d", sigResp.StatusCode)
	}
	var m oci.Manifest
	if err := json.Unmarshal(sigManifest, &m); err != nil {
		t.Fatalf("signature manifest decode: %v", err)
	}
	if len(m.Layers) != 1 || m.Layers[0].MediaType != "application/vnd.dev.cosign.simplesigning.v1+json" {
		t.Fatalf("unexpected signature layers: %+v", m.Layers)
	}

	payloadResp, payload := get(signed, "/v2/"+f.name()+"/blobs/"+m.Layers[0].Digest)
	if payloadResp.StatusCode != http.StatusOK {
		t.Fatalf("payload blob status = %d", payloadResp.StatusCode)
	}
	if oci.Digest(payload) != m.Layers[0].Digest {
		t.Fatal("payload does not match its digest")
	}

	// The signature must verify against the signer's public key, and the
	// payload must claim the target digest.
	sigB64 := m.Layers[0].Annotations["dev.cosignproject.cosign/signature"]
	rawSig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("signature annotation: %v", err)
	}
	pubPEM, err := pubSigner.PublicKeyPEM()
	if err != nil {
		t.Fatalf("PublicKeyPEM: %v", err)
	}
	block, _ := pem.Decode(pubPEM)
	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	if !ed25519.Verify(pubKey.(ed25519.PublicKey), payload, rawSig) {
		t.Fatal("signature does not verify")
	}
	if !strings.Contains(string(payload), target) {
		t.Fatal("payload does not claim the target digest")
	}

	// Config blob is fetchable too.
	if cfgResp, _ := get(signed, "/v2/"+f.name()+"/blobs/"+m.Config.Digest); cfgResp.StatusCode != http.StatusOK {
		t.Fatalf("signature config blob status = %d", cfgResp.StatusCode)
	}

	// A cache-cold replica must re-derive the same signature blobs by digest.
	cold := f.newProxy(t)
	coldResp, coldPayload := get(cold, "/v2/"+f.name()+"/blobs/"+m.Layers[0].Digest)
	if coldResp.StatusCode != http.StatusOK {
		t.Fatalf("cold payload blob status = %d", coldResp.StatusCode)
	}
	if string(coldPayload) != string(payload) {
		t.Fatal("cold replica derived different signature payload bytes")
	}
}

func TestSignatureTagWithSigningDisabled(t *testing.T) {
	f := newFixture(t, nil)
	sigTag := "sha256-" + strings.Repeat("ab", 32) + ".sig"
	resp := f.get(t, "/v2/"+f.name()+"/manifests/"+sigTag)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("signing-disabled .sig status = %d, want 404", resp.StatusCode)
	}
}

func TestSignatureForUnknownDigest(t *testing.T) {
	f := newFixture(t, nil)
	f.signer, _ = testSigner(t)
	signed := f.newProxy(t)

	sigTag := "sha256-" + strings.Repeat("00", 32) + ".sig"
	resp, err := http.Get(signed.URL + "/v2/" + f.name() + "/manifests/" + sigTag)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-digest .sig status = %d, want 404 (never sign underivable digests)", resp.StatusCode)
	}
}

// TestDependencyRewriting pulls a chart with an HTTP dependency through a
// rewrite-enabled proxy and checks the served bytes: the dependency now
// points back through the proxy, the layer digest diverges from upstream's
// (the documented trade-off), and a cache-cold replica still resolves the
// rewritten blob by digest via the scan path — the index-digest fast path
// cannot help once served bytes differ from published ones.
func TestDependencyRewriting(t *testing.T) {
	f := newFixture(t, nil)
	f.rewriteHost = "ocharted.example.com"
	proxy := f.newProxy(t)

	get := func(srv *httptest.Server, path string) (*http.Response, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}
	name := f.repo + "/withdeps"

	resp, raw := get(proxy, "/v2/"+name+"/manifests/1.0.0")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status = %d", resp.StatusCode)
	}
	var m oci.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest decode: %v", err)
	}
	if m.Layers[0].Digest == oci.Digest(f.charts["withdeps"]) {
		t.Fatal("rewritten layer digest should diverge from the upstream tarball digest")
	}

	blobResp, blob := get(proxy, "/v2/"+name+"/blobs/"+m.Layers[0].Digest)
	if blobResp.StatusCode != http.StatusOK {
		t.Fatalf("blob status = %d", blobResp.StatusCode)
	}
	if oci.Digest(blob) != m.Layers[0].Digest {
		t.Fatal("served blob does not match its digest")
	}
	if !strings.Contains(string(mustGunzip(t, blob)), "oci://ocharted.example.com/charts.bitnami.com/bitnami") {
		t.Fatal("served chart does not contain the rewritten dependency URL")
	}

	// Config blob is derived from the rewritten tarball, so it reflects the
	// rewritten dependency too.
	_, cfgBlob := get(proxy, "/v2/"+name+"/blobs/"+m.Config.Digest)
	if !strings.Contains(string(cfgBlob), "oci://ocharted.example.com/charts.bitnami.com/bitnami") {
		t.Fatalf("config blob does not reflect the rewrite: %s", cfgBlob)
	}

	// A cache-cold replica with the same rewrite config re-derives the same
	// bytes, found via the bounded scan.
	cold := f.newProxy(t)
	coldResp, coldBlob := get(cold, "/v2/"+name+"/blobs/"+m.Layers[0].Digest)
	if coldResp.StatusCode != http.StatusOK {
		t.Fatalf("cold rewritten blob status = %d", coldResp.StatusCode)
	}
	if string(coldBlob) != string(blob) {
		t.Fatal("cold replica derived different rewritten bytes")
	}

	// The default (non-rewriting) proxy still serves upstream bytes verbatim.
	verbatimResp, verbatim := get(f.srv, "/v2/"+name+"/manifests/1.0.0")
	if verbatimResp.StatusCode != http.StatusOK {
		t.Fatalf("verbatim manifest status = %d", verbatimResp.StatusCode)
	}
	var vm oci.Manifest
	if err := json.Unmarshal(verbatim, &vm); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if vm.Layers[0].Digest != oci.Digest(f.charts["withdeps"]) {
		t.Fatal("non-rewriting proxy must keep the upstream layer digest")
	}
}

// mustGunzip decompresses a gzip blob (tar content inspection is done by the
// caller via plain substring match — the rewritten Chart.yaml is inside).
func mustGunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	out, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("gunzip read: %v", err)
	}
	return out
}

// TestCacheControlHeaders checks the caching contract: by-digest responses are
// immutable, by-tag/list responses expire with the index TTL, and errors are
// never cacheable.
func TestCacheControlHeaders(t *testing.T) {
	f := newFixture(t, nil)

	resp := f.get(t, "/v2/"+f.name()+"/manifests/1.0.0")
	digest := resp.Header.Get("Docker-Content-Digest")
	wantMutable := "public, max-age=300"
	if got := resp.Header.Get("Cache-Control"); got != wantMutable {
		t.Fatalf("manifest-by-tag Cache-Control = %q, want %q", got, wantMutable)
	}

	resp = f.get(t, "/v2/"+f.name()+"/tags/list")
	if got := resp.Header.Get("Cache-Control"); got != wantMutable {
		t.Fatalf("tags/list Cache-Control = %q, want %q", got, wantMutable)
	}

	resp = f.get(t, "/v2/"+f.name()+"/manifests/"+digest)
	if got := resp.Header.Get("Cache-Control"); got != immutableCacheControl {
		t.Fatalf("manifest-by-digest Cache-Control = %q, want %q", got, immutableCacheControl)
	}

	var m oci.Manifest
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp = f.get(t, "/v2/"+f.name()+"/blobs/"+m.Config.Digest)
	if got := resp.Header.Get("Cache-Control"); got != immutableCacheControl {
		t.Fatalf("blob Cache-Control = %q, want %q", got, immutableCacheControl)
	}

	resp = f.get(t, "/v2/"+f.name()+"/manifests/9.9.9")
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("error Cache-Control = %q, want no-store", got)
	}
}
