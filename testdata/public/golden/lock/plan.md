# pinup plan for golden/lock

4 dependencies in 3 files, 4 lookups (0 from cache), 7 updates (4 held), 3 branches to write.

## Branches

| Branch | Title | Changes |
|---|---|---|
| `renovate/monolog-monolog-3.x` | fix(deps): update dependency monolog/monolog to v3.12.0 | `composer.json` 3.5.0 → 3.12.0; `composer update monolog/monolog --with-all-dependencies --no-plugins --no-install --no-scripts --no-audit --ignore-platform-reqs` |
| `renovate/npm` | fix(deps): update dependency minimist to v1.2.8 | `package.json` 1.2.7 → 1.2.8; `npm install --package-lock-only --no-audit --ignore-scripts minimist` |
| `renovate/psr-log-3.x` | fix(deps): update dependency psr/log to v3.0.2 | `composer.json` 3.0.0 → 3.0.2; `composer update psr/log --with-all-dependencies --no-plugins --no-install --no-scripts --no-audit --ignore-platform-reqs` |

## Held

| Branch | Update | Reason | Held by | Thaws |
|---|---|---|---|---|
| `renovate/js-tokens-10.x` | js-tokens ^4.0.0 → ^10.0.0 | dependencyDashboardApproval | packageRules[36] | — |
| `renovate/lock-file-maintenance` | lock file  →  | schedule `outside after 1am and before 6am (Europe/Berlin)` | packageRules[67] | 2026-09-14T23:00:00Z |
| `renovate/lock-file-maintenance` | lock file  →  | schedule `outside after 1am and before 6am (Europe/Berlin)` | packageRules[67] | 2026-09-14T23:00:00Z |
| `renovate/lock-file-maintenance` | lock file  →  | schedule `outside after 1am and before 6am (Europe/Berlin)` | packageRules[67] | 2026-09-14T23:00:00Z |

## Warnings

- **config**: preset "abandonments:recommended" has no effect: abandonment detection is not implemented; the preset resolves to nothing
- **config**: preset "mergeConfidence:age-confidence-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself and contacts no third-party service, so the preset resolves to nothing
- **config**: preset "mergeConfidence:all-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself (docs/plan.md §3.5) and contacts no third-party service, so the preset resolves to nothing
