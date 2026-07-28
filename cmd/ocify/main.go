// Command ocify runs a stateless OCI registry proxy for classic Helm
// repositories: any chart in any HTTP Helm repo becomes pullable as an OCI
// artifact (oci://<ocify-host>/<upstream-host[/path]>/<chart>) with no
// onboarding, no storage, and no publish step. Every response is derived on
// demand from upstream, deterministically, so replicas need no coordination.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/home-operations/ocify/internal/config"
	"github.com/home-operations/ocify/internal/registry"
	"github.com/home-operations/ocify/internal/sign"
	"github.com/home-operations/ocify/internal/upstream"
	"github.com/spf13/cobra"
)

// Build metadata, stamped via -ldflags at release time.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ocify:", err)
		os.Exit(1)
	}
}

// newRootCmd wires the CLI: bare `ocify` runs the server (the container
// entrypoint).
func newRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ocify",
		Short: "A stateless OCI registry proxy for classic Helm repositories",
		Long: "ocify serves any HTTP Helm repository as a read-only OCI registry, packaging charts\n" +
			"on demand so Flux OCIRepositories and `helm install oci://` work without the upstream\n" +
			"publishing OCI artifacts. With no subcommand it runs the server.",
		Version:       fmt.Sprintf("%s (commit %s)", version, commit),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			// Server faults log structured (the container-log contract), not via
			// cobra's plain-text error path.
			if err := run(); err != nil {
				slog.Error("fatal error", "error", err)
				os.Exit(1)
			}
			return nil
		},
	}
}

func run() error {
	setMemLimit()

	// The first signal triggers a graceful drain; re-arm the default handler so
	// a second signal force-quits instead of being swallowed during a slow drain.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	up := upstream.New(upstream.Options{
		Timeout:       cfg.UpstreamTimeout,
		IndexTTL:      cfg.IndexTTL,
		MaxIndexBytes: cfg.MaxIndexBytes,
		MaxChartBytes: cfg.MaxChartBytes,
		AllowPrivate:  cfg.AllowPrivateUpstreams,
		AllowedHosts:  cfg.UpstreamAllowlist,
		UserAgent:     "ocify/" + version,
	})
	var signer *sign.Signer
	if cfg.SigningKeyPath != "" {
		keyPEM, err := os.ReadFile(cfg.SigningKeyPath)
		if err != nil {
			return fmt.Errorf("read OCIFY_SIGNING_KEY_PATH: %w", err)
		}
		if signer, err = sign.Load(keyPEM); err != nil {
			return err
		}
	}
	var rewriteHost string
	if cfg.RewriteDependencies {
		rewriteHost = cfg.ExternalHost
	}
	resolver := registry.NewResolver(up, registry.ResolverOptions{
		Provenance:  cfg.ProvenanceEnabled,
		ScanLimit:   cfg.ResolveScanLimit,
		CacheBytes:  cfg.CacheMaxBytes,
		Signer:      signer,
		RewriteHost: rewriteHost,
	})

	if cfg.Users == nil {
		// Anonymous mode is a fine default for cluster-internal deployments;
		// note it at info level so an accidental public anonymous deployment is
		// at least visible in the boot log.
		logger.Info("registry auth disabled (no OCIFY_AUTH); serving anonymously")
	}
	if len(cfg.UpstreamAllowlist) == 0 {
		logger.Info("no OCIFY_UPSTREAM_ALLOWLIST; any public upstream host may be proxied")
	}

	registry.RecordBuildInfo(version, commit)

	logger.Info("starting ocify",
		"version", version,
		"commit", commit,
		"http_port", cfg.Port,
		"metrics_port", cfg.MetricsPort,
		"index_ttl", cfg.IndexTTL.String(),
		"auth", cfg.Users != nil,
		"allowlist_hosts", len(cfg.UpstreamAllowlist),
		"provenance", cfg.ProvenanceEnabled,
		"signing", signer != nil,
		"rewrite_dependencies", cfg.RewriteDependencies,
		"gomaxprocs", runtime.GOMAXPROCS(0),
	)

	return registry.New(cfg, resolver, logger).Run(ctx)
}

// newLogger builds the root logger: JSON by default (the container-friendly
// format), text on request, always to stdout.
func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if strings.EqualFold(cfg.LogFormat, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

// setMemLimit caps the Go heap (GOMEMLIMIT) at 90% of the cgroup memory limit
// when one is set, so the GC reclaims before the container is OOM-killed. It
// is a silent no-op outside a memory-limited cgroup.
func setMemLimit() {
	_, _ = memlimit.SetGoMemLimitWithOpts(
		memlimit.WithRatio(0.9),
		memlimit.WithProvider(memlimit.FromCgroup),
		memlimit.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
}
