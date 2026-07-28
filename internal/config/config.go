// Package config loads ocharted's runtime configuration from OCHARTED_*-prefixed
// environment variables. There is no config file: the service's behavior is a
// handful of scalars, and everything content-related arrives in request URLs.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the fully resolved runtime configuration. The simple fields are
// populated by env.Parse; LogLevel and Users are derived in Load so their
// parsing fails fast with a clear message.
type Config struct {
	// Port serves the OCI distribution API (/v2/...) plus /healthz and /readyz.
	Port int `env:"OCHARTED_PORT" envDefault:"8080"`

	// MetricsEnabled exposes Prometheus metrics at /metrics on MetricsPort.
	// Disabling it removes the metrics listener entirely; the probe endpoints
	// live on the main port and are unaffected.
	MetricsEnabled bool `env:"OCHARTED_METRICS_ENABLED" envDefault:"true"`
	MetricsPort    int  `env:"OCHARTED_METRICS_PORT" envDefault:"8081"`

	// Auth is an optional "user:password" list (comma-separated). When set,
	// every /v2/ request requires HTTP basic auth — the flow Flux consumes via
	// a dockerconfigjson secretRef and Renovate via a hostRule. When empty the
	// registry is anonymous, intended for cluster-internal or public read-only
	// deployments.
	Auth string `env:"OCHARTED_AUTH"`

	// AuthBypassNetworks lists CIDRs (comma-separated) whose traffic skips
	// basic auth: a request is anonymous iff its entire connection chain — the
	// TCP peer plus every X-Forwarded-For hop — lies within these networks.
	// Any hop outside means an external party was in the path (as client, or
	// as the forger of an XFF header a trusted gateway then appended the real
	// address to), so auth applies. Typical value: pod + service + LAN CIDRs,
	// letting in-cluster Flux/Renovate pull anonymously through the same
	// public hostname external clients must authenticate to. Requires every
	// listed hop (gateway, tunnel) to append to X-Forwarded-For truthfully,
	// which Envoy and Cloudflare do.
	AuthBypassNetworks []string `env:"OCHARTED_AUTH_BYPASS_NETWORKS" envSeparator:","`

	// IndexTTL is how long an upstream index.yaml is cached. It is the
	// freshness knob (how fast new chart versions appear) and the politeness
	// knob (how often upstreams are hit) in one.
	IndexTTL time.Duration `env:"OCHARTED_INDEX_TTL" envDefault:"5m"`

	// IndexStaleTTL bounds stale-if-error: when re-fetching an expired index
	// fails with a non-authoritative error (network fault, 5xx), the cached
	// copy is served instead, up to this age — so an upstream outage delays
	// version freshness instead of failing Flux reconciles. Authoritative
	// answers (404, allowlist rejection) are never masked. 0 disables.
	IndexStaleTTL time.Duration `env:"OCHARTED_INDEX_STALE_TTL" envDefault:"24h"`

	// CacheMaxBytes bounds the in-memory derived-artifact cache. The cache is
	// purely a latency/upstream-traffic optimization — every entry can be
	// re-derived from upstream, so restarts and evictions never affect
	// correctness.
	CacheMaxBytes int64 `env:"OCHARTED_CACHE_MAX_BYTES" envDefault:"268435456"`

	// MaxIndexBytes / MaxChartBytes cap upstream response sizes. The index cap
	// is generous because real indexes get big (Bitnami's is tens of MB).
	MaxIndexBytes int64 `env:"OCHARTED_MAX_INDEX_BYTES" envDefault:"67108864"`
	MaxChartBytes int64 `env:"OCHARTED_MAX_CHART_BYTES" envDefault:"33554432"`

	// UpstreamTimeout bounds one upstream HTTP exchange.
	UpstreamTimeout time.Duration `env:"OCHARTED_UPSTREAM_TIMEOUT" envDefault:"30s"`

	// UpstreamAllowlist restricts which upstream repo hosts may be proxied
	// (comma-separated path.Match globs, e.g. "*.github.io,charts.jetstack.io").
	// Empty allows any public host. This is an SSRF boundary as much as an
	// abuse guard: authenticated deployments still need it if the proxy must
	// not fetch attacker-chosen URLs.
	UpstreamAllowlist []string `env:"OCHARTED_UPSTREAM_ALLOWLIST" envSeparator:","`

	// AllowPrivateUpstreams disables the private-address dial guard, for
	// clusters proxying an internal ChartMuseum. Leave off for anything
	// reachable by untrusted clients.
	AllowPrivateUpstreams bool `env:"OCHARTED_ALLOW_PRIVATE_UPSTREAMS" envDefault:"false"`

	// ProvenanceEnabled fetches each chart's .prov file and attaches it as the
	// standard Helm provenance layer when upstream publishes one. Off by
	// default: it adds one upstream request per chart build.
	ProvenanceEnabled bool `env:"OCHARTED_PROVENANCE_ENABLED" envDefault:"false"`

	// ResolveScanLimit caps how many index entries (newest first) a cold
	// by-digest lookup will download and hash before giving up. It bounds the
	// worst case of the stateless design: a blob request whose digest is in no
	// replica's cache.
	ResolveScanLimit int `env:"OCHARTED_RESOLVE_SCAN_LIMIT" envDefault:"25"`

	// RewriteDependencies rewrites HTTP(S) dependency repository URLs inside
	// each served chart's Chart.yaml to point back through this proxy, so
	// `helm dependency update` also resolves through it. Opt-in and
	// deliberately constrained: it requires ExternalHost (rewritten bytes must
	// be identical no matter which hostname a client used) and is mutually
	// exclusive with ProvenanceEnabled (the upstream .prov signs the original
	// tarball, which rewriting invalidates). Trade-off: rewritten charts no
	// longer hash to the digest upstream's index publishes.
	RewriteDependencies bool `env:"OCHARTED_REWRITE_DEPENDENCIES" envDefault:"false"`
	// ExternalHost is the canonical name clients use to reach this proxy
	// (e.g. "ocharted.example.com"), written into rewritten dependency URLs.
	// Host only — no scheme, no path.
	ExternalHost string `env:"OCHARTED_EXTERNAL_HOST"`

	// SigningKeyPath points at a PEM-encoded Ed25519 private key (PKCS#8).
	// When set, ocharted serves cosign signature artifacts for every manifest
	// (the sha256-<digest>.sig tag convention), which Flux verifies via
	// `verify.provider: cosign`. Ed25519 only: its signatures are
	// deterministic, which the stateless multi-replica design requires.
	// Empty disables signing.
	SigningKeyPath string `env:"OCHARTED_SIGNING_KEY_PATH"`

	// LogFormat selects the slog handler: "json" (default) or "text".
	LogFormat string `env:"OCHARTED_LOG_FORMAT" envDefault:"json"`
	// DisableRequestLogs silences the per-request access log.
	DisableRequestLogs bool `env:"OCHARTED_DISABLE_REQUEST_LOGS" envDefault:"false"`

	// ShutdownTimeout bounds the graceful drain on SIGINT/SIGTERM.
	ShutdownTimeout time.Duration `env:"OCHARTED_SHUTDOWN_TIMEOUT" envDefault:"15s"`

	// LogLevel is parsed from OCHARTED_LOG_LEVEL (debug|info|warn|error) in Load.
	LogLevel slog.Level `env:"-"`
	// Users is parsed from Auth in Load (username → password). Nil means auth
	// is disabled.
	Users map[string]string `env:"-"`
	// AuthBypassNets is parsed from AuthBypassNetworks in Load.
	AuthBypassNets []netip.Prefix `env:"-"`
}

// Load reads the configuration from the environment, applies defaults,
// derives the computed fields, and validates the result.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	level := strings.TrimSpace(os.Getenv("OCHARTED_LOG_LEVEL"))
	if level == "" {
		level = "info"
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("config: invalid OCHARTED_LOG_LEVEL %q: %w", level, err)
	}

	users, err := parseAuth(cfg.Auth)
	if err != nil {
		return nil, err
	}
	cfg.Users = users

	for _, raw := range trimList(cfg.AuthBypassNetworks) {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("config: invalid OCHARTED_AUTH_BYPASS_NETWORKS entry %q: %w", raw, err)
		}
		// Normalize IPv4-mapped IPv6 prefixes (::ffff:10.42.0.0/112) to plain
		// IPv4, mirroring the Unmap applied to client addresses at match time —
		// netip.Prefix.Contains rejects address-family mismatches, so an
		// unnormalized mapped prefix would silently never match.
		if addr := prefix.Addr(); addr.Is4In6() && prefix.Bits() >= 96 {
			prefix = netip.PrefixFrom(addr.Unmap(), prefix.Bits()-96)
		}
		cfg.AuthBypassNets = append(cfg.AuthBypassNets, prefix)
	}

	cfg.UpstreamAllowlist = trimList(cfg.UpstreamAllowlist)

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// parseAuth parses OCHARTED_AUTH ("user:password,user2:password2") into a
// username→password map. An empty string yields a nil map (auth disabled).
// The password may contain ':' (only the first separates user from password).
func parseAuth(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	users := map[string]string{}
	for pair := range strings.SplitSeq(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		user, pass, ok := strings.Cut(pair, ":")
		user = strings.TrimSpace(user)
		if !ok || user == "" || pass == "" {
			return nil, fmt.Errorf("config: OCHARTED_AUTH entry %q must be user:password", pair)
		}
		users[user] = pass
	}
	return users, nil
}

func trimList(in []string) []string {
	out := in[:0]
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (c *Config) validate() error {
	if err := validatePort(c.Port, "OCHARTED_PORT"); err != nil {
		return err
	}
	if err := validatePort(c.MetricsPort, "OCHARTED_METRICS_PORT"); err != nil {
		return err
	}
	if c.MetricsEnabled && c.Port == c.MetricsPort {
		return fmt.Errorf("config: OCHARTED_PORT and OCHARTED_METRICS_PORT must differ (both %d)", c.Port)
	}
	switch strings.ToLower(c.LogFormat) {
	case "json", "text":
	default:
		return fmt.Errorf("config: invalid OCHARTED_LOG_FORMAT %q (want json or text)", c.LogFormat)
	}
	for name, v := range map[string]time.Duration{
		"OCHARTED_INDEX_TTL":        c.IndexTTL,
		"OCHARTED_UPSTREAM_TIMEOUT": c.UpstreamTimeout,
		"OCHARTED_SHUTDOWN_TIMEOUT": c.ShutdownTimeout,
	} {
		if v <= 0 {
			return fmt.Errorf("config: %s must be > 0, got %s", name, v)
		}
	}
	if c.IndexStaleTTL < 0 {
		return fmt.Errorf("config: OCHARTED_INDEX_STALE_TTL must be >= 0 (0 disables), got %s", c.IndexStaleTTL)
	}
	for name, v := range map[string]int64{
		"OCHARTED_CACHE_MAX_BYTES": c.CacheMaxBytes,
		"OCHARTED_MAX_INDEX_BYTES": c.MaxIndexBytes,
		"OCHARTED_MAX_CHART_BYTES": c.MaxChartBytes,
	} {
		if v < 1 {
			return fmt.Errorf("config: %s must be >= 1, got %d", name, v)
		}
	}
	if c.ResolveScanLimit < 1 {
		return fmt.Errorf("config: OCHARTED_RESOLVE_SCAN_LIMIT must be >= 1, got %d", c.ResolveScanLimit)
	}
	if c.RewriteDependencies {
		if c.ExternalHost == "" {
			return errors.New("config: OCHARTED_REWRITE_DEPENDENCIES requires OCHARTED_EXTERNAL_HOST (rewritten URLs must not depend on the request hostname)")
		}
		if c.ProvenanceEnabled {
			return errors.New("config: OCHARTED_REWRITE_DEPENDENCIES and OCHARTED_PROVENANCE_ENABLED are mutually exclusive (the .prov signature covers the original tarball, which rewriting invalidates)")
		}
	}
	if c.ExternalHost != "" && (strings.Contains(c.ExternalHost, "://") || strings.Contains(c.ExternalHost, "/")) {
		return fmt.Errorf("config: OCHARTED_EXTERNAL_HOST must be a bare host, got %q", c.ExternalHost)
	}
	if len(c.AuthBypassNets) > 0 && len(c.Users) == 0 {
		return errors.New("config: OCHARTED_AUTH_BYPASS_NETWORKS requires OCHARTED_AUTH (bypassing disabled auth is a configuration mistake)")
	}
	return nil
}

func validatePort(p int, name string) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("config: %s must be between 1 and 65535, got %d", name, p)
	}
	return nil
}
