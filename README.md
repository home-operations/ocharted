# ocharted

> To OCI, your classic Helm repos are _uncharted_ territory. Point this proxy
> at them and consider them **ocharted**.

A stateless OCI registry proxy for classic Helm repositories. Point it at any
HTTP Helm repo and every chart in it becomes pullable as an OCI artifact — no
onboarding, no storage, no publish pipeline:

```
oci://<ocharted-host>/<upstream-host[/path]>/<chart>
```

So `https://charts.jetstack.io`'s `cert-manager` chart is
`oci://ocharted.example.com/charts.jetstack.io/cert-manager`, usable in a Flux
`OCIRepository` or with `helm install oci://...` even though upstream never
published OCI artifacts.

## How it works

ocharted implements the read-only subset of the OCI distribution spec (`tags/list`,
`manifests`, `blobs`) and derives every response on demand from the upstream
repository:

- **Tags** are the chart versions in the upstream `index.yaml` (SemVer `+`
  build metadata maps to `_`, Helm's own OCI convention).
- **The chart layer** is the upstream tarball byte-for-byte, verified against
  the digest the index publishes.
- **The config blob** is the chart's `Chart.yaml` rendered as canonical JSON,
  sourced from inside the tarball so repo-side index regeneration can never
  shift digests.
- **Manifests** are assembled from the above with no clocks and no randomness.

Because every artifact is a pure function of upstream bytes, any replica (or a
freshly restarted one) re-derives byte-identical answers. There is no volume,
no database, and no coordination between replicas — the in-memory cache is a
latency optimization, never a source of truth. A cold request costs one or two
upstream fetches; correctness never depends on cache state.

Flux's source-controller keeps its own artifact store and reconciles by digest
comparison, so steady-state traffic through ocharted is a handful of metadata
checks per interval — actual tarball transfers only happen when a version
really changes, and running HelmReleases are unaffected if ocharted is down (the
proxy sits in the update path, not the runtime path).

## Usage

### Flux

```yaml
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: cert-manager
spec:
  interval: 1h
  layerSelector:
    mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
    operation: copy
  ref:
    tag: v1.18.2
  url: oci://ocharted.example.com/charts.jetstack.io/cert-manager
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: cert-manager
spec:
  interval: 1h
  chartRef:
    kind: OCIRepository
    name: cert-manager
```

If ocharted runs with `OCHARTED_AUTH`, add a `spec.secretRef` pointing at a
`kubernetes.io/dockerconfigjson` secret for the ocharted host, exactly as for any
private registry.

### Helm CLI

```sh
helm pull oci://ocharted.example.com/charts.jetstack.io/cert-manager --version v1.18.2
```

## Renovate

How updates keep flowing depends on where Renovate runs relative to ocharted:

| Deployment                                          | Renovate config needed                      |
| --------------------------------------------------- | ------------------------------------------- |
| ocharted reachable by Renovate, anonymous           | none                                        |
| ocharted reachable by Renovate, `OCHARTED_AUTH` set | one `hostRule` with the credentials         |
| ocharted internal-only, Renovate hosted             | one `packageRule` per upstream repo (below) |

When Renovate can reach ocharted, it discovers versions through the standard
`tags/list` endpoint via its docker datasource — zero configuration, digest
pinning included. For an authenticated deployment:

```json
{
  "hostRules": [
    {
      "matchHost": "ocharted.example.com",
      "username": "renovate",
      "password": "{{ secrets.OCHARTED_PASSWORD }}"
    }
  ]
}
```

When ocharted is cluster-internal and Renovate is not, redirect version lookups to
the real upstream — the proxy never invents version information, so the answers
are identical by construction:

```json
{
  "packageRules": [
    {
      "matchDatasources": ["docker"],
      "matchPackageNames": ["ocharted.internal.example.com/charts.jetstack.io/**"],
      "overrideDatasource": "helm",
      "overridePackageName": "{{replace '.*/' '' packageName}}",
      "registryUrls": ["https://charts.jetstack.io"]
    }
  ]
}
```

One such rule covers every chart from that upstream repo; repeat per upstream.
(`registryUrls` cannot be templated, so a single fully generic rule is not
possible — if that matters to you, expose ocharted with auth instead.)

## Configuration

Everything is environment variables; there is no config file.

| Variable                           | Default     | Description                                                                                        |
| ---------------------------------- | ----------- | -------------------------------------------------------------------------------------------------- |
| `OCHARTED_PORT`                    | `8080`      | Registry API (`/v2/`), `/healthz`, `/readyz`                                                       |
| `OCHARTED_METRICS_ENABLED`         | `true`      | Serve Prometheus metrics on the metrics port                                                       |
| `OCHARTED_METRICS_PORT`            | `8081`      | `/metrics` listener                                                                                |
| `OCHARTED_AUTH`                    | _(empty)_   | `user:password` list (comma-separated); empty = anonymous                                          |
| `OCHARTED_AUTH_BYPASS_NETWORKS`    | _(empty)_   | CIDRs whose traffic skips basic auth (see Exposure and hardening); requires `OCHARTED_AUTH`        |
| `OCHARTED_INDEX_TTL`               | `5m`        | Upstream `index.yaml` cache TTL — the freshness/politeness knob                                    |
| `OCHARTED_INDEX_STALE_TTL`         | `24h`       | Stale-if-error bound: serve the cached index this long when upstream is down; `0` disables         |
| `OCHARTED_CACHE_MAX_BYTES`         | `268435456` | In-memory derived-artifact cache bound                                                             |
| `OCHARTED_MAX_INDEX_BYTES`         | `67108864`  | Upstream index size cap                                                                            |
| `OCHARTED_MAX_CHART_BYTES`         | `33554432`  | Upstream chart tarball size cap                                                                    |
| `OCHARTED_UPSTREAM_TIMEOUT`        | `30s`       | One upstream HTTP exchange                                                                         |
| `OCHARTED_UPSTREAM_ALLOWLIST`      | _(empty)_   | Host globs permitted as upstreams (e.g. `*.github.io,charts.jetstack.io`); empty = any public host |
| `OCHARTED_ALLOW_PRIVATE_UPSTREAMS` | `false`     | Permit upstreams resolving to private addresses                                                    |
| `OCHARTED_PROVENANCE_ENABLED`      | `false`     | Attach upstream `.prov` files as the Helm provenance layer                                         |
| `OCHARTED_REWRITE_DEPENDENCIES`    | `false`     | Rewrite HTTP(S) dependency repository URLs through this proxy (see below)                          |
| `OCHARTED_EXTERNAL_HOST`           | _(empty)_   | Canonical client-facing hostname; required by dependency rewriting                                 |
| `OCHARTED_RESOLVE_SCAN_LIMIT`      | `25`        | Max candidate versions a cold by-digest lookup derives                                             |
| `OCHARTED_SIGNING_KEY_PATH`        | _(empty)_   | PEM-encoded Ed25519 private key; enables cosign signature serving                                  |
| `OCHARTED_LOG_LEVEL`               | `info`      | `debug`, `info`, `warn`, `error`                                                                   |
| `OCHARTED_LOG_FORMAT`              | `json`      | `json` or `text`                                                                                   |
| `OCHARTED_DISABLE_REQUEST_LOGS`    | `false`     | Silence the per-request access log                                                                 |
| `OCHARTED_SHUTDOWN_TIMEOUT`        | `15s`       | Graceful drain bound                                                                               |

### Exposure and hardening

The registry is read-only and serves only publicly published chart data, so
exposing it (with or without `OCHARTED_AUTH`) is reasonable — that is what makes
the zero-config Renovate story work. Regardless of auth, set
`OCHARTED_UPSTREAM_ALLOWLIST` when the proxy should not fetch arbitrary
attacker-named hosts: the allowlist (plus the always-on private-address dial
guard) is an SSRF boundary, not just an abuse guard.

With auth enabled, `OCHARTED_AUTH_BYPASS_NETWORKS` lets cluster-local clients
skip it while external clients still authenticate — so the `OCIRepository` URL
in git stays the public hostname (which hosted Renovate resolves with its
`hostRule` credentials) while in-cluster Flux, hairpinning through the same
gateway, needs no `secretRef`. The rule: a request is anonymous **iff its
entire connection chain — the TCP peer plus every `X-Forwarded-For` hop —
lies within the listed networks** (e.g. pod + service + LAN CIDRs). Any hop
outside means an external party was in the path, so auth applies; a forged
`X-Forwarded-For` doesn't help an attacker, because the gateway appends their
real address to the chain. The one requirement: every hop you route external
traffic through (gateway, tunnel) must append to `X-Forwarded-For` truthfully
— Envoy and Cloudflare both do. Bypassed requests are counted in
`ocharted_auth_bypassed_total`.

Responses carry caching headers: by-digest manifests and blobs are
`immutable`, by-tag and listing responses expire with `OCHARTED_INDEX_TTL`. A
plain ingress ignores these, but a caching layer in front — e.g. a Cloudflare
cache rule covering the ocharted host — will serve the heavy immutable endpoints
from its edge with no further configuration.

## Resiliency

ocharted sits in Flux's _update_ path, never the runtime path: if it (or an
upstream) is unreachable, source-controller keeps serving its last stored
artifact and running HelmReleases are untouched — only version freshness is
delayed. With that in mind, resiliency in practice:

- **Run 2+ replicas with a PodDisruptionBudget.** Statelessness makes this
  free — no coordination, any replica answers any request — so rollouts, node
  drains, and crashes never present downtime to clients. This is the single
  biggest lever; the chart's `replicaCount` and `podDisruptionBudget` values
  cover it.
- **Upstream outages are absorbed by stale-if-error.** When re-fetching an
  expired index fails with a non-authoritative error (network fault, 5xx),
  ocharted serves the cached copy for up to `OCHARTED_INDEX_STALE_TTL` (default
  24h), logging a warning and counting
  `ocharted_upstream_stale_index_served_total`. Authoritative answers — a 404
  (repo gone) or an allowlist rejection — are never masked. Chart downloads
  themselves have no stale fallback (there is nothing cached to serve
  correctly), but Flux only downloads on version changes.
- **No Flux features need enabling.** Failed reconciles retry automatically
  with exponential backoff (source-controller defaults: 750ms doubling up to
  a 15m cap, tunable via `--min-retry-delay`/`--max-retry-delay` if the
  post-outage catch-up lag ever matters to you). The one per-object knob worth
  knowing: `OCIRepository spec.timeout` (default 60s) covers the whole
  registry exchange, and a cold ocharted cache resolving a chart from a very
  large upstream index does its index fetch and tarball download within that
  window — raise it per-repo if you ever see timeout-flavored `FetchFailed`.

## Dependency rewriting

A chart's `Chart.yaml` can declare dependencies with hardcoded HTTP repository
URLs; by default, `helm dependency update` fetches those straight from the
original repos, bypassing the proxy. With `OCHARTED_REWRITE_DEPENDENCIES=true`
(and `OCHARTED_EXTERNAL_HOST` set to the proxy's canonical name), ocharted rewrites
those URLs inside each served chart so dependency resolution also flows
through it:

```yaml
# upstream Chart.yaml            # served Chart.yaml
dependencies:                    dependencies:
  - name: redis                    - name: redis
    repository: https://charts.bitnami.com/bitnami
                                     repository: oci://ocharted.example.com/charts.bitnami.com/bitnami
```

`file://` paths, `@alias` references, and already-OCI URLs are left alone, and
charts with nothing to rewrite pass through byte-for-byte. Rewriting stays a
pure function — the same inputs produce identical bytes on every replica —
because the rewrite target is the static `OCHARTED_EXTERNAL_HOST`, never the
request hostname.

**Trade-offs, deliberately explicit:**

- Rewritten charts no longer hash to the digest upstream's index publishes.
  "You got exactly upstream's bytes" stops holding — which is why this is
  off by default and why cosign signing (below) is worth enabling alongside
  it, as ocharted's signature becomes the artifact's only integrity attestation.
- It is mutually exclusive with `OCHARTED_PROVENANCE_ENABLED`: the upstream
  `.prov` signs the original tarball, and serving it beside rewritten bytes
  would present a signature that verifies against nothing. ocharted refuses the
  combination at startup.
- The upstream download is still digest-verified _before_ rewriting, so
  integrity against upstream holds internally; only the served digest
  diverges.
- Cold by-digest lookups lose the index-digest fast path for rewritten charts
  and fall back to the bounded scan — correct, marginally slower.

## Cosign signing

With a signing key configured, ocharted serves a cosign signature for every
manifest (the `sha256-<digest>.sig` tag convention), derived on demand like
everything else. Flux can then enforce that only charts served by _your_ proxy
enter the cluster:

```yaml
spec:
  url: oci://ocharted.example.com/charts.jetstack.io/cert-manager
  verify:
    provider: cosign
    secretRef:
      name: ocharted-pub # data key `cosign.pub` with the public key PEM
```

Generate the key pair (Ed25519 only — its signatures are deterministic, which
the stateless multi-replica design requires; default ECDSA's random nonce
would break by-digest blob serving across replicas):

```sh
openssl genpkey -algorithm ed25519 -out signing.key
openssl pkey -in signing.key -pubout -out cosign.pub
```

Mount the private key and set `OCHARTED_SIGNING_KEY_PATH` (the chart's
`signing.existingSecret` values wire this up). Verify from anywhere with:

```sh
cosign verify --key cosign.pub --insecure-ignore-tlog=true \
  ocharted.example.com/charts.jetstack.io/cert-manager:v1.18.2
```

(`--insecure-ignore-tlog` because signatures are derived per request, not
uploaded to a transparency log.)

**What the signature attests:** that the bytes passed through your ocharted
instance unmodified — provenance of _path_, not of _origin_. If an upstream
repo is compromised, ocharted will faithfully sign the compromised chart. For
upstream-author attestation, enable `OCHARTED_PROVENANCE_ENABLED`, which passes
the maintainer's PGP `.prov` file through as the standard Helm provenance
layer when upstream publishes one. The two are complementary: cosign is your
admission gate, provenance is upstream's signature.

## Relationship to charts-mirror

[charts-mirror](https://github.com/home-operations/charts-mirror) republishes a
curated set of charts to GHCR: every chart is a PR, artifacts survive upstream
deletion, and releases are cosign-signed. ocharted is the complementary shape: no
curation and no onboarding, but also no rehosting — it is a translator, not an
archive, and inherits upstream availability and mutability. Use charts-mirror
for charts that need durability and signing; use ocharted for the long tail.

## Prior art

ocharted was inspired by
[helm-charts-oci-proxy](https://github.com/container-registry/helm-charts-oci-proxy),
which pioneered the idea of transparently serving Chart Repository styled Helm
repos as OCI artifacts and proved the URL scheme this project uses. ocharted is a
from-scratch take on the same idea, built for GitOps/homelab deployments.
What it adds:

- **Deterministic derivation as a hard invariant** — manifests, config blobs,
  and digests are byte-identical across replicas and restarts (no clocks, no
  randomness, config blob sourced from inside the tarball). That is what makes
  digest pinning and coordination-free multi-replica HA safe.
- **Cosign signing** of served artifacts (deterministic Ed25519), so Flux
  `verify.provider: cosign` can enforce that charts only enter the cluster
  through your proxy.
- **Helm `.prov` provenance passthrough**, carrying the upstream maintainer's
  PGP signature through as the standard provenance layer.
- **Basic auth** for the whole `/v2/` API — the standard registry credential
  flow Flux (`dockerconfigjson`) and Renovate (`hostRules`) already speak.
- **An SSRF boundary**: upstream host allowlist plus an always-on
  private-address dial guard that rejects at connect time, post-DNS.
- **Upstream digest verification** — tarballs are checked against the digest
  the index publishes before anything is served.
- **Operational plumbing**: Prometheus metrics, liveness/readiness probes,
  structured logs, graceful drain, response size caps, spec-compliant
  `tags/list` pagination, and `Cache-Control` headers that make by-digest
  responses edge-cacheable.
- **A documented Renovate story** for every deployment topology.

In the other direction, helm-charts-oci-proxy offers a free hosted instance.
Its dependency URL rewriting also exists here, but more constrained: ocharted's
version is a deployment-level opt-in requiring a static external hostname (no
per-request toggle, no request-Host fallback), so rewritten bytes stay
deterministic across replicas — and it is off by default, because serving
upstream's bytes verbatim is the property everything else is built on.

## Non-goals

- **Git-sourced charts** — packaging from a git checkout is not
  byte-deterministic, which would break the stateless digest guarantees.
  charts-mirror covers this case.
- **Rehosting** — if upstream deletes a version, it is gone here too.
- **Push support** — the registry is strictly read-only.
