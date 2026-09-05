# ocharted

![Version](https://img.shields.io/static/v1?label=Version&message=0.1.6&color=informational&style=flat-square) <!-- x-release-please-version -->
![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)
![AppVersion](https://img.shields.io/static/v1?label=AppVersion&message=0.1.6&color=informational&style=flat-square) <!-- x-release-please-version -->

> To OCI, your classic Helm repos are *uncharted* territory. Point this proxy
> at them and consider them **ocharted**.

A stateless **OCI registry proxy** for classic Helm repositories: any chart in
any HTTP Helm repo becomes pullable as
`oci://<ocharted-host>/<upstream-host[/path]>/<chart>` — usable in a Flux
`OCIRepository` or with `helm install oci://…` even though upstream never
published OCI artifacts. Everything is derived on demand from upstream: no
volume, no database, and replicas need no coordination.

**Homepage:** <https://github.com/home-operations/ocharted>

## Installing

```bash
helm install ocharted oci://ghcr.io/home-operations/charts/ocharted --version <version>
```

## Configuration

Every setting maps to an `OCHARTED_*` environment variable via the `config.*`
values; the only secret is the optional basic-auth user list (`auth.users`, or
`auth.existingSecret` for a SOPS/sealed Secret). Two settings deserve
deliberate choices:

- **`config.upstreamAllowlist`** restricts which upstream hosts the proxy will
  fetch from. It is an SSRF boundary, not just an abuse guard — set it on any
  deployment reachable by untrusted clients.
- **`auth.users`** turns on HTTP basic auth for the whole `/v2/` API, which is
  what Flux consumes via a `dockerconfigjson` `secretRef` and Renovate via a
  `hostRule`.

See the [project README](https://github.com/home-operations/ocharted) for the URL scheme, the Renovate
integration options, and the exposure/hardening guidance.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| home-operations | <contact@home-operations.com> |  |

## Source Code

* <https://github.com/home-operations/ocharted>

## Requirements

Kubernetes: `>=1.25.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling (templated). |
| auth.bypassNetworks | list | `[]` | CIDRs whose traffic skips basic auth (OCHARTED_AUTH_BYPASS_NETWORKS): a request is anonymous iff its entire connection chain — TCP peer plus every X-Forwarded-For hop — lies within these networks. Typical value: pod + service + LAN CIDRs, so in-cluster Flux/Renovate pull anonymously through the same public hostname external clients must authenticate to. Every listed hop (gateway, tunnel) must append to X-Forwarded-For truthfully (Envoy and Cloudflare do). Requires auth to be enabled. |
| auth.existingSecret | string | `""` | Use this existing Secret for OCHARTED_AUTH instead of rendering one. |
| auth.existingSecretKey | string | `"auth"` | Key in `existingSecret` holding the `user:password,…` list. |
| auth.users | string | `""` | Basic-auth users as a `user:password,user2:password2` list (OCHARTED_AUTH). If set, it is rendered into a chart-managed Secret. Leave empty and use `existingSecret` to supply it from your own (e.g. SOPS/sealed) Secret. |
| config.allowPrivateUpstreams | bool | `false` | Permit upstreams that resolve to private addresses (OCHARTED_ALLOW_PRIVATE_UPSTREAMS), e.g. an in-cluster ChartMuseum. Leave off otherwise. |
| config.cacheMaxBytes | int | `268435456` | In-memory derived-artifact cache bound in bytes (OCHARTED_CACHE_MAX_BYTES). Purely a latency/upstream-traffic optimization; size resources.limits.memory above it with headroom. |
| config.disableRequestLogs | bool | `false` | Silence the per-request access log (OCHARTED_DISABLE_REQUEST_LOGS). |
| config.externalHost | string | `""` | Canonical client-facing hostname written into rewritten dependency URLs (OCHARTED_EXTERNAL_HOST). Host only — no scheme, no path. Static so rewritten bytes are identical no matter which hostname a client used. |
| config.indexStaleTTL | string | `"24h"` | Stale-if-error bound (OCHARTED_INDEX_STALE_TTL): when re-fetching an expired index fails (network fault, upstream 5xx), the cached copy is served up to this age, so upstream outages delay freshness instead of failing Flux reconciles. Authoritative answers (404, allowlist rejection) are never masked. "0" disables. |
| config.indexTTL | string | `"5m"` | Upstream index.yaml cache TTL (OCHARTED_INDEX_TTL) — how fast new chart versions appear, and how often upstreams are re-fetched. |
| config.logFormat | string | `"json"` | Log format (OCHARTED_LOG_FORMAT): json or text. |
| config.logLevel | string | `"info"` | Log level (OCHARTED_LOG_LEVEL): debug, info, warn, or error. |
| config.maxChartBytes | int | `33554432` | Upstream chart tarball size cap in bytes (OCHARTED_MAX_CHART_BYTES). |
| config.maxIndexBytes | int | `67108864` | Upstream index.yaml size cap in bytes (OCHARTED_MAX_INDEX_BYTES); generous because real indexes get big (Bitnami's is tens of MB). |
| config.metricsEnabled | bool | `true` | Expose Prometheus metrics at /metrics on metricsPort (OCHARTED_METRICS_ENABLED). Disabling removes the metrics listener, container port, Service port, and ServiceMonitor entirely; health probes are unaffected (they target the http port). |
| config.metricsPort | int | `8081` | Metrics listen port (OCHARTED_METRICS_PORT), kept off the public registry port. |
| config.port | int | `8080` | Registry API port (OCHARTED_PORT): /v2/, /healthz, /readyz; also the container/Service http port. |
| config.provenanceEnabled | bool | `false` | Fetch each chart's .prov file and attach it as the Helm provenance layer when upstream publishes one (OCHARTED_PROVENANCE_ENABLED). Adds one upstream request per chart build. |
| config.resolveScanLimit | int | `25` | Max candidate versions a cold by-digest lookup derives before giving up (OCHARTED_RESOLVE_SCAN_LIMIT). |
| config.rewriteDependencies | bool | `false` | Rewrite HTTP(S) dependency repository URLs inside served charts to point back through this proxy (OCHARTED_REWRITE_DEPENDENCIES), so `helm dependency update` also resolves through it. Requires `externalHost`; mutually exclusive with `provenanceEnabled`. Trade-off: rewritten charts no longer hash to the digest upstream's index publishes. |
| config.shutdownTimeout | string | `"15s"` | Graceful drain bound on SIGTERM (OCHARTED_SHUTDOWN_TIMEOUT). |
| config.upstreamAllowlist | list | `[]` | Upstream repo hosts that may be proxied, as glob patterns (OCHARTED_UPSTREAM_ALLOWLIST), e.g. ["*.github.io", "charts.jetstack.io"]. Empty allows any public host. This is an SSRF boundary as much as an abuse guard — set it on any deployment reachable by untrusted clients. |
| config.upstreamTimeout | string | `"30s"` | One upstream HTTP exchange deadline (OCHARTED_UPSTREAM_TIMEOUT). |
| deploymentAnnotations | object | `{}` | Annotations added to the Deployment metadata, e.g. `reloader.stakater.com/auto: "true"` to roll the pod when a referenced Secret changes (recommended when using `auth.existingSecret`). |
| env | object | `{}` | Extra environment variables as a map (templated). |
| envFrom | list | `[]` | Sources of environment variables (templated), e.g. `- secretRef: { name: ocharted-auth }`. |
| extraEnv | list | `[]` | Extra environment variables as a raw list (templated), e.g. valueFrom a Secret key. |
| fullnameOverride | string | `""` | Override the generated name used for every resource's `metadata.name` (the chart "fullname"). |
| httpRoute.additionalRules | list | `[]` | Custom rules prepended before the default rule (templated). |
| httpRoute.annotations | object | `{}` | HTTPRoute annotations. |
| httpRoute.apiVersion | string | `""` | HTTPRoute apiVersion; empty defaults to gateway.networking.k8s.io/v1. |
| httpRoute.enabled | bool | `false` | Expose the registry via a Gateway API HTTPRoute. |
| httpRoute.filters | list | `[]` | Filters applied to the default rule. |
| httpRoute.hostnames | list | `[]` | Hostnames matched against the Host header (templated). The hostname clients use is also what goes into OCIRepository URLs — and into `config.externalHost` when dependency rewriting is enabled. |
| httpRoute.httpsRedirect | bool | `false` | Redirect HTTP→HTTPS (301) instead of routing to the backend (needs a Gateway with HTTP+HTTPS listeners); matches/filters are ignored. |
| httpRoute.kind | string | `""` | HTTPRoute kind; empty defaults to HTTPRoute. |
| httpRoute.labels | object | `{}` | HTTPRoute labels. |
| httpRoute.matches | list | `[{"path":{"type":"PathPrefix","value":"/"}}]` | Match conditions for the default rule. |
| httpRoute.parentRefs | list | `[]` | Gateways (and listeners) this route attaches to. |
| image.digest | string | `""` | Pin the image by digest (sha256:…); set by the release pipeline. When set, it overrides the tag. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/home-operations/ocharted"` | Image repository. |
| image.tag | string | `""` | Overrides the image tag; defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets for private registries. |
| initContainers | list | `[]` | Additional init containers (templated). |
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"http"},"periodSeconds":20}` | Liveness probe. Targets /healthz on the main http port. |
| monitoring.serviceMonitor.annotations | object | `{}` | ServiceMonitor annotations. |
| monitoring.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor (requires its CRDs and `config.metricsEnabled`). |
| monitoring.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| monitoring.serviceMonitor.labels | object | `{}` | ServiceMonitor labels. |
| monitoring.serviceMonitor.metricRelabelings | list | `[]` | Prometheus metric relabelings. |
| monitoring.serviceMonitor.path | string | `"/metrics"` | Metrics path. |
| monitoring.serviceMonitor.relabelings | list | `[]` | Prometheus relabelings. |
| monitoring.serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| nameOverride | string | `""` | Override the chart name used in the `app.kubernetes.io/name` label. |
| nodeSelector | object | `{}` | Node selector for pod scheduling (templated). |
| podAnnotations | object | `{}` | Annotations added to the pod. A `checksum/secret` annotation is added automatically when the chart manages the auth Secret, so credential edits roll the pod. |
| podDisruptionBudget.enabled | bool | `false` | Create a PodDisruptionBudget (meaningful only with replicaCount > 1). |
| podDisruptionBudget.maxUnavailable | string | `""` | maxUnavailable. |
| podDisruptionBudget.minAvailable | int | `1` | minAvailable (mutually exclusive with maxUnavailable). |
| podLabels | object | `{}` | Labels added to the pod. |
| podSecurityContext | object | `{"fsGroup":65532,"runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level securityContext. ocharted runs as the image's nonroot user (uid 65532); no host or elevated access is needed. |
| priorityClassName | string | `""` | PriorityClass for the pod (templated); empty uses the cluster default. |
| readinessProbe | object | `{"httpGet":{"path":"/readyz","port":"http"},"periodSeconds":10}` | Readiness probe. Targets /readyz on the main http port. |
| replicaCount | int | `1` | Number of replicas. ocharted is stateless and replicas re-derive identical answers, so >1 is just more capacity/HA — no coordination, no sticky sessions. |
| resources | object | `{}` | Container resource requests/limits. If you set a memory limit, keep it comfortably above `config.cacheMaxBytes`. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"privileged":false,"readOnlyRootFilesystem":true}` | Container securityContext. Drops all capabilities, read-only root filesystem, no privilege escalation. |
| service.annotations | object | `{}` | Service annotations. |
| service.externalTrafficPolicy | string | `""` | externalTrafficPolicy (only applied when type is not ClusterIP). |
| service.port | int | `8080` | Registry (http) service port. |
| service.type | string | `"ClusterIP"` | Service type. |
| serviceAccount.annotations | object | `{}` | ServiceAccount annotations. |
| serviceAccount.automount | bool | `false` | Mount the API token. ocharted never calls the Kubernetes API, so this is off by default. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name; empty uses the chart fullname. |
| signing.existingSecret | string | `""` | Existing Secret holding a PEM-encoded Ed25519 private key (PKCS#8, e.g. `openssl genpkey -algorithm ed25519`). Ed25519 only — its deterministic signatures are what keep replicas stateless. Empty disables signing. The chart never manages this Secret: private key material belongs in your own (e.g. SOPS/sealed) Secret. NOTE: ocharted reads the key once at startup, so rotating it requires a pod roll — e.g. set `reloader.stakater.com/auto` via `deploymentAnnotations`. |
| signing.existingSecretKey | string | `"signing.key"` | Key in `existingSecret` holding the private key PEM. |
| startupProbe | object | `{"failureThreshold":30,"httpGet":{"path":"/healthz","port":"http"},"periodSeconds":2}` | Startup probe (GET /healthz on the main http port, so it works regardless of the metrics toggle). |
| strategy | object | `{"rollingUpdate":{"maxSurge":1,"maxUnavailable":0},"type":"RollingUpdate"}` | Deployment update strategy. RollingUpdate by default; ocharted is stateless so a surge-then-drain rollout is safe. |
| terminationGracePeriodSeconds | int | `30` | Grace period for a clean shutdown (drain). |
| tests.image.pullPolicy | string | `"IfNotPresent"` | `helm test` image pull policy. |
| tests.image.repository | string | `"mirror.gcr.io/curlimages/curl"` | `helm test` connection-pod image; a gcr-mirrored curl, so the test never pulls from Docker Hub. |
| tests.image.tag | string | `"8.22.0@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777"` | `helm test` image, pinned as `tag@sha256:digest` so Renovate bumps the tag and its digest together. |
| tolerations | list | `[]` | Tolerations for pod scheduling (templated). |
| topologySpreadConstraints | list | `[]` | Topology spread constraints (templated). |
| volumeMounts | list | `[]` | Additional volume mounts on the container (templated). |
| volumes | list | `[]` | Additional volumes on the pod (templated). |

---

_This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs) from `Chart.yaml` and `values.yaml`. Edit those (or `README.md.gotmpl`) and run `mise run generate`._
