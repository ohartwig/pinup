# pinup plan for golden/osv

4 dependencies in 2 files, 4 lookups (0 from cache), 4 updates (0 held), 4 branches to write.

## Branches

| Branch | Title | Changes |
|---|---|---|
| `renovate/npm-lodash-vulnerability` | fix(deps): update dependency lodash to v4.18.1 [security] | `package.json` 4.17.20 → 4.18.1 |
| `renovate/npm-minimist-vulnerability` | fix(deps): update dependency minimist to ^1.2.6 [security] | `package.json` ^1.2.5 → ^1.2.6 |
| `renovate/packagist-guzzlehttp-guzzle-vulnerability` | fix(deps): update dependency guzzlehttp/guzzle to v7.15.2 [security] | `composer.json` 7.4.4 → 7.15.2 |
| `renovate/packagist-symfony-http-kernel-vulnerability` | fix(deps): update dependency symfony/http-kernel to v6.0.20 [security] | `composer.json` 6.0.0 → 6.0.20 |

## Advisories

| Dependency | Advisories | Fix at or above |
|---|---|---|
| guzzlehttp/guzzle 7.4.4 | GHSA-25mq-v84q-4j7r, GHSA-94pj-82f3-465w, GHSA-cwxw-98qj-8qjx, GHSA-f283-ghqc-fg79, GHSA-f7vp-7xgx-4w4r, GHSA-g446-98w2-8p5w, GHSA-h95v-h523-3mw8, GHSA-q559-8m2m-g699, GHSA-v5mv-p594-2x33, GHSA-wm3w-8rrp-j577, GHSA-wpwq-4j6v-78m3 | 7.15.2 |
| symfony/http-kernel 6.0.0 | GHSA-h7vf-5wrv-9fhv | 6.0.20 |
| lodash 4.17.20 | GHSA-29mw-wpgm-hmr9, GHSA-35jh-r3h4-6jhm, GHSA-f23m-r3pf-42rh, GHSA-r5fr-rjxr-66jc, GHSA-xxjr-mmjv-4gpg | 4.18.0 |
| minimist ^1.2.5 | GHSA-xvch-5gv4-984h | 1.2.6 |

## Warnings

- **config**: preset "abandonments:recommended" has no effect: abandonment detection is not implemented; the preset resolves to nothing
- **config**: preset "mergeConfidence:all-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself (docs/plan.md §3.5) and contacts no third-party service, so the preset resolves to nothing
