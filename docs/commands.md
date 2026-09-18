<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: MIT
-->

# Commands

`pinup <command> [flags]`. Every command prints its flags with `-h`.
Flags take one dash or two.

## whatif

Resolve the configuration and plan; write nothing. Works on a checkout
and needs no platform token.

| Flag | Meaning |
|---|---|
| `--repo`, `--dir` | the checkout (default `.`) |
| `--config` | the configuration file, or a `local>` preset the platform serves |
| `--name` | the repository path recorded in the plan |
| `--report` | write the plan as JSON here instead of stdout |
| `--now` | plan as if it were this moment (RFC 3339); schedules and release ages are judged against it |
| `--cache` | the lookup cache (bbolt); empty means every lookup is cold |
| `--cache-ttl` | how long a cached lookup counts as fresh (default 1h) |

## run

Plan, then apply and publish: push the branches, open or update the merge
requests, write the dashboard issue. One of `--repo`, `--project`,
`--autodiscover` or `--released` says what to run against.

| Flag | Meaning |
|---|---|
| `--repo`, `--dir` | an existing checkout with an `origin` remote |
| `--project` | a project path to clone and run against |
| `--autodiscover` | every project the token can see that matches these globs, a JSON list with `!` negations; eight at a time |
| `--released` | the fast lane: a project that was just released, optionally `@version`; runs only its consumers from the index, only for that dependency, with fresh lookups |
| `--package` | narrow the run to one external package, `datasource:name` (`npm:lodash`), with a fresh lookup for it; the advisory watch's targeted run |
| `--index` | the consumer index (default `consumers.json` beside `--cache`); several, comma-separated or a pattern, are merged and read only |
| `--base` | plan and branch from this branch instead of the project's default branch |
| `--dry-run` | plan only; push nothing, open nothing - `whatif` with the platform's view of the existing merge requests |
| `--config`, `--report`, `--cache`, `--cache-ttl` | as for `whatif`; a `%s` in `--report` becomes the project path |

Every run also reads the dashboard issue's ticked boxes before it plans,
and writes the consumer index the fast lane and the advisory watch read.

## print-config

Print the resolved configuration.

| Flag | Meaning |
|---|---|
| `--config` | the configuration to resolve |
| `--explain` | print every source that set this path, winner last (`packageRules[25].automerge`) |
| `--diff` | compare against a resolved snapshot (JSON) and print the differing lines |
| `--json` | the resolved document as JSON instead of flattened lines |
| `--runner` | the runner's configuration a `local>` extends resolves to: a path, or `local>project` fetched through the platform; the aliases answer for `PINUP_RUNNER_PROJECT` (or the project named) and for `devops/renovate-runner` |

The flattened lines are the format the parity tests compare Renovate's
resolution against; rule order is part of the meaning, so it is a list,
not a tree.

## migrate

Classify a configuration, key by key, as supported, partial or
unsupported ([configuration](configuration.md) has the table). It changes
no file unless asked to.

| Flag | Meaning |
|---|---|
| `--config` | the configuration to classify |
| `--json` | the classification as JSON |
| `--to yaml` | rewrite the file as YAML: descriptions become comments, scalars YAML would misread are quoted |
| `--out` | with `--to`: write here instead of stdout |
| `--keep-descriptions` | with `--to yaml`: keep the description keys as well as the comments |
| `--runner` | as for print-config: without it a file that extends the runner is refused as unknown, not classified around the gap |
| `--extends old=new` | with `--to`: rename one extends entry (repeatable); the rewrite must resolve to the same document, so a rename to a name the chain does not answer is refused |

`--to yaml` also drops `$schema`: it names Renovate's schema, and on a
`.pinup.yaml` that would be a lie. What a repository's move looks like:

```bash
pinup migrate --config renovate.json --runner "local>pinup/runner" \
  --extends "local>devops/renovate-runner=local>pinup/runner" \
  --to yaml --out .pinup.yaml
git rm renovate.json   # pinup refuses two configuration files
```

## advise

Analyse a configuration as a run resolves it - defaults, presets, file -
and report what to change, in four categories: **compat** (what this
version of pinup does not read, cannot evaluate, rewrote or would refuse),
**hygiene** (what the file says twice or for nothing), **performance**
(work a run does for nothing) and **security** (what widens the blast
radius of an update nobody looked at). Every finding names its pointer,
the layer that wrote the value - the file, a preset, the builtin defaults -
and, where the file owns the value, a fix.

| Flag | Meaning |
|---|---|
| `--config` | the configuration to analyse (required) |
| `--runner` | as for print-config: the runner's configuration a `local>` extends resolves to |
| `--plan` | a plan `whatif` wrote (repeatable); enables the checks that read what runs found |
| `--skip` | a check ID to leave out of the report and the fixes (repeatable) |
| `--json` | the report as JSON |
| `--strict` | exit 1 when any finding is an `error`; warnings never fail |
| `--fix` | apply the fixes in memory and verify the result - a dry run unless `--out` or `--write` |
| `--out` | with `--fix`: write the fixed file here |
| `--write` | with `--fix`: write the fixed file in place, keeping its mode |

Without `--strict` the exit code is 0: advice is not a gate unless a job
asks for one. The text report groups findings by severity, then category:

```text
renovate.json  (2 plans)

warn (2)
  performance
    perf/ignore-paths-replaced  /ignorePaths
      ignorePaths replaces the inherited list; **/node_modules/**, … are walked again
      fix:  set /ignorePaths = ["**/node_modules/**", …, "**/prometheus-exporter/**"]
      from: file:renovate.json
  …
skipped: 6 plan checks (no --plan)
```

`--json` prints `{"file", "plans", "skipped", "counts": {error, warn,
info}, "findings": [{id, category, severity, pointer, frame, origin, msg,
fix}], "fix": {applied, skipped, wrote}}`. A finding's `frame` says which
document its pointer indexes: `file` for the document as written, `resolved`
for what a run reads - the presets' `packageRules` stand before the file's
in the resolved frame, so the two numberings differ. A fix always points
into the file.

### The fixes

A fix is `{pointer, op: set | remove | append, value, changes}`. `--fix`
applies every fix the findings carry to the file's bytes - comments, key
order and formatting untouched - from the highest pointer down, so a
removal never moves what a later fix points at. Two fixes at one pointer
are both skipped; a fix under a pointer another removes is skipped.

Nothing is written until the result has passed the gate: the file is
resolved before and after, both are flattened as `print-config` prints
them, and every line that differs must lie under a pointer some applied fix
declared in `changes`. A fix that declares nothing must leave the
resolution byte-identical - removing a key a preset already sets, say. A
rewrite that changes anything else is refused, named, and not written.

`--fix` alone is a dry run and prints what would be applied. `--fix --out
path` writes there; `--fix --write` writes in place. JSON, JSONC and JSON5
files are rewritten; a fix on a YAML file is reported with the line to
write by hand, until the YAML rewriter lands.

### The checks

| ID | Severity | Fires when | Fix |
|---|---|---|---|
| `compat/preset-inert` | warn | the file extends a preset that resolves to nothing | remove the entry |
| `compat/migrated` | info | the run rewrote a value the file wrote (`minimumReleaseAge: "0"` → null) | write the migrated value |
| `compat/rule-not-evaluable` | warn | a rule carries a matcher the engine does not evaluate; it never fires | — |
| `compat/rules-not-compilable` | error | a rule carries a matcher the engine has never heard of | — |
| `compat/key-unsupported` | warn | a key nothing in pinup reads | remove it |
| `compat/key-partial` | info | a key pinup honours in part | — |
| `compat/manager-unknown` | warn | an enabled manager nothing implements | remove it |
| `compat/datasource-unknown` | warn | a `matchDatasources` or `datasourceTemplate` no lookup serves | — |
| `compat/schedule-invalid` | error | a schedule or timezone that does not parse | — |
| `compat/regex-not-re2` | error | a regex with a lookaround or backreference (tech-spec §0.2) | — |
| `hygiene/redundant-inherited` | info | a top-level key repeating what a preset sets | remove it |
| `hygiene/redundant-default` | info | a top-level key set to the builtin default | remove it |
| `hygiene/null-clears-nothing` | warn | a `null` over a key no preset sets | remove it |
| `hygiene/schedule-anytime-explicit` | info | a schedule spelling out "at any time" | remove it, unless it opens an inherited window |
| `hygiene/extends-duplicate` | warn | a preset extended twice | remove the second |
| `hygiene/extends-transitive` | info | a preset another entry already reaches | remove it |
| `hygiene/duplicate-entry` | info | a string listed twice in a set-like list | remove the second |
| `hygiene/rule-no-matchers` | warn | a rule matching every dependency | — |
| `hygiene/rule-no-effect` | warn | a rule setting nothing | remove it |
| `hygiene/rule-duplicate-matchers` | info | two rules selecting the same dependencies | remove the shadowed one |
| `hygiene/custom-manager-duplicate` | warn | two identical custom managers | remove the second |
| `perf/ignore-paths-replaced` | warn | `ignorePaths` replacing the inherited list (arrays replace) | set the union |
| `perf/lock-file-maintenance-unscheduled` | warn | a lock refresh with no window | set a weekly window |
| `perf/pr-limit-unbounded` | info | `prHourlyLimit` or `prConcurrentLimit` at 0 | — |
| `perf/file-pattern-catch-all` | info | a custom manager reading every file | — |
| `sec/automerge-major` | error | an automerge that covers major updates | restrict `matchUpdateTypes`, or `major.automerge: false` |
| `sec/trust-effective` | warn | a rule letting the analyzer's label relax an automerge | — |
| `sec/match-effective-without-trust` | info | a rule on the analyzer's label without `trustEffective` | — |
| `sec/registry-http` | warn | a registry over plaintext | `https://` |
| `sec/vulnerability-alerts-off` | warn / info | advisories switched off / never on | switch on |
| `sec/pin-digests-off` | info | container images in use and none pinned by digest | extend `docker:pinDigests` |
| `sec/post-upgrade-tasks-unallowed` | info | tasks the configuration's `allowedCommands` does not admit | — |
| `sec/allowed-commands-catch-all` | error | an allowlist admitting every command | — |
| `sec/minimum-release-age-unset` | info | no `minimumReleaseAge` anywhere | `3 days` |
| `sec/ignore-unstable-false` | warn | prereleases let in wholesale | switch on |
| `plan/rule-never-matched` | info | a rule of the file's no dependency in the plans reached | — |
| `plan/manager-idle` | info | an enabled manager that produced nothing | remove it |
| `plan/custom-manager-idle` | info | a custom manager that produced nothing | — |
| `plan/all-held` | warn | a plan whose every update one setting holds | — |
| `plan/limit-holds` | info | updates the two caps held | — |
| `plan/datasource-failing` | warn | a custom datasource whose lookups the plans record as failed | — |

The `plan/*` checks run only with `--plan`; the report counts them as
skipped otherwise. Every check has a test case that makes it fire, and a
test asserts that every check in the catalogue has one.

## advisories

Ask OSV about every dependency the consumer index carries and report the
advisories not reported before. No clone: the index has the versions.

| Flag | Meaning |
|---|---|
| `--index` | the consumer indexes the full runs wrote (default `.pinup/consumers.json`); paths or patterns, merged |
| `--state` | advisories already reported; written back after the run |
| `--filter` | report only repositories matching these globs |
| `--control` | a repository with known-vulnerable dependencies that must yield findings, or the watch fails - the proof that the watch can fail |
| `--report` | write the report as JSON |

The runner follows a report with `run --project <repo> --package
<datasource:name>` per finding.

## shadow

Compare plan reports with the merge requests another tool (or a previous
pinup) has open, and fail when a difference persists from one run to the
next without a triaged suppression.

| Flag | Meaning |
|---|---|
| `--plans` | the plan reports to compare, a glob |
| `--prefix` | the other tool's branch prefix (default `renovate/`) |
| `--state` | the previous comparison's differences; a difference fails only when it persists |
| `--suppressions` | triaged only-pinup entries with reason, owner and expiry |
| `--controls` | projects that must yield exactly one only-pinup entry each (default `pinup/shadow-fixture`) |
| `--report` | write the comparison as JSON |

## notify

`pinup notify rolling-major|estate --plans 'reports/*.json' --project
<runner project>`: keep one issue in the runner project current - the
rolling-major notice (every `@N` component pin with a newer major
available) or the estate overview.

## token

`pinup token rotate`: renew the bot's own personal access token before
it expires and store the new one in the CI variables named
(`--variables`, `--also`), at the scope given. `--threshold-days`,
`--lifetime-days`, `--dry-run`. GitLab only.

## version, askpass

`version` prints the version. `askpass` is what git calls when
`GIT_ASKPASS` points at the binary: it answers the credential prompt for
the platform's host from the environment and for no other host. Nobody
types it.
