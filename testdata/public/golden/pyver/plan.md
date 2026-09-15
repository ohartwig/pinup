# pinup plan for golden/pyver

4 dependencies in 3 files, 4 lookups (0 from cache), 3 updates (3 held), 0 branches to write.

## Held

| Branch | Update | Reason | Held by | Thaws |
|---|---|---|---|---|
| `renovate/boto3-1.x` | boto3 1.43.93 → 1.43.94 | minimumReleaseAge `datasource old since 2026-09-14T20:10:03Z (20 hours), needs 24 hours` | config | 2026-09-15T20:10:03Z |
| `renovate/boto3-1.x` | boto3 1.43.93 → 1.43.94 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-15T18:00:00Z |
| `renovate/git-filter-repo-2.x` | git-filter-repo 2.45.0 → 2.47.0 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-15T18:00:00Z |
| `renovate/yamllint-1.x` | yamllint 1.35.1 → 1.38.0 | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-15T18:00:00Z |

## Warnings

- **config**: preset "abandonments:recommended" has no effect: abandonment detection is not implemented; the preset resolves to nothing
- **config**: preset "mergeConfidence:age-confidence-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself and contacts no third-party service, so the preset resolves to nothing
- **config**: preset "mergeConfidence:all-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself (docs/plan.md §3.5) and contacts no third-party service, so the preset resolves to nothing
