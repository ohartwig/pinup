# pinup plan for golden/gitrefs

2 dependencies in 2 files, 4 lookups (0 from cache), 2 updates (2 held), 0 branches to write.

## Held

| Branch | Update | Reason | Held by | Thaws |
|---|---|---|---|---|
| `renovate/https-github.com-crowdsecurity-hub-digest` | https://github.com/crowdsecurity/hub master → master | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |
| `renovate/pin-dependencies` | node 24-alpine → 24-alpine | schedule `outside * 0,4,8,12,16,20 * * * (Europe/Berlin)` | packageRules[54] | 2026-09-14T14:00:00Z |

## Warnings

- **config**: preset "abandonments:recommended" has no effect: abandonment detection is not implemented; the preset resolves to nothing
- **config**: preset "mergeConfidence:age-confidence-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself and contacts no third-party service, so the preset resolves to nothing
- **config**: preset "mergeConfidence:all-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself (docs/plan.md §3.5) and contacts no third-party service, so the preset resolves to nothing
