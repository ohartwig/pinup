# The preset library

`presets.json` is pinup's own: written for the behaviour the configurations
it serves need, under the names Renovate uses, so a configuration's
`extends` reads the same in both tools. Nothing in it is taken from the
Renovate tree.

## Why it is not Renovate's

Renovate's presets - the small option sets like `:dependencyDashboard` as
much as the curated lists behind `group:monorepos`, `replacements:all` and
`abandonments:recommended` - are data in an AGPL-3.0 tree (`lib/config/
presets/internal/*.preset.ts`, `lib/data/*.json`), with no licence of their
own. Until 0.16.0 the library (then `library.json`, filtered from the public history with its generator) was generated from the definitions the pinned
container's resolver handed back for the 1085 presets in the estate's
closure: gained by execution, identical in content. That is not a clean
room for data, whatever it is for behaviour, and it shipped a curated
collection - 461 monorepo groups, 63 replacement rules, their descriptions -
under MIT. Decided 2026-09-14 (docs/tasks.md, D.21): the library is
authored, and carries what the configurations use.

## What it carries

- The structural presets the estate's and the twin's `extends` reach:
  `config:recommended` (as pinup composes it), `:dependencyDashboard`,
  `:configMigration`, `:automergeDigest`, `:pinDevDependencies`,
  `:semanticPrefixFixDepsChoreOthers` (the two rules that decide anything
  here), `:ignoreModulesAndTests`, `docker:pinDigests`,
  `helpers:pinGitHubActionDigests`, the two annotated-`_VERSION` custom
  managers and the tsconfig one.
- The workarounds for ecosystems pinup reads: Node.js versioning for
  `@types/node` and the `node` images, alpine's stable/edge split, the
  containerbase images, the clamav, grafana and k3s tag grammars.
- The groups the corpora and the open branches show in use: `group:nodeJs`,
  `group:phpstan`, `group:symfony`, and under `group:monorepos` the
  aws-sdk-go-v2 and vitest monorepos. Each monorepo entry is one fact -
  which source URL its packages share - written here as such; add a
  `group:<name>Monorepo` and a `monorepo:<name>` pair for another.
- Empty, by name, so a configuration that extends them still resolves:
  `replacements:all` (name a replacement with `replacementName` yourself),
  and the inert `mergeConfidence:*` and `abandonments:recommended`, which
  resolve to nothing and say so once per run.

Not carried: the digest-changelog and Go-package link helpers (the
merge-request body is rendered by `report` from the plan, compare links
come from `changelog`), the workarounds for managers pinup does not have
(maven, gradle, sbt, the JDK and Red Hat images), the monorepo, replacement
and abandonment lists.

## How it is checked

Not against Renovate's preset data - that is the point - but by effect:

- `rules/parity_test` applies every vector Renovate recorded against its
  own resolution of the configuration (3366 for the estate, 1728 for the
  twin) to pinup's resolution with this library, and every key a rule wrote
  must agree, the file's own rules by index. A rule whose effect this
  library lost goes red on the keys it wrote; the keys pinup does not read
  are listed there by name (`unreadKeys`).
- `config/flatten_test` compares every resolved option to the container's,
  rules, custom managers and descriptions aside.
- The corpus tests compare what the two custom managers extract.
- `config/preset` checks the resolver's semantics on synthetic presets
  (order, override, description accumulation as measured) and that the
  library is closed and resolves.

Renovate's own expansions of the two configurations, and the closure the
earlier library was generated from, are kept under `testdata/upstream` for
parity work - outside the public mirror, and read by no test.
