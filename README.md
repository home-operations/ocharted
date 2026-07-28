# ocify

A stateless OCI registry proxy for classic Helm repositories. Point it at any
HTTP Helm repo and every chart in it becomes pullable as an OCI artifact — no
onboarding, no storage, no publish pipeline:

```
oci://<ocify-host>/<upstream-host[/path]>/<chart>
```

So `https://charts.jetstack.io`'s `cert-manager` chart is
`oci://ocify.example.com/charts.jetstack.io/cert-manager`, usable in a Flux
`OCIRepository` or with `helm install oci://...` even though upstream never
published OCI artifacts.

## How it works

ocify implements the read-only subset of the OCI distribution spec (`tags/list`,
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
comparison, so steady-state traffic through ocify is a handful of metadata
checks per interval — actual tarball transfers only happen when a version
really changes, and running HelmReleases are unaffected if ocify is down (the
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
  url: oci://ocify.example.com/charts.jetstack.io/cert-manager
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

If ocify runs with `OCIFY_AUTH`, add a `spec.secretRef` pointing at a
`kubernetes.io/dockerconfigjson` secret for the ocify host, exactly as for any
private registry.

### Helm CLI

```sh
helm pull oci://ocify.example.com/charts.jetstack.io/cert-manager --version v1.18.2
```

## Renovate

How updates keep flowing depends on where Renovate runs relative to ocify:

| Deployment                                    | Renovate config needed                      |
| --------------------------------------------- | ------------------------------------------- |
| ocify reachable by Renovate, anonymous        | none                                        |
| ocify reachable by Renovate, `OCIFY_AUTH` set | one `hostRule` with the credentials         |
| ocify internal-only, Renovate hosted          | one `packageRule` per upstream repo (below) |

When Renovate can reach ocify, it discovers versions through the standard
`tags/list` endpoint via its docker datasource — zero configuration, digest
pinning included. For an authenticated deployment:

```json
{
  "hostRules": [
    {
      "matchHost": "ocify.example.com",
      "username": "renovate",
      "password": "{{ secrets.OCIFY_PASSWORD }}"
    }
  ]
}
```

When ocify is cluster-internal and Renovate is not, redirect version lookups to
the real upstream — the proxy never invents version information, so the answers
are identical by construction:

```json
{
  "packageRules": [
    {
      "matchDatasources": ["docker"],
      "matchPackageNames": ["ocify.internal.example.com/charts.jetstack.io/**"],
      "overrideDatasource": "helm",
      "overridePackageName": "{{replace '.*/' '' packageName}}",
      "registryUrls": ["https://charts.jetstack.io"]
    }
  ]
}
```

One such rule covers every chart from that upstream repo; repeat per upstream.
(`registryUrls` cannot be templated, so a single fully generic rule is not
possible — if that matters to you, expose ocify with auth instead.)

## Configuration

Everything is environment variables; there is no config file.

| Variable                        | Default     | Description                                                                                        |
| ------------------------------- | ----------- | -------------------------------------------------------------------------------------------------- |
| `OCIFY_PORT`                    | `8080`      | Registry API (`/v2/`), `/healthz`, `/readyz`                                                       |
| `OCIFY_METRICS_ENABLED`         | `true`      | Serve Prometheus metrics on the metrics port                                                       |
| `OCIFY_METRICS_PORT`            | `8081`      | `/metrics` listener                                                                                |
| `OCIFY_AUTH`                    | _(empty)_   | `user:password` list (comma-separated); empty = anonymous                                          |
| `OCIFY_INDEX_TTL`               | `5m`        | Upstream `index.yaml` cache TTL — the freshness/politeness knob                                    |
| `OCIFY_CACHE_MAX_BYTES`         | `268435456` | In-memory derived-artifact cache bound                                                             |
| `OCIFY_MAX_INDEX_BYTES`         | `67108864`  | Upstream index size cap                                                                            |
| `OCIFY_MAX_CHART_BYTES`         | `33554432`  | Upstream chart tarball size cap                                                                    |
| `OCIFY_UPSTREAM_TIMEOUT`        | `30s`       | One upstream HTTP exchange                                                                         |
| `OCIFY_UPSTREAM_ALLOWLIST`      | _(empty)_   | Host globs permitted as upstreams (e.g. `*.github.io,charts.jetstack.io`); empty = any public host |
| `OCIFY_ALLOW_PRIVATE_UPSTREAMS` | `false`     | Permit upstreams resolving to private addresses                                                    |
| `OCIFY_PROVENANCE_ENABLED`      | `false`     | Attach upstream `.prov` files as the Helm provenance layer                                         |
| `OCIFY_REWRITE_DEPENDENCIES`    | `false`     | Rewrite HTTP(S) dependency repository URLs through this proxy (see below)                          |
| `OCIFY_EXTERNAL_HOST`           | _(empty)_   | Canonical client-facing hostname; required by dependency rewriting                                 |
| `OCIFY_RESOLVE_SCAN_LIMIT`      | `25`        | Max candidate versions a cold by-digest lookup derives                                             |
| `OCIFY_SIGNING_KEY_PATH`        | _(empty)_   | PEM-encoded Ed25519 private key; enables cosign signature serving                                  |
| `OCIFY_LOG_LEVEL`               | `info`      | `debug`, `info`, `warn`, `error`                                                                   |
| `OCIFY_LOG_FORMAT`              | `json`      | `json` or `text`                                                                                   |
| `OCIFY_DISABLE_REQUEST_LOGS`    | `false`     | Silence the per-request access log                                                                 |
| `OCIFY_SHUTDOWN_TIMEOUT`        | `15s`       | Graceful drain bound                                                                               |

### Exposure and hardening

The registry is read-only and serves only publicly published chart data, so
exposing it (with or without `OCIFY_AUTH`) is reasonable — that is what makes
the zero-config Renovate story work. Regardless of auth, set
`OCIFY_UPSTREAM_ALLOWLIST` when the proxy should not fetch arbitrary
attacker-named hosts: the allowlist (plus the always-on private-address dial
guard) is an SSRF boundary, not just an abuse guard.

Responses carry caching headers: by-digest manifests and blobs are
`immutable`, by-tag and listing responses expire with `OCIFY_INDEX_TTL`. A
plain ingress ignores these, but a caching layer in front — e.g. a Cloudflare
cache rule covering the ocify host — will serve the heavy immutable endpoints
from its edge with no further configuration.

## Dependency rewriting

A chart's `Chart.yaml` can declare dependencies with hardcoded HTTP repository
URLs; by default, `helm dependency update` fetches those straight from the
original repos, bypassing the proxy. With `OCIFY_REWRITE_DEPENDENCIES=true`
(and `OCIFY_EXTERNAL_HOST` set to the proxy's canonical name), ocify rewrites
those URLs inside each served chart so dependency resolution also flows
through it:

```yaml
# upstream Chart.yaml            # served Chart.yaml
dependencies:                    dependencies:
  - name: redis                    - name: redis
    repository: https://charts.bitnami.com/bitnami
                                     repository: oci://ocify.example.com/charts.bitnami.com/bitnami
```

`file://` paths, `@alias` references, and already-OCI URLs are left alone, and
charts with nothing to rewrite pass through byte-for-byte. Rewriting stays a
pure function — the same inputs produce identical bytes on every replica —
because the rewrite target is the static `OCIFY_EXTERNAL_HOST`, never the
request hostname.

**Trade-offs, deliberately explicit:**

- Rewritten charts no longer hash to the digest upstream's index publishes.
  "You got exactly upstream's bytes" stops holding — which is why this is
  off by default and why cosign signing (below) is worth enabling alongside
  it, as ocify's signature becomes the artifact's only integrity attestation.
- It is mutually exclusive with `OCIFY_PROVENANCE_ENABLED`: the upstream
  `.prov` signs the original tarball, and serving it beside rewritten bytes
  would present a signature that verifies against nothing. ocify refuses the
  combination at startup.
- The upstream download is still digest-verified _before_ rewriting, so
  integrity against upstream holds internally; only the served digest
  diverges.
- Cold by-digest lookups lose the index-digest fast path for rewritten charts
  and fall back to the bounded scan — correct, marginally slower.

## Cosign signing

With a signing key configured, ocify serves a cosign signature for every
manifest (the `sha256-<digest>.sig` tag convention), derived on demand like
everything else. Flux can then enforce that only charts served by _your_ proxy
enter the cluster:

```yaml
spec:
  url: oci://ocify.example.com/charts.jetstack.io/cert-manager
  verify:
    provider: cosign
    secretRef:
      name: ocify-pub # data key `cosign.pub` with the public key PEM
```

Generate the key pair (Ed25519 only — its signatures are deterministic, which
the stateless multi-replica design requires; default ECDSA's random nonce
would break by-digest blob serving across replicas):

```sh
openssl genpkey -algorithm ed25519 -out signing.key
openssl pkey -in signing.key -pubout -out cosign.pub
```

Mount the private key and set `OCIFY_SIGNING_KEY_PATH` (the chart's
`signing.existingSecret` values wire this up). Verify from anywhere with:

```sh
cosign verify --key cosign.pub --insecure-ignore-tlog=true \
  ocify.example.com/charts.jetstack.io/cert-manager:v1.18.2
```

(`--insecure-ignore-tlog` because signatures are derived per request, not
uploaded to a transparency log.)

**What the signature attests:** that the bytes passed through your ocify
instance unmodified — provenance of _path_, not of _origin_. If an upstream
repo is compromised, ocify will faithfully sign the compromised chart. For
upstream-author attestation, enable `OCIFY_PROVENANCE_ENABLED`, which passes
the maintainer's PGP `.prov` file through as the standard Helm provenance
layer when upstream publishes one. The two are complementary: cosign is your
admission gate, provenance is upstream's signature.

## Relationship to charts-mirror

[charts-mirror](https://github.com/home-operations/charts-mirror) republishes a
curated set of charts to GHCR: every chart is a PR, artifacts survive upstream
deletion, and releases are cosign-signed. ocify is the complementary shape: no
curation and no onboarding, but also no rehosting — it is a translator, not an
archive, and inherits upstream availability and mutability. Use charts-mirror
for charts that need durability and signing; use ocify for the long tail.

## Prior art

ocify was inspired by
[helm-charts-oci-proxy](https://github.com/container-registry/helm-charts-oci-proxy),
which pioneered the idea of transparently serving Chart Repository styled Helm
repos as OCI artifacts and proved the URL scheme this project uses. ocify is a
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
Its dependency URL rewriting also exists here, but more constrained: ocify's
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
