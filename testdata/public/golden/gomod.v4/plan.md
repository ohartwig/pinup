# pinup plan for golden/gomod

12 dependencies in 2 files, 7 lookups (0 from cache), 5 updates (4 held), 1 branches to write.

## Branches

| Branch | Title | Changes |
|---|---|---|
| `renovate/go-golang.org-x-mod-vulnerability` | chore(deps): update module golang.org/x/mod to v0.40.0 [security] | `go.mod` v0.39.0 → v0.40.0; `go mod tidy` |

## Held

| Branch | Update | Reason | Held by | Thaws |
|---|---|---|---|---|
| `renovate/aws-sdk-go-v2-monorepo` | github.com/aws/aws-sdk-go-v2 v1.43.7 → v1.47.0 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/aws-sdk-go-v2-monorepo` | github.com/aws/aws-sdk-go-v2 v1.43.8 → v1.47.0 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/github.com-spf13-cobra-1.x` | github.com/spf13/cobra v1.9.1 → v1.10.2 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/golang.org-x-mobile-digest` | golang.org/x/mobile v0.0.0-20260821190718-4776eadac327 → v0.0.0-20260908204917-8b95e45f8d3e | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |

## Not planned

| Dependency | File | Reason |
|---|---|---|
| golang.org/x/sys v0.45.0 | `go.mod` | indirect dependency |
| golang.org/x/tools v0.30.0 | `tools/go.mod` | indirect dependency |

## Advisories

| Dependency | Advisories | Fix at or above |
|---|---|---|
| golang.org/x/mod v0.39.0 | GO-2026-6179, GO-2026-6180 | 0.40.0 |

## Warnings

- **config**: preset "abandonments:recommended" has no effect: abandonment detection is not implemented; the preset resolves to nothing
- **config**: preset "mergeConfidence:all-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself (docs/plan.md §3.5) and contacts no third-party service, so the preset resolves to nothing
