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
	t.Setenv("OCHARTED_AUTH", "flux:hunter2, renovate:s3cret")
	t.Setenv("OCHARTED_UPSTREAM_ALLOWLIST", "*.github.io, charts.jetstack.io ,")

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

func TestLoadAuthBypassNetworks(t *testing.T) {
	t.Setenv("OCHARTED_AUTH", "flux:hunter2")
	t.Setenv("OCHARTED_AUTH_BYPASS_NETWORKS", "10.42.0.0/16, 192.168.0.0/16")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AuthBypassNets) != 2 || cfg.AuthBypassNets[0].String() != "10.42.0.0/16" {
		t.Fatalf("unexpected bypass networks: %v", cfg.AuthBypassNets)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"bad auth entry":  {"OCHARTED_AUTH": "nopassword"},
		"port collision":  {"OCHARTED_PORT": "9000", "OCHARTED_METRICS_PORT": "9000"},
		"bad log format":  {"OCHARTED_LOG_FORMAT": "xml"},
		"bad log level":   {"OCHARTED_LOG_LEVEL": "loud"},
		"zero ttl":        {"OCHARTED_INDEX_TTL": "0s"},
		"zero scan limit": {"OCHARTED_RESOLVE_SCAN_LIMIT": "0"},
		"zero cache":      {"OCHARTED_CACHE_MAX_BYTES": "0"},
		"rewrite without external host": {
			"OCHARTED_REWRITE_DEPENDENCIES": "true",
		},
		"rewrite with provenance": {
			"OCHARTED_REWRITE_DEPENDENCIES": "true",
			"OCHARTED_EXTERNAL_HOST":        "ocharted.example.com",
			"OCHARTED_PROVENANCE_ENABLED":   "true",
		},
		"external host with scheme": {
			"OCHARTED_EXTERNAL_HOST": "https://ocharted.example.com",
		},
		"bad bypass network": {
			"OCHARTED_AUTH":                 "flux:hunter2",
			"OCHARTED_AUTH_BYPASS_NETWORKS": "10.42.0.0/16,not-a-cidr",
		},
		"bypass without auth": {
			"OCHARTED_AUTH_BYPASS_NETWORKS": "10.42.0.0/16",
		},
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
