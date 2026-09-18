# pinup plan for golden/gitrefs

2 dependencies in 2 files, 4 lookups (0 from cache), 2 updates (0 held), 2 branches to write.

## Branches

| Branch | Title | Changes |
|---|---|---|
| `renovate/https-github.com-crowdsecurity-hub-digest` | chore(deps): update https://github.com/crowdsecurity/hub digest to 4a9e186 | `crowdsec-hub.yaml` 6359f3c81f253cddf075afae75e5355e2be1eef0 → 4a9e186dc93e642bf8b33fe5fcc7c319a276676e; `node tools/update-expected-commit.mjs crowdsec-hub.yaml` |
| `renovate/pin-dependencies` | ci(deps): pin node.js to 50c8e8c | `.gitlab-ci.yml` 24-alpine → 24-alpine@sha256:50c8e8ca1d27439048670df5883f32d57cf81cff6233222c893fd0d9884cbd81 |

## Warnings

- **config**: preset "abandonments:recommended" has no effect: abandonment detection is not implemented; the preset resolves to nothing
- **config**: preset "mergeConfidence:all-badges" has no effect: merge confidence badges call developer.mend.io; pinup classifies risk itself (docs/plan.md §3.5) and contacts no third-party service, so the preset resolves to nothing
