<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Configuration

pinup reads Renovate's configuration language. A repository that has a
`renovate.json` keeps it; the switch from Renovate changes no byte in any
repository.

## Where it comes from

Three layers, resolved in this order:

1. **The run's configuration**, `--config`: a file, or a `local>` preset
   the platform serves (`local>group/runner`, `local>group/runner:release-fast`,
   `local>group/runner:default.yaml`). This is the estate's file: the
   rules, the custom managers, the presets. A name without an extension
   is `.json`; one that spells it (`.yaml`, `.yml`, `.jsonc`, `.json5`)
   is fetched and parsed as written - there is no probing.
2. **The repository's file** in the checkout, exactly one of
   `.pinup.yaml`, `.pinup.yml`, `.pinup.json`, `.pinup.jsonc`,
   `renovate.json`, `renovate.json5`, `.renovaterc`, `.renovaterc.json`.
   **Two of them is an error**, not a silent choice: the one that lost
   would be edited for weeks with no effect — which is also why that list
   is not a precedence. Nothing picks a winner, because there is never a
   contest; it is written YAML first because YAML is the spelling that
   takes comments. Its `extends` may name
   the run's file as `local><runner project>`; pinup answers that name from
   the file it was started with, without a fetch - and it keeps answering
   the project's old name after the file moved, so an estate migrates its
   runner without touching the repositories.
3. **Presets**: Renovate's built-in preset names (`config:recommended`,
   `:disableDependencyDashboard`, …) come from pinup's own preset library,
   written from observed behaviour, not from Renovate's tree. `local>`
   presets of other projects are fetched from the platform.

`PINUP_RUNNER_PROJECT` names the runner project when `--config` is a
plain file and the repositories extend it by name.

## How it merges

The semantics are Renovate's, measured against it:

- objects deep-merge, arrays replace, `null` clears a key;
- `packageRules` concatenate across every layer and apply in array order;
  for a dependency every matching rule applies, later rules winning per key;
- rules run twice, as in Renovate: once per dependency before lookup,
  where they can disable it or change its versioning and registries, and
  once per update, where `matchUpdateTypes` (and `matchEffective`) are
  known and a rule can hold it;
- a matcher over a field that is not known does not match: a
  `matchUpdateTypes` rule stays silent before lookup, a `matchEffective`
  rule before an analyzer ran.

Every resolved value keeps its whole origin chain. `pinup print-config
--config … --explain packageRules[25].automerge` prints every source that
set a path, winner last; a held update in the plan names the rule that
held it the same way.

## The keys

What `pinup migrate --config …` reports. Supported means the key does
what it does in Renovate, measured; partial names the difference;
unsupported means nothing reads it and the run says so.

| Key | Status |
|---|---|
| `$schema` | supported |
| `addLabels` | supported |
| `additionalBranchPrefix` | supported |
| `allowedCommands` | supported |
| `allowedVersions` | supported |
| `analyze` | supported (pinup's own) |
| `automerge` | supported |
| `automergeDirect` | pinup's own; default on, see [automerge](#automerge) |
| `branchPrefix` | supported |
| `branchPrefixOld` | supported: a request still open under the old prefix is adopted under its own name, not opened again |
| `branchTopic` | supported |
| `commitMessageAction` | supported |
| `commitMessageExtra` | supported |
| `commitMessageLowerCase` | supported |
| `commitMessagePrefix` | supported |
| `commitMessageSuffix` | supported |
| `commitMessageTopic` | supported |
| `customDatasources` | supported |
| `customManagers` | supported |
| `dependencyDashboard` | supported |
| `dependencyDashboardApproval` | supported |
| `dependencyDashboardTitle` | supported: names the dashboard issue; unset, the issue is "pinup Dashboard" (Renovate's builtin "Dependency Dashboard" does not count as set) |
| `description` | supported |
| `enabled` | supported |
| `enabledManagers` | supported |
| `extends` | supported |
| `extractVersion` | supported |
| `fetchChangeLogs` | supported |
| `groupName` | supported |
| `groupSlug` | supported |
| `ignoreDeps` | supported |
| `ignorePaths` | supported |
| `ignoreUnstable` | supported |
| `internalChecksFilter` | supported |
| `labels` | supported |
| `lockFileMaintenance` | supported |
| `major`, `minor`, `patch`, `pin`, `digest`, `pinDigest`, `rollback`, `replacement` | supported: the per-update-type objects, overlaid on the resolved rules for an update of that type |
| `matchCurrentValue` | supported |
| `matchDatasources` | supported |
| `matchDepNames` | supported |
| `matchDepTypes` | supported |
| `matchEffective` | supported (pinup's own) |
| `matchFileNames` | supported |
| `matchManagers` | supported |
| `matchPackageNames` | supported |
| `matchUpdateTypes` | supported |
| `minimumReleaseAge` | supported; for npm, a release age the project sets for npm, pnpm or yarn is a floor under it ([below](#a-projects-own-release-age)) |
| `minimumReleaseAgeBehaviour` | supported |
| `osvVulnerabilityAlerts` | supported |
| `packageRules` | supported |
| `pinDigests` | supported |
| `platformAutomerge` | supported |
| `postUpgradeTasks` | supported |
| `prBodyDefinitions` | supported |
| `prBodyNotes` | supported |
| `prConcurrentLimit` | supported; a branch that fixes an advisory (`vulnerabilityAlerts`) is not held by it, and requests labelled with `prConcurrentLimitIgnoreLabels` are not counted |
| `prConcurrentLimitIgnoreLabels` | pinup's own: labels whose open requests hold no slot of `prConcurrentLimit` - requests that wait for a person by design, such as a cluster minor behind a runbook |
| `prHourlyLimit` | supported; a branch that fixes an advisory is not held by it |
| `rangeStrategy` | supported |
| `registryUrls` | supported |
| `schedule` | supported |
| `semanticCommitScope` | supported |
| `semanticCommitType` | supported |
| `semanticCommits` | supported |
| `separateMajorMinor` | supported |
| `timezone` | supported |
| `trustEffective` | supported (pinup's own) |
| `versioning` | supported |
| `vulnerabilityAlerts` | supported |
| `commitBody` | supported: rendered per branch with the update's variables (`updateType`, `depName`, …) |
| `executionTimeout` | partial: the runner's `PINUP_EXECUTION_TIMEOUT` decides, never a repository |
| `matchCurrentVersion` | partial: matched as a version, not as a range |
| `matchJsonata` | partial: `isLockfileUpdate` and `sourceUrl` only, what the reference estate uses |
| `matchSourceUrls` | partial: the URL as the datasource reported it |
| `postUpdateOptions` | partial: `gomodTidy` is what the gomod lock refresh does anyway; the others are unread |
| `prCreation` | partial: `immediate` only; the not-pending deadlock is gone by design |
| `rebaseWhen` | partial: a branch with foreign commits is left alone; otherwise rebased on conflict |
| `separateMinorPatch` | partial: read into the template variables; `separateMajorMinor` decides |
| `separateMultipleMajor` | partial: read into the template variables; `separateMajorMinor` decides |
| `separateMultipleMinor` | partial: read into the template variables; `separateMajorMinor` decides |
| `matchCategories` | unsupported: no category model; the rule never fires and `migrate` says so |

A key not in this table is not read; `migrate` lists it as unsupported.

## pinup's own keys

| Key | Where | Meaning |
|---|---|---|
| `analyze` | a rule | ask an analyzer what actually changed for this dependency's updates; off by default, since it fetches both versions ([effective classification](effective-classification.md)) |
| `matchEffective` | a rule | match the analyzer's label: `patch`, `minor`, `major`, `breaking-values`; never fires while the label is unknown |
| `trustEffective` | a rule | let an automerge a `matchEffective` rule switched on stand although the declared label is stricter; without it the stricter label wins |

## Automerge

`automerge: true` asks the platform to merge the request once its checks
pass. On GitLab that is one call to the merge endpoint with
`merge_when_pipeline_succeeds`, and pinup makes it on the run after the one
that opened the request — the request needs a pipeline before GitLab will
arm anything.

**`automergeDirect`** (default `true`) decides what happens when that call is
accepted and arms nothing. GitLab 19.4-ee does exactly that: it answers 2xx,
`merge_when_pipeline_succeeds` stays `false`, and the request sits there.
Measured on `devops/koh-gitops!2808` (2026-09-22) — reported as automerging,
open for 125 minutes with a green pipeline, and finally merged outright by a
later run. With `automergeDirect`, pinup instead merges it there and then,
but only where GitLab itself reports `detailed_merge_status: mergeable`: with
the pipeline already finished, arming would have had nothing left to wait
for.

Turn it off — `"automergeDirect": false`, globally or in a `packageRules`
entry — to keep the older behaviour: the request stays open for a later run,
and the run reports that it was not armed rather than claiming an automerge
that did not happen. Turning it off never turns automerge *on*; it only
changes how one that is already allowed is carried out.

`automerge: false` remains the way to stop pinup merging at all.

## A project's own release age

npm, pnpm and yarn can each be told to refuse a version younger than a
given age, except the packages the setting exempts. pinup reads every
such setting governing an npm manifest - in its directory or, for a
workspace member, the nearest file above it - and takes the longest that
does not exempt the package, if it is longer than its own
`minimumReleaseAge`:

| Tool | File | Setting | Unit | Exemptions |
|---|---|---|---|---|
| npm ≥ 11.10 | `.npmrc` | `min-release-age` | days | `min-release-age-exclude`: names, patterns; comma lists split |
| pnpm 10 (measured 10.34) | `.npmrc` | `minimum-release-age` | minutes | `minimum-release-age-exclude`: one entry per line |
| pnpm 10 and 11 (measured 10.34, 11.27) | `pnpm-workspace.yaml` | `minimumReleaseAge` | minutes | `minimumReleaseAgeExclude`: names, patterns, `name@1.2.3 \|\| 1.2.4` |
| yarn berry (measured 4.14) | `.yarnrc.yml` | `npmMinimalAgeGate` | minutes, or `h`/`d`/`w` | `npmPreapprovedPackages`: names, patterns, `name@range` |

| pinup's rules | project | applies |
|---|---|---|
| 3 days | 7 days | 7 days |
| 7 days | 7 days | 7 days |
| 14 days | 7 days | 14 days |
| 3 days | `.npmrc` 6 days, `pnpm-workspace.yaml` 8 days | 8 days |
| any | 7 days, package exempt | pinup's rules alone |

Every setting counts, whichever package manager the project actually
uses: what the repository wrote down is what it asked for, and holding a
release a little longer than one tool would is the direction that cannot
break a lock refresh. An exemption narrowed to versions covers those
versions only. Where no single version is in question yet - choosing the
candidate under `internalChecksFilter` - it covers none.

The age is the one both the candidate choice and the hold use, and the
hold names the file: `origin` `file:<path>`, pointer the setting
(`/min-release-age`, `/minimum-release-age`, `/minimumReleaseAge`,
`/npmMinimalAgeGate`), with `until` the moment the package manager will
install the version. It applies to security fixes too, which otherwise
carry no age - the package manager refuses the version whatever the
reason for proposing it. Lock-file maintenance stays exempt, as for any
age: the package manager applies its own policy while re-resolving
ranges. Other holds - approval, schedule, `allowedVersions`,
`enabled: false` - are unchanged and add up. A `pnpm-workspace.yaml` or
`.yarnrc.yml` whose age pinup cannot read is a plan warning, and no
floor.

Why it matters - measured on 2026-09-24 with a version 5.8 days old,
pinned exactly, under a seven-day age:

- **npm** 11.17 and 12.1 fail with `ETARGET` - unless another dependency
  peers on the pinned package, as every eslint config does. Then they
  resolve in a loop and never return
  ([npm/cli#9891](https://github.com/npm/cli/issues/9891)), and a lock
  refresh proposing such a version runs into its timeout (45 minutes in
  the estate's runner) in every schedule window until the version ages.
- **pnpm** 10.34 and 11.27 fail within two seconds with
  `ERR_PNPM_NO_MATURE_MATCHING_VERSION`.
- **yarn** 4.14 fails at once with `YN0016` ("quarantined").

Failing fast is kinder than looping, but either way the branch lands
nowhere. None of the three has an age by default. pnpm 11 reads its age
from `pnpm-workspace.yaml` only, not from `.npmrc` or package.json's
`pnpm` field. yarn classic (1.x), the only yarn pinup refreshes locks
with, has no age at all; a `.yarnrc.yml` age is honoured as the
project's stated wish.

## Custom managers

`customManagers` with `customType: regex` work as in Renovate: RE2
regular expressions with named groups (`depName`, `currentValue`,
`currentDigest`, `datasource`, `versioning`, `registryUrl`,
`packageName`, `extractVersion`), the `*Template` keys rendered with a
Handlebars subset (`{{{x}}}`, `{{#if}}`/`{{else}}`, `{{#if (equals a
"b")}}`), `matchStringsStrategy` `any` and `recursive` (`combination` is
refused, not ignored). A captured group beats its template.
`# renovate:` and `# pinup:` annotations are both read.

`customDatasources` with `defaultRegistryUrlTemplate`, `format` and
`transformTemplates` (a JSONata subset) work as in Renovate; the
platform token is never sent to a URL a repository configuration names
([security](security.md)).

## Environment

| Variable | Meaning |
|---|---|
| `PINUP_PLATFORM` | `gitlab` (default) or `github` |
| `PINUP_GITLAB_URL` / `CI_SERVER_URL` | the GitLab instance |
| `PINUP_GITLAB_TOKEN` / `GITLAB_TOKEN` | a personal access token (`api`, `write_repository`); `CI_JOB_TOKEN` when none is set (read-only) |
| `PINUP_GITHUB_URL` / `GITHUB_SERVER_URL` | the GitHub host; `github.com` when unset |
| `PINUP_GITHUB_TOKEN` / `GITHUB_TOKEN` | a token with `repo`, or a fine-grained one with contents, pull requests and issues read/write |
| `PINUP_REGISTRY_HOST` / `CI_REGISTRY` | the estate's container registry; the platform token is exchanged for a pull token there and nowhere else |
| `GITHUB_COM_TOKEN` | for `github-*` lookups and release notes, bound to `api.github.com` |
| `PINUP_GIT_NAME`, `PINUP_GIT_EMAIL` | who commits |
| `PINUP_SIGNING_FORMAT`, `PINUP_SIGNING_KEY` | `openpgp` or `ssh` and the key; unsigned only with the explicit word `none` |
| `PINUP_CACHE` | the lookup cache (bbolt) |
| `PINUP_ALLOWED_COMMANDS` | JSON array of anchored patterns a `postUpgradeTasks` command must match; the runner's decision, never a repository's |
| `PINUP_PLUGIN_ENV` | variables a task may see besides `PATH`, `LANG`, `TZ` |
| `PINUP_TASK_NETRC` | a `.netrc` written into each task's scratch `HOME` for a toolchain that fetches private modules; never a variable |
| `PINUP_EXECUTION_TIMEOUT` | minutes per task |
| `PINUP_RUNNER_PROJECT` | the project repositories extend the runner configuration from |
| `PINUP_DASHBOARD_TITLE` | the operator's override of `dependencyDashboardTitle`; empty means the configuration names the issue |
| `PINUP_APK_VIEWS` | apk indexes served natively besides the public Wolfi repository: `{"custom.<name>": {"mirrors": [...], "arches": [...]}}` |
