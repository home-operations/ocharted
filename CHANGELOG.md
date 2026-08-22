# Changelog

## [0.1.5](https://github.com/home-operations/ocharted/compare/0.1.4...0.1.5) (2026-08-22)


### Miscellaneous Chores

* **github-action:** update action docker/setup-buildx-action (v4.2.0 → v4.3.0) ([#41](https://github.com/home-operations/ocharted/issues/41)) ([f61414a](https://github.com/home-operations/ocharted/commit/f61414a20b5e253206ee62beb24cf37d18f26c26))
* **mise:** update tool oxfmt (0.63.0 → 0.64.0) ([#39](https://github.com/home-operations/ocharted/issues/39)) ([db363fa](https://github.com/home-operations/ocharted/commit/db363fac9baa97e46fb3498f8ee565e1b549e62a))
* **mise:** update tool yq (4.53.3 → 4.53.4) ([#40](https://github.com/home-operations/ocharted/issues/40)) ([abe4a58](https://github.com/home-operations/ocharted/commit/abe4a586eff2688ebb636779953010f67b1be936))

## [0.1.4](https://github.com/home-operations/ocharted/compare/0.1.3...0.1.4) (2026-08-21)


### Bug Fixes

* **ci:** fail the merge gate on cancelled jobs, and key the lint cache on the toolchain ([#16](https://github.com/home-operations/ocharted/issues/16)) ([db74760](https://github.com/home-operations/ocharted/commit/db74760cfebd2e9c9ce7919e631a088f0b30462d))
* **go:** update to go 1.27.0 ([#37](https://github.com/home-operations/ocharted/issues/37)) ([2443c56](https://github.com/home-operations/ocharted/commit/2443c563da3a0c5c3b4fc2d893d1f933de11c55d))
* **registry:** answer 404 for an upstream digest mismatch ([#38](https://github.com/home-operations/ocharted/issues/38)) ([c276164](https://github.com/home-operations/ocharted/commit/c276164fa02487577521f4b312e83269fa44f0be))


### Documentation

* add AGENTS.md with Go conventions ([#19](https://github.com/home-operations/ocharted/issues/19)) ([1646042](https://github.com/home-operations/ocharted/commit/1646042435f4896ff20d8a5aa19390ab44ebbecb))
* warn against secret usernames that collide with package paths ([#14](https://github.com/home-operations/ocharted/issues/14)) ([18d3500](https://github.com/home-operations/ocharted/commit/18d350085c01a4667f8b7503ea88bd557f9335ed))


### Miscellaneous Chores

* **github-action:** update action jdx/mise-action (v4.2.4 → v4.2.5) ([#31](https://github.com/home-operations/ocharted/issues/31)) ([ea60fdf](https://github.com/home-operations/ocharted/commit/ea60fdfb5783fc7ca3b11b0cd697150233d579dc))
* **go:** pin go directive to 1.26.0 ([#32](https://github.com/home-operations/ocharted/issues/32)) ([b94ccc8](https://github.com/home-operations/ocharted/commit/b94ccc8af4a10a62a402fea1d80d50b1ca4cfa1f))
* **mise:** prune lockfile to used platforms ([#22](https://github.com/home-operations/ocharted/issues/22)) ([6c1595c](https://github.com/home-operations/ocharted/commit/6c1595c70f7efe196371c1de60b08620d8b0899b))
* **mise:** Update tool cosign (3.1.2 → 3.1.3) ([#27](https://github.com/home-operations/ocharted/issues/27)) ([ab6b9e5](https://github.com/home-operations/ocharted/commit/ab6b9e5b1679ae9230dbc2564313ad0736547c1c))
* **mise:** update tool go (1.26.5 → 1.26.6) ([#35](https://github.com/home-operations/ocharted/issues/35)) ([eca9367](https://github.com/home-operations/ocharted/commit/eca9367e9a46093a519315e13c9a455c4e1273fa))
* **mise:** update tool go:golang.org/x/vuln/cmd/govulncheck (1.6.0 → v1.7.0) ([#30](https://github.com/home-operations/ocharted/issues/30)) ([d75498d](https://github.com/home-operations/ocharted/commit/d75498d4da82b4436fefb9ebfd443018b85370ed))
* **mise:** update tool helm (4.2.3 → 4.2.4) ([#34](https://github.com/home-operations/ocharted/issues/34)) ([f9a09a7](https://github.com/home-operations/ocharted/commit/f9a09a708007cb1afe975ecb4379ba85650603e3))
* **mise:** Update tool oxfmt (0.61.0 → 0.62.0) ([#25](https://github.com/home-operations/ocharted/issues/25)) ([f342722](https://github.com/home-operations/ocharted/commit/f34272283a8bc18674d39412309eb827fe33e4e5))
* **mise:** Update tool oxfmt (0.62.0 → 0.63.0) ([#28](https://github.com/home-operations/ocharted/issues/28)) ([6237458](https://github.com/home-operations/ocharted/commit/62374585f919dd46398196a47300578918135e49))
* **mise:** Update tool zizmor (1.28.0 → 1.29.0) ([#21](https://github.com/home-operations/ocharted/issues/21)) ([b5025c8](https://github.com/home-operations/ocharted/commit/b5025c85c2e7fab72d66c76f4e288f87c1e2bee6))
* **release-please:** standardize the release pull request title pattern ([#18](https://github.com/home-operations/ocharted/issues/18)) ([3f4c685](https://github.com/home-operations/ocharted/commit/3f4c68532c82eaf91f48d2831d148c96bae8cf1a))
* update pre-commit JSON format exclusions ([27aa8ef](https://github.com/home-operations/ocharted/commit/27aa8ef5b4d35f623063d0095625e9193880c8c4))

## [0.1.3](https://github.com/home-operations/ocharted/compare/0.1.2...0.1.3) (2026-07-28)


### Bug Fixes

* log the X-Forwarded-For chain and auth outcome in access logs ([#10](https://github.com/home-operations/ocharted/issues/10)) ([efec437](https://github.com/home-operations/ocharted/commit/efec437076296fff0703241f585289e12f5281f5))

## [0.1.2](https://github.com/home-operations/ocharted/compare/0.1.1...0.1.2) (2026-07-28)


### Features

* trusted-network auth bypass for cluster-local clients ([#7](https://github.com/home-operations/ocharted/issues/7)) ([aaf3862](https://github.com/home-operations/ocharted/commit/aaf38628a2ff2c5d9dc56591daabfc53a58ab858))

## [0.1.1](https://github.com/home-operations/ocharted/compare/0.1.0...0.1.1) (2026-07-28)


### Features

* **chart:** add Gateway API HTTPRoute support ([8b8e2af](https://github.com/home-operations/ocharted/commit/8b8e2afb219b88212b51d00b16b792c4389aa766))
* **chart:** add Gateway API HTTPRoute support ([#6](https://github.com/home-operations/ocharted/issues/6)) ([1d38b5e](https://github.com/home-operations/ocharted/commit/1d38b5e929bcd1aef5d2604916f71db0b21cce41))


### Reverts

* chart HTTPRoute support pending review ([10ff834](https://github.com/home-operations/ocharted/commit/10ff83456a2c07e0ee0ab8f915e798f6b2fcb616))


### Miscellaneous Chores

* **mise:** Update tool oxfmt (0.60.0 → 0.61.0) ([#3](https://github.com/home-operations/ocharted/issues/3)) ([ff097cb](https://github.com/home-operations/ocharted/commit/ff097cbda863f2a4e1a068136ca00739d79c6b5c))

## 0.1.0 (2026-07-28)


### ⚠ BREAKING CHANGES

* rename project to ocharted

### Features

* initial implementation ([edd2a56](https://github.com/home-operations/ocharted/commit/edd2a56cc819160343d8f2e15fe3d57411687c67))
* opt-in dependency URL rewriting ([211d2f1](https://github.com/home-operations/ocharted/commit/211d2f1d09842d8b3e021fe328c8013bf986ca35))
* rename project to ocharted ([9956d47](https://github.com/home-operations/ocharted/commit/9956d4784a3eba57f74dc407ada94d0a86406a50))
* stale-if-error index serving and resiliency docs ([ac78742](https://github.com/home-operations/ocharted/commit/ac7874252d4051e604c7da4ab3dbeb10bb2b9a2d))


### Bug Fixes

* commit raw helm-docs output for the chart README ([2fea566](https://github.com/home-operations/ocharted/commit/2fea5665417d9957f0722d713b607bd7208db9c7))


### Documentation

* credit helm-charts-oci-proxy and compare features ([e665097](https://github.com/home-operations/ocharted/commit/e665097e36302e5382f876b8ef14ebe801938b9c))


### Miscellaneous Chores

* prepare initial release ([2808f21](https://github.com/home-operations/ocharted/commit/2808f215c9b32a4e7e955e371758f85565fa1310))
* switch license to Apache 2.0 ([1caf25d](https://github.com/home-operations/ocharted/commit/1caf25dda8b9ca62196cf5bf4b673fe0cbfe8bc7))


### Code Refactoring

* deduplicate resolver lookups and response writing ([12384a2](https://github.com/home-operations/ocharted/commit/12384a23b26b4da0c76f3ab010053875c2b5a985))
