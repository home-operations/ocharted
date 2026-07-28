package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8080 || cfg.MetricsPort != 8081 || !cfg.MetricsEnabled {
		t.Fatalf("unexpected port defaults: %+v", cfg)
	}
	if cfg.IndexTTL != 5*time.Minute {
		t.Fatalf("unexpected IndexTTL: %s", cfg.IndexTTL)
	}
	if cfg.Users != nil {
		t.Fatal("auth should default to disabled")
	}
	if cfg.ProvenanceEnabled || cfg.AllowPrivateUpstreams {
		t.Fatal("opt-in features must default off")
	}
}

func TestLoadAuthAndAllowlist(t *testing.T) {
	t.Setenv("OCIFY_AUTH", "flux:hunter2, renovate:s3cret")
	t.Setenv("OCIFY_UPSTREAM_ALLOWLIST", "*.github.io, charts.jetstack.io ,")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Users["flux"] != "hunter2" || cfg.Users["renovate"] != "s3cret" {
		t.Fatalf("unexpected users: %v", cfg.Users)
	}
	if len(cfg.UpstreamAllowlist) != 2 || cfg.UpstreamAllowlist[0] != "*.github.io" {
		t.Fatalf("unexpected allowlist: %v", cfg.UpstreamAllowlist)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"bad auth entry":  {"OCIFY_AUTH": "nopassword"},
		"port collision":  {"OCIFY_PORT": "9000", "OCIFY_METRICS_PORT": "9000"},
		"bad log format":  {"OCIFY_LOG_FORMAT": "xml"},
		"bad log level":   {"OCIFY_LOG_LEVEL": "loud"},
		"zero ttl":        {"OCIFY_INDEX_TTL": "0s"},
		"zero scan limit": {"OCIFY_RESOLVE_SCAN_LIMIT": "0"},
		"zero cache":      {"OCIFY_CACHE_MAX_BYTES": "0"},
	}
	for name, envs := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			for k, v := range envs {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}
