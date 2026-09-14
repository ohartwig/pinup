# Vulnerability alerts, as measured

Renovate 43.288.0, `--dry-run=lookup`, `osvVulnerabilityAlerts: true`,
`minimumReleaseAge: "30 days"`, over a synthetic tree with `lodash 4.17.20`,
`minimist ^1.2.5` (npm), `guzzlehttp/guzzle 7.4.4`, `symfony/http-kernel 6.0.0`
(composer). Captured 2026-09-11 from the container's log; no code was read.

## What the log says

- `fetchVulnerabilities() - osvVulnerabilityAlerts=true`, then one
  `Vulnerability <ID> affects <name> <version>` per advisory, then per
  advisory `Setting allowed version >= <fixed> to fix vulnerability <ID> in
  <name> <version>`:
  - lodash 4.17.20 → 4.17.21, 4.17.21, 4.17.23, 4.18.0, 4.18.0
  - guzzlehttp/guzzle 7.4.4 → 7.4.5 ×2, 7.12.1 ×2, 7.12.3, 7.14.2, 7.15.1 ×3,
    7.15.2 ×2
  - symfony/http-kernel 6.0.0 → 6.0.20
- `Skipping vulnerability lookup for package minimist due to unsupported
  version ^1.2.5` — a range is not queried.
- `Using vulnerabilityFixStrategy=lowest for <name>` for each of the three.
- The resulting updates: lodash → **4.18.1** (`4.18.0` exists and is
  deprecated on npm: "Bad release"), guzzle → **7.15.2**, symfony/http-kernel
  → **6.0.20**; each on branch `renovate/<datasource>-<depNameSanitized>-vulnerability`
  (`renovate/npm-lodash-vulnerability`) — the `vulnerabilityAlerts`
  configuration's `branchTopic`.

## What that settles

- The effective lower bound is the **highest** fix version across the
  advisories affecting the current version; the update is the **lowest**
  released, non-deprecated version at or above it.
- The `vulnerabilityAlerts` object of the configuration overlays the update
  (branch topic, labels, `[SECURITY]` suffix, `minimumReleaseAge: null`,
  `schedule: at any time`, automerge as configured) — the fast path.
- Ecosystems sent to OSV: `npm` → `npm`, `packagist` → `Packagist`.

The queries and advisory documents behind this are served by
`https://api.osv.dev/v1/querybatch` and `/v1/vulns/{id}`.
