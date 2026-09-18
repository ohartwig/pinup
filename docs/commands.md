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
