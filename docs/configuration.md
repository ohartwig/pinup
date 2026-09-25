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
| `minimumReleaseAge` | supported; for npm, a project's `.npmrc` `min-release-age` is a floor under it ([below](#a-projects-npmrc-release-age)) |
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

## A project's .npmrc release age

npm (from 11.10) refuses a version younger than an `.npmrc`'s
`min-release-age` days, except the packages `min-release-age-exclude`
names. pinup reads both from the `.npmrc` governing an npm manifest -
beside it or, for a workspace, the nearest one above it - and takes the
longer of that and its own `minimumReleaseAge`:

| pinup's rules | `.npmrc` | applies |
|---|---|---|
| 3 days | 7 | 7 days |
| 7 days | 7 | 7 days |
| 14 days | 7 | 14 days |
| any | 7, package excluded (`@scope/*`) | pinup's rules alone |

The floor is the one age both the candidate choice
(`internalChecksFilter`) and the hold use, and the hold names the file:
`origin` `file:<path>/.npmrc`, pointer `/min-release-age`, with `until`
the moment npm will install the version. It applies to security fixes
too, which otherwise carry no age - npm refuses the version whatever the
reason for proposing it. Lock-file maintenance stays exempt, as for any
age: npm applies its own policy while re-resolving ranges. Other holds -
approval, schedule, `allowedVersions`, `enabled: false` - are unchanged
and add up.

Why it matters: a version under the floor, pinned exactly, does not make
npm fail. npm 11.17 and 12.1 both resolve in a loop and never return, so
a lock refresh proposing one runs into its timeout (45 minutes in the
estate's runner) in every schedule window until the version ages.

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
