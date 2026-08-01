# AGENTS.md: ocharted

Guidance for AI coding agents (and humans) writing Go in this repository.
These are the home-operations fleet's shared Go conventions, kept in sync
word-for-word across every Go service/CLI in the org; propagate any change
here to the others.

## Working in this repo: AI usage, commits, and safety

This repo doesn't carry its own `CONTRIBUTING.md`; GitHub serves the org-wide
one from [`home-operations/.github`](https://github.com/home-operations/.github/blob/main/CONTRIBUTING.md),
which includes an AI Usage Policy that applies to any AI coding agent here:
assistive use only, a human must author the majority of any change, AI use
must be disclosed, a human reviews every line before submission, and the
contributor must be able to explain any line a reviewer asks about. AI must
never write the PR description, an issue, or a reply to a human on the
contributor's behalf. Read the policy itself rather than trusting this
summary; it can change without this file being updated to match.

- PR titles follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):
  `<type>[(scope)][!]: <description>` (e.g. `fix(config): reject a negative
timeout`), which is what drives release-please's version bumps. Individual
  commit messages don't have to follow the format, though matching it is
  fine. Sign off commits: `git commit -s`.
- Never `git commit`, `git push`, or open a PR unless asked to. Ask before
  any destructive or hard-to-reverse action (force-push, `git reset --hard`,
  deleting a branch, rewriting history) instead of defaulting to it.
- Never touch secrets or gitignored files (`*.key`, `*.crt`, `.env`,
  anything `.gitignore` matches). This fleet passes signing keys and
  webhook secrets by path or env var specifically so they're never
  committed; don't be the exception.
- Don't state a library's API, flags, or defaults from memory: verify
  against `pkg.go.dev`, the vendored source in the module cache, or this
  project's own code. This fleet's dependencies (`caarlos0/env`,
  `spf13/pflag`, `k8s.io/client-go`, and friends) change behavior between
  major versions in ways that are easy to get subtly wrong from
  recollection alone.
- After a change, run `mise run test` and `mise run lint` (and `mise run
generate-check`) before calling it done. Don't claim untested code works.

## Baseline

- **Idiomatic.** Follow [Effective Go](https://go.dev/doc/effective_go) and
  the [Code Review Comments](https://go.dev/wiki/CodeReviewComments) wiki.
  `gofmt -s` runs on every staged `.go` file via lefthook and again in CI:
  never hand-format, and don't fight it with inline exceptions. Comments
  explain non-obvious constraints only (a hidden invariant, why a workaround
  exists, what would surprise a reader), matching this repo's own style
  throughout `internal/`; don't narrate what good naming already says, and
  don't reference the current change or past behavior in a comment: that
  belongs in the PR description and rots as the code moves on.
- **Go 1.26**, matching `.mise/config.toml`'s `tools.go` and the `go`
  directive in `go.mod`; bump both together, never one without the other.
  When a newer construct is genuinely more idiomatic, use it: Go 1.26 added
  `errors.AsType[T](err)`, a generic type-safe replacement for the
  `var t *T; errors.As(err, &t)` two-step; prefer it in new code. `go fix`
  (rebuilt in 1.26 as a modernizer runner on `go vet`'s analysis) surfaces
  these mechanical migrations; run it after a toolchain bump.
- **Idempotent.** Reconcilers, code generators (`mise run generate`), and CLI
  subcommands must be safe to re-run: identical input yields identical
  output/state, with no accumulating side effects on a second invocation.
  This repo is the clearest example in the fleet of the principle: every
  cache entry is re-derivable from upstream (see `internal/config/config.go`'s
  doc comment on `CacheMaxBytes`), so a restart or an extra replica never
  affects correctness, only latency. Keep new features consistent with
  that: don't introduce state that can't be rebuilt from upstream.
- **DRY and minimal, without premature abstraction.** Three similar call
  sites are fine as-is; don't introduce an interface, options struct, or
  generic helper until a real third caller needs the variance it buys.
  Touch only what the task requires: don't refactor or "improve" adjacent
  code, and match the existing style even where you'd do it differently.
  Remove imports, variables, and functions your own change orphaned; leave
  pre-existing dead code alone and mention it instead of deleting it
  unprompted.
- **Unit tested**, table-driven via `t.Run` subtests. Stdlib `testing` is
  the convention in this repo (see `internal/config/config_test.go`,
  `internal/oci/*_test.go`, `internal/registry/registry_test.go`); no
  `testify` here, keep it that way unless a table's assertions genuinely
  get hard to read without it. `go test -race` is the floor (`mise run
test`); don't submit goroutine-touching code you haven't run under
  `-race`.
- **`log/slog`**, JSON handler to stdout by default (`OCHARTED_LOG_FORMAT=text`
  for local runs), never a third-party logging library. `slog.SetDefault`
  is called once in `main` (`newLogger`); everything downstream either uses
  the package-level `slog.*` calls or receives the constructed
  `*slog.Logger` explicitly (see `registry.New(cfg, resolver, logger)`);
  don't reach for a second way to get a logger.
- **`github.com/caarlos0/env/v11`** for configuration: `internal/config.Config`,
  populated by `env.Parse`, behind `Load()`, which also derives computed
  fields (`LogLevel`, `Users`, `AuthBypassNets`) and calls `validate()`: fail
  fast on invalid config at startup instead of letting a bad value surface
  later as a runtime error. Doc-comment every field with what it does and
  why its `envDefault` is what it is: the struct doubles as the config
  reference, and keep it up to date when you add a field.
- **`github.com/spf13/pflag`, via `github.com/spf13/cobra`**: this repo is a
  fleet reference example for that combination (`cmd/ocharted/main.go`):
  bare `ocharted` runs the server, `SilenceUsage`/`SilenceErrors` are set so
  server faults log through `slog` instead of cobra's plain-text error
  path. If a future subcommand is purely env-configured (no flags of its
  own), it still doesn't need its own flag parsing; only add pflag/cobra
  wiring where a human actually types a flag or picks a subcommand.

## Project layout

`cmd/ocharted/main.go` is the entrypoint; everything else lives under
`internal/`. This repo doesn't export a package for other repos to import
(contrast `github.com/home-operations/flate`, imported directly by
`downflate` and `konflate`); keep it that way unless a real cross-repo
consumer shows up. Keep `main.go` to wiring: parse config, build the
logger, construct `upstream`/`sign`/`registry` dependencies, run, translate
the top-level error into an exit code. Business logic belongs in
`internal/<package>`, not in `main`.

## Errors

Wrap with `fmt.Errorf("<component>: %w", err)` so a caller gets context
without losing the original error for `errors.Is`/`errors.As`/`errors.AsType`
(see the `"config: %w"` prefix throughout `internal/config`). Sentinel
errors (`upstream.ErrNotFound`, `ErrChartUnknown`, `ErrVersionUnknown`,
`ErrBlobUnknown`, `upstream.ErrHostNotAllowed`, ...) get classified once, at
the HTTP boundary, by the `errors.Is` switch in `writeResolveError`; that's
the pattern to extend, not a per-handler duplicate of it. Never discard an
error silently: `_ = someCall()` is only for genuinely fire-and-forget calls
(e.g. best-effort metrics), and say why in a comment when that's not
obvious.

## Context & shutdown

Every function that does I/O takes a `context.Context` as its first
parameter. `run()` derives the root context from `signal.NotifyContext`
(`SIGINT`, `SIGTERM`) and re-arms the default handler on a second signal so
a stuck drain can still be force-killed; see `cmd/ocharted/main.go`, and
don't duplicate that wiring elsewhere. Prefer `golang.org/x/sync/errgroup`
over raw `sync.WaitGroup` + channels when fanning out goroutines that can
fail: it propagates the first error and cancels the group's context for
you.

## Build, lint, test (via mise)

Mise is mandatory: it pins the exact Go, golangci-lint, and Helm versions
(`.mise/config.toml`), so running `go build`/`go test` outside `mise run`
risks a toolchain mismatch with CI. `mise tasks` lists everything available;
the common ones:

```bash
mise run build       # go build ./...
mise run fmt          # go fmt ./...
mise run vet          # go vet ./...
mise run test         # go test -race ./... -coverprofile cover.out
mise run test-e2e     # cosign-verify a real chart through the proxy (needs network)
mise run lint         # golangci-lint run
mise run lint-fix     # golangci-lint run --fix
mise run generate     # regenerate chart README + values.schema.json
mise run generate-check
mise run helm-lint
mise run helm-unittest
mise run helm-template
mise run flux-e2e      # flux-operator + FluxInstance e2e in a kind cluster
mise run helm-test     # helm install + `helm test` in a kind cluster
```

`lefthook` (`.lefthook.toml`, extending the shared `home-operations/.github`
config, plus this repo's own `generate`/format-exclusion hooks for the
generated chart README/schema) runs `gofmt -s -w` on staged `.go` files
pre-commit. CI additionally fails on `go mod tidy` producing a diff and on
stale generated files (`generate-check`); run both locally before pushing.
Lint rules live in `.golangci.yml`; read it instead of trusting a restated
list here, since the two can drift.

## Containers

`Dockerfile` in this repo is the fleet's reference implementation of the
static-binary pattern: `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -X
main.version=... -X main.commit=..."`, `upx --best --lzma` to shrink the
binary, built `FROM golang:${GO_VERSION}-alpine` and run `FROM
gcr.io/distroless/static:nonroot`. `setMemLimit()` in `main.go` sets
`GOMEMLIMIT` via `github.com/KimMachineGun/automemlimit` (90% of the cgroup
limit) so the GC reclaims before the container is OOM-killed; it's a silent
no-op outside a memory-limited cgroup, so it's safe to call unconditionally.
Metrics are `github.com/prometheus/client_golang` on `MetricsPort`, separate
from the main HTTP port.

## Security

`govulncheck ./...` (`go install golang.org/x/vuln/cmd/govulncheck@latest`)
isn't wired into CI anywhere in the fleet yet. Run it before cutting a
release regardless, and consider proposing it as a `mise run` task / CI job
here.
