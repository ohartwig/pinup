# pinup plan for golden/tfversion

14 dependencies in 4 files, 12 lookups (0 from cache), 7 updates (7 held), 0 branches to write.

## Held

| Branch | Update | Reason | Held by | Thaws |
|---|---|---|---|---|
| `renovate/alpine` | alpine  →  | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/argoproj-argo-cd-2.x` | argoproj/argo-cd v2.12.3 → v2.14.21 | minimumReleaseAge `the release's age is unknown and minimumReleaseAgeBehaviour is not timestamp-optional` | config | — |
| `renovate/argoproj-argo-cd-2.x` | argoproj/argo-cd v2.12.3 → v2.14.21 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/argoproj-argo-cd-3.x` | argoproj/argo-cd v2.12.3 → v3.5.3 | dependencyDashboardApproval | packageRules[36] | — |
| `renovate/argoproj-argo-cd-3.x` | argoproj/argo-cd v2.12.3 → v3.5.3 | minimumReleaseAge `the release's age is unknown and minimumReleaseAgeBehaviour is not timestamp-optional` | packageRules[36] | — |
| `renovate/argoproj-argo-cd-3.x` | argoproj/argo-cd v2.12.3 → v3.5.3 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/grafana-10.x` | grafana 8.5.1 → 10.5.15 | dependencyDashboardApproval | packageRules[36] | — |
| `renovate/grafana-10.x` | grafana 8.5.1 → 10.5.15 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/grafana-8.x` | grafana 8.5.1 → 8.15.0 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/hashicorp-terraform-1.x` | hashicorp/terraform 1.9.8 → 1.16.2 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/hashicorp-terraform-1.x` | hashicorp/terraform v1.7.0 → v1.16.2 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |

## Not planned

| Dependency | File | Reason |
|---|---|---|
| registry.acme.test/devops/images/wolfi-base 2 | `base/kustomization.yaml` | lookup failed: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/images/wolfi-base/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host |
| git.acme.test/platform/gitops/manifests 1.4.0 | `overlays/prod/kustomization.yaml` | lookup failed: git-tags: ls-remote https://git.acme.test/platform/gitops/manifests.git: exit status 128: fatal: unable to access 'https://git.acme.test/platform/gitops/manifests.git/': Could not resolve host: git.acme.test |
| git.acme.test/platform/gitops/manifests v0.9.1 | `overlays/prod/kustomization.yaml` | lookup failed: git-tags: ssh://***@git.acme.test/platform/gitops/manifests.git is an ssh:// URL; only an unauthenticated public repository is supported |
| registry.acme.test/devops/images/golang 1.27.0 | `overlays/prod/kustomization.yaml` | lookup failed: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/images/golang/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host |
| registry.acme.test/devops/ci-mirrors/nginx 1.27.2 | `overlays/prod/kustomization.yaml` | lookup failed: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/ci-mirrors/nginx/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host |
| busybox 1.36 | `overlays/prod/kustomization.yaml` | invalid-dependency-specification |
| prometheus 25.8.0 | `overlays/prod/kustomization.yaml` | lookup failed: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/charts/prometheus/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host |

## Warnings

- **config**: preset "abandonments:recommended" has no effect: abandonment detection is not implemented; the preset resolves to nothing
- **config**: preset "mergeConfidence:age-confidence-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself and contacts no third-party service, so the preset resolves to nothing
- **config**: preset "mergeConfidence:all-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself (docs/plan.md §3.5) and contacts no third-party service, so the preset resolves to nothing
- **lookup**: https://git.acme.test/platform/gitops/manifests.git via git-tags: git-tags: ls-remote https://git.acme.test/platform/gitops/manifests.git: exit status 128: fatal: unable to access 'https://git.acme.test/platform/gitops/manifests.git/': Could not resolve host: git.acme.test
- **lookup**: registry.acme.test/devops/charts/prometheus via docker: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/charts/prometheus/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host
- **lookup**: registry.acme.test/devops/ci-mirrors/nginx via docker: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/ci-mirrors/nginx/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host
- **lookup**: registry.acme.test/devops/images/golang via docker: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/images/golang/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host
- **lookup**: registry.acme.test/devops/images/wolfi-base via docker: docker: request to registry.acme.test failed: Get "https://registry.acme.test/v2/devops/images/wolfi-base/tags/list?n=1000": dial tcp: lookup registry.acme.test: no such host
