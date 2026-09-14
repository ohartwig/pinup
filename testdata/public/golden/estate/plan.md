# pinup plan for golden/estate

16 dependencies in 4 files, 16 lookups (0 from cache), 3 updates (3 held), 0 branches to write.

## Held

| Branch | Update | Reason | Held by | Thaws |
|---|---|---|---|---|
| `renovate/pin-dependencies` | bash alpine3.22 → alpine3.22 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/terraform-aws-modules-vpc-aws-5.x` | terraform-aws-modules/vpc/aws 5.0.0 → 5.21.0 | minimumReleaseAge `the release's age is unknown and minimumReleaseAgeBehaviour is not timestamp-optional` | config | — |
| `renovate/terraform-aws-modules-vpc-aws-5.x` | terraform-aws-modules/vpc/aws 5.0.0 → 5.21.0 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/terraform-aws-modules-vpc-aws-6.x` | terraform-aws-modules/vpc/aws 5.0.0 → 6.7.2 | dependencyDashboardApproval | packageRules[36] | — |
| `renovate/terraform-aws-modules-vpc-aws-6.x` | terraform-aws-modules/vpc/aws 5.0.0 → 6.7.2 | minimumReleaseAge `the release's age is unknown and minimumReleaseAgeBehaviour is not timestamp-optional` | packageRules[36] | — |
| `renovate/terraform-aws-modules-vpc-aws-6.x` | terraform-aws-modules/vpc/aws 5.0.0 → 6.7.2 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |

## Not planned

| Dependency | File | Reason |
|---|---|---|
| devops/ci-cd-components/lint-tools 1 | `.gitlab-ci.yml` | lookup failed: gitlab-releases: devops/ci-cd-components/lint-tools: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Flint-tools/releases?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host |
| devops/ci-cd-components/lint-tools 1 | `.gitlab-ci.yml` | lookup failed: gitlab-tags: devops/ci-cd-components/lint-tools: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Flint-tools/repository/tags?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host |
| devops/ci-cd-components/release-tools 1.17.59 | `.gitlab-ci.yml` | lookup failed: gitlab-releases: devops/ci-cd-components/release-tools: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Frelease-tools/releases?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host |
| devops/ci-cd-components/release-tools 1.17.59 | `.gitlab-ci.yml` | lookup failed: gitlab-tags: devops/ci-cd-components/release-tools: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Frelease-tools/repository/tags?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host |
| devops/ci-cd-components/supply-chain-verify 2 | `.gitlab-ci.yml` | lookup failed: gitlab-releases: devops/ci-cd-components/supply-chain-verify: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Fsupply-chain-verify/releases?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host |
| devops/ci-cd-components/supply-chain-verify 2 | `.gitlab-ci.yml` | lookup failed: gitlab-tags: devops/ci-cd-components/supply-chain-verify: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Fsupply-chain-verify/repository/tags?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host |
| registry.acme.test/devops/ci-mirrors/alpine 3.22 | `.gitlab-ci.yml` | lookup failed: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/ci-mirrors/alpine/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host |
| registry.acme.test/devops/images/wolfi-base 2 | `Containerfile` | lookup failed: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/images/wolfi-base/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host |
| php-frankenphp-8.5 8.5.10-r0 | `Containerfile` | lookup failed: custom.corp-apk: php-frankenphp-8.5: httpx: request failed: Get "http://127.0.0.1:8099/corp/php-frankenphp-8.5.json": dial tcp 127.0.0.1:8099: connect: connection refused |
| acme/sitepackage ^5.0 | `composer.json` | disabled by packageRules[30] |
| acme/sitepackage ^5.0 | `composer.json` | lookup failed: gitlab-packages: development/acme/sitepackage: httpx: request failed: Get "https://git.acme.test/api/v4/projects/development%2Facme%2Fsitepackage/packages?package_name=acme%2Fsitepackage&per_page=100&page=1": dial tcp: lookup git.acme.test: no such host |

## Warnings

- **config**: preset "abandonments:recommended" has no effect: abandonment detection is not implemented; the preset resolves to nothing
- **config**: preset "mergeConfidence:age-confidence-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself and contacts no third-party service, so the preset resolves to nothing
- **config**: preset "mergeConfidence:all-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself (docs/plan.md §3.5) and contacts no third-party service, so the preset resolves to nothing
- **lookup**: development/acme/sitepackage:acme/sitepackage via gitlab-packages: gitlab-packages: development/acme/sitepackage: httpx: request failed: Get "https://git.acme.test/api/v4/projects/development%2Facme%2Fsitepackage/packages?package_name=acme%2Fsitepackage&per_page=100&page=1": dial tcp: lookup git.acme.test: no such host
- **lookup**: devops/ci-cd-components/lint-tools via gitlab-releases: gitlab-releases: devops/ci-cd-components/lint-tools: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Flint-tools/releases?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host
- **lookup**: devops/ci-cd-components/lint-tools via gitlab-tags: gitlab-tags: devops/ci-cd-components/lint-tools: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Flint-tools/repository/tags?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host
- **lookup**: devops/ci-cd-components/release-tools via gitlab-releases: gitlab-releases: devops/ci-cd-components/release-tools: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Frelease-tools/releases?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host
- **lookup**: devops/ci-cd-components/release-tools via gitlab-tags: gitlab-tags: devops/ci-cd-components/release-tools: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Frelease-tools/repository/tags?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host
- **lookup**: devops/ci-cd-components/supply-chain-verify via gitlab-releases: gitlab-releases: devops/ci-cd-components/supply-chain-verify: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Fsupply-chain-verify/releases?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host
- **lookup**: devops/ci-cd-components/supply-chain-verify via gitlab-tags: gitlab-tags: devops/ci-cd-components/supply-chain-verify: httpx: request failed: Get "https://git.acme.test/api/v4/projects/devops%2Fci-cd-components%2Fsupply-chain-verify/repository/tags?per_page=100&page=1": dial tcp: lookup git.acme.test: no such host
- **lookup**: php-frankenphp-8.5 via custom.corp-apk: custom.corp-apk: php-frankenphp-8.5: httpx: request failed: Get "http://127.0.0.1:8099/corp/php-frankenphp-8.5.json": dial tcp 127.0.0.1:8099: connect: connection refused
- **lookup**: registry.acme.test/devops/ci-mirrors/alpine via docker: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/ci-mirrors/alpine/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host
- **lookup**: registry.acme.test/devops/images/wolfi-base via docker: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/images/wolfi-base/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host
