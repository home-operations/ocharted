package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/ocify/internal/testchart"
)

func testOptions() Options {
	return Options{
		Timeout:       5 * time.Second,
		IndexTTL:      time.Hour,
		MaxIndexBytes: 1 << 20,
		MaxChartBytes: 1 << 20,
		AllowPrivate:  true,
		UserAgent:     "ocify-test",
	}
}

// fakeRepo serves a one-chart Helm repository over TLS and returns the client
// wired to trust it, the repo name (host:port), and the chart tarball.
func fakeRepo(t *testing.T, hits *atomic.Int64) (*Client, string, []byte) {
	t.Helper()
	chart := testchart.Tgz("demo", testchart.ChartYAML("demo", "1.0.0"), nil)
	sum := sha256.Sum256(chart)
	digest := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		_, _ = fmt.Fprintf(w, `entries:
  demo:
    - version: 1.0.0
      digest: %s
      urls:
        - charts/demo-1.0.0.tgz
`, digest)
	})
	mux.HandleFunc("/charts/demo-1.0.0.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(chart)
	})
	mux.HandleFunc("/charts/demo-1.0.0.tgz.prov", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fake provenance"))
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	opts := testOptions()
	opts.Transport = srv.Client().Transport
	return New(opts), strings.TrimPrefix(srv.URL, "https://"), chart
}

func TestIndexFetchAndCache(t *testing.T) {
	var hits atomic.Int64
	c, repo, _ := fakeRepo(t, &hits)

	for range 3 {
		idx, err := c.Index(t.Context(), repo)
		if err != nil {
			t.Fatalf("Index: %v", err)
		}
		if len(idx.Versions("demo")) != 1 {
			t.Fatalf("unexpected entries: %+v", idx.Entries)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 upstream fetch (cache hits after), got %d", hits.Load())
	}
}

func TestIndexTTLExpiry(t *testing.T) {
	var hits atomic.Int64
	c, repo, _ := fakeRepo(t, &hits)
	c.opts.IndexTTL = 0

	for range 2 {
		if _, err := c.Index(t.Context(), repo); err != nil {
			t.Fatalf("Index: %v", err)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("expected TTL=0 to refetch every call, got %d fetches", hits.Load())
	}
}

func TestChartDownloadVerifiesDigest(t *testing.T) {
	c, repo, chart := fakeRepo(t, nil)
	idx, err := c.Index(t.Context(), repo)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	entry, ok := idx.FindVersion("demo", "1.0.0")
	if !ok {
		t.Fatal("version not found")
	}

	got, err := c.Chart(t.Context(), repo, entry)
	if err != nil {
		t.Fatalf("Chart: %v", err)
	}
	if string(got) != string(chart) {
		t.Fatal("chart bytes differ from upstream")
	}

	entry.Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := c.Chart(t.Context(), repo, entry); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("expected ErrDigestMismatch, got %v", err)
	}
}

func TestChartSizeCap(t *testing.T) {
	c, repo, _ := fakeRepo(t, nil)
	c.opts.MaxChartBytes = 10

	idx, err := c.Index(t.Context(), repo)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	entry, _ := idx.FindVersion("demo", "1.0.0")
	if _, err := c.Chart(t.Context(), repo, entry); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestProvPresentAndAbsent(t *testing.T) {
	c, repo, _ := fakeRepo(t, nil)
	idx, _ := c.Index(t.Context(), repo)
	entry, _ := idx.FindVersion("demo", "1.0.0")

	prov, err := c.Prov(t.Context(), repo, entry)
	if err != nil || string(prov) != "fake provenance" {
		t.Fatalf("Prov: %v %q", err, prov)
	}

	entry.URLs = []string{"charts/missing-9.9.9.tgz"}
	prov, err = c.Prov(t.Context(), repo, entry)
	if err != nil || prov != nil {
		t.Fatalf("absent prov must be (nil, nil), got %v %v", prov, err)
	}
}

func TestIndexNotFound(t *testing.T) {
	c, repo, _ := fakeRepo(t, nil)
	if _, err := c.Index(t.Context(), repo+"/nosuchpath"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestHostAllowlist(t *testing.T) {
	cases := []struct {
		host      string
		allowlist []string
		want      bool
	}{
		{"charts.jetstack.io", nil, true},
		{"charts.jetstack.io", []string{"charts.jetstack.io"}, true},
		{"charts.jetstack.io", []string{"*.jetstack.io"}, true},
		{"evil.example.com", []string{"*.jetstack.io"}, false},
		{"Charts.Jetstack.IO", []string{"charts.jetstack.io"}, true},
	}
	for _, tc := range cases {
		if got := hostAllowed(tc.host, tc.allowlist); got != tc.want {
			t.Errorf("hostAllowed(%q, %v) = %v, want %v", tc.host, tc.allowlist, got, tc.want)
		}
	}

	c, repo, _ := fakeRepo(t, nil)
	c.opts.AllowedHosts = []string{"charts.example.com"}
	if _, err := c.Index(t.Context(), repo); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("expected ErrHostNotAllowed, got %v", err)
	}
}

// flakyRepo serves index.yaml successfully until fail is set, then answers
// with the given status code.
func flakyRepo(t *testing.T, fail *atomic.Int64) (*Client, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		if code := fail.Load(); code != 0 {
			w.WriteHeader(int(code))
			return
		}
		_, _ = w.Write([]byte("entries:\n  demo:\n    - version: 1.0.0\n      urls: [charts/demo-1.0.0.tgz]\n"))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	opts := testOptions()
	opts.IndexTTL = 0 // every call re-fetches, so failure paths trigger immediately
	opts.IndexStaleTTL = time.Hour
	opts.Transport = srv.Client().Transport
	return New(opts), strings.TrimPrefix(srv.URL, "https://")
}

func TestStaleIndexServedOnUpstreamError(t *testing.T) {
	var fail atomic.Int64
	c, repo := flakyRepo(t, &fail)

	if _, err := c.Index(t.Context(), repo); err != nil {
		t.Fatalf("initial Index: %v", err)
	}

	fail.Store(http.StatusInternalServerError)
	idx, err := c.Index(t.Context(), repo)
	if err != nil {
		t.Fatalf("expected stale index during upstream 500, got %v", err)
	}
	if len(idx.Versions("demo")) != 1 {
		t.Fatalf("stale index content wrong: %+v", idx.Entries)
	}
}

func TestStaleIndexNeverMasksAuthoritativeErrors(t *testing.T) {
	var fail atomic.Int64
	c, repo := flakyRepo(t, &fail)

	if _, err := c.Index(t.Context(), repo); err != nil {
		t.Fatalf("initial Index: %v", err)
	}

	// A 404 is an authoritative "this repo is gone" — stale must not hide it.
	fail.Store(http.StatusNotFound)
	if _, err := c.Index(t.Context(), repo); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound despite stale entry, got %v", err)
	}
}

func TestStaleIndexDisabledAndExpired(t *testing.T) {
	var fail atomic.Int64
	c, repo := flakyRepo(t, &fail)

	if _, err := c.Index(t.Context(), repo); err != nil {
		t.Fatalf("initial Index: %v", err)
	}
	fail.Store(http.StatusInternalServerError)

	// An entry older than the stale window is not served.
	c.mu.Lock()
	entry := c.cache[repo]
	entry.fetched = time.Now().Add(-2 * time.Hour)
	c.cache[repo] = entry
	c.mu.Unlock()
	if _, err := c.Index(t.Context(), repo); err == nil {
		t.Fatal("expected error for entry beyond IndexStaleTTL")
	}

	// StaleTTL=0 disables the behavior entirely.
	c.mu.Lock()
	entry.fetched = time.Now()
	c.cache[repo] = entry
	c.mu.Unlock()
	c.opts.IndexStaleTTL = 0
	if _, err := c.Index(t.Context(), repo); err == nil {
		t.Fatal("expected error with stale-if-error disabled")
	}
}

func TestPrivateAddressGuard(t *testing.T) {
	// A guarded client (no transport override, AllowPrivate=false) must refuse
	// to dial the loopback test server at connect time.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)

	opts := testOptions()
	opts.AllowPrivate = false
	c := New(opts)

	_, err := c.Index(context.Background(), strings.TrimPrefix(srv.URL, "https://"))
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("expected ErrHostNotAllowed from dial guard, got %v", err)
	}
}
