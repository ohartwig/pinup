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
| `allowedVersions` | supported: a range in the scheme's own syntax, else - for every scheme but npm, as in Renovate - an npm range (`>=8.4.0 <8.5.0`) against the version coerced to x.y.z; `/regex/` and `!/regex/`. A dependency whose every newer release it excludes is `held by allowedVersions`, naming the rule and the newest release kept out - not `up to date` |
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
| `extractVersion` | supported, from a custom manager and from a packageRule (the rule's value wins) |
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
| `osvVulnerabilityAlerts` | supported; composer packages are also checked against Packagist's security advisories (the list `composer audit` reads), which carry a TYPO3-CORE-SA days to weeks before OSV. A Packagist advisory OSV already carries under its CVE or GHSA id is not listed twice; one OSV lacks takes the same path as an OSV finding (`vulnerabilityAlerts`, its labels, no soak, KEV by its CVE). Its fix is the lowest release the advisory's range no longer covers. Packagist that cannot be reached is a warning, never a failed run; `osvTransitiveAlerts` and the advisory watch use it the same way |
| `osvTransitiveAlerts` | pinup's own, off by default: also asks OSV about every package a composer, npm, pnpm or yarn lock pins that no manifest names. A finding makes that lock's refresh a security fix - planned even with `lockFileMaintenance` off, and taking `enabled`, `schedule`, `minimumReleaseAge`, `labels`, `automerge`, `prCreation`, `dependencyDashboardApproval` and `commitMessageSuffix` from `vulnerabilityAlerts` while keeping the maintenance branch. Each finding is also a plan warning, every run while it lasts: a fix outside the constraints of whatever requires the package is out of the refresh's reach. Nested npm copies (`node_modules/a/node_modules/b`) are not read |
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
| `overrideDatasource` | supported in packageRules: the dependency is looked up at that datasource, without the manager's registry URLs |
| `overridePackageName` | partial: a literal name; Renovate's templates are not expanded |
| `registryUrls` | supported |
| `schedule` | supported |
| `semanticCommitScope` | supported |
| `semanticCommitType` | supported |
| `semanticCommits` | supported |
| `separateMajorMinor` | supported |
| `timezone` | supported |
| `trustEffective` | supported (pinup's own) |
| `versioning` | supported |
| `vulnerabilityAlerts` | supported; a fix for a CVE CISA lists as exploited goes first and waits for no release age or approval - see [Known exploited vulnerabilities](#known-exploited-vulnerabilities) |
| `commitBody` | supported: rendered per branch with the update's variables (`updateType`, `depName`, …) |
| `executionTimeout` | partial: the runner's `PINUP_EXECUTION_TIMEOUT` decides, never a repository |
| `matchCurrentVersion` | partial: matched as a version, not as a range |
| `matchJsonata` | partial: `isLockfileUpdate` and `sourceUrl` only, what the reference estate uses |
| `matchSourceUrls` | partial: the URL as the datasource reported it |
| `postUpdateOptions` | partial: `gomodTidy` is what the gomod lock refresh does anyway; the others are unread |
| `prCreation` | partial: `immediate` only; the not-pending deadlock is gone by design |
| `rebaseWhen` | partial: a branch with foreign commits is left alone. Otherwise a branch is rebuilt on the current base when the base moved, except while its pipeline is still queued or running and it merges cleanly - and, with `conflicted`, whenever it merges cleanly, its pipeline finished or not. Either way a changed edit, a conflict or the dashboard's rebase box pushes. `never` is not honoured. After every push automerge is armed again, since GitLab cancels it on a push |
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
| `osvEcosystem` | `customDatasources.<name>` | the [OSV ecosystem](https://ossf.github.io/osv-schema/#appendix-ecosystems) this custom datasource's packages belong to, e.g. `"Wolfi"`; with it, `osvVulnerabilityAlerts` asks OSV about them ([advisory coverage](#advisory-coverage)) |
| `osvPackage` | a rule | `{"ecosystem": "Go", "name": "github.com/aquasecurity/trivy"}`: ask OSV about the matched dependencies as this package, for a pin whose datasource has no advisory ecosystem ([advisory coverage](#mapping-a-pin-to-an-osv-package)) |

### pinup's own keys and Renovate

Renovate refuses a configuration that carries a key it does not know - not
the key, the whole file. Measured with `renovate-config-validator` 43.204.0
(2026-10-06): every key in the table above, and `osvTransitiveAlerts`,
`automergeDirect` and `prConcurrentLimitIgnoreLabels`, is an *"Invalid
configuration option"*; `osvEcosystem` inside a custom datasource is *"key
is not allowed"*.

So a pinup-only key belongs in a file Renovate never reads:

- the repository's **`.pinup.*`** file (`pinup migrate --to yaml` writes one), or
- the **run's own configuration** (`--config`), the estate's shared preset that
  only pinup runs against.

A `renovate.json` keeps Renovate's keys only. That is what keeps a shadow run
working - Renovate and pinup side by side against the same repository while
an estate moves - and what lets a repository go back. `pinup advise` names
each pinup-only key such a file carries (`compat/renovate-rejects`), with its
pointer. An estate that runs pinup alone may keep pinup's keys in its shared
preset; Renovate never reads it there.

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

## Known exploited vulnerabilities

With `osvVulnerabilityAlerts` on, every advisory OSV reports is also checked
against CISA's [Known Exploited Vulnerabilities](https://www.cisa.gov/known-exploited-vulnerabilities-catalog)
catalog by its CVE id and aliases. The catalog is read only when a run has an
advisory to check, and kept in the cache for a day; when CISA cannot be
reached a cached copy older than that is used, with a warning.

A security fix for a CVE in the catalog:

- is the first branch of the plan, so the run creates its merge request before
  any routine update;
- is held by no `minimumReleaseAge` and no `dependencyDashboardApproval`,
  whatever `vulnerabilityAlerts` or the rules say - like every security fix it
  already ignores `prConcurrentLimit` and `prHourlyLimit`;
- carries the label `security:kev` beside the configured ones;
- is listed in the dashboard's *Known exploited vulnerabilities* section with
  CISA's dates and where its merge request stands, and named at the top of the
  merge request.

A fix that is only available as a major update is opened at once like any
other, and says so in both places; whether it automerges is still
`vulnerabilityAlerts.automerge`. A dependency whose exploited CVE has no fix
release is listed in the same section with the reason.

Two things the catalog does not override: a project's own release age (the
next section), because the package manager would refuse the fix, and the
schedule, which `vulnerabilityAlerts.schedule` decides.

## Advisory coverage

The sources, how their answers merge and why there is more than one:
[advisory sources](advisory-sources.md).

`osvVulnerabilityAlerts` asks OSV about the dependencies whose datasource has
an OSV ecosystem: npm, packagist, go, pypi, maven, crate, rubygems and nuget.
A custom datasource has none of its own - a URL template says nothing about
what it serves - unless its definition names one:

```json
"customDatasources": {
  "wolfi": { "defaultRegistryUrlTemplate": "…", "osvEcosystem": "Wolfi" }
}
```

A named ecosystem is asked only for dependencies written in its versioning:
`Wolfi` for `versioning=apk` pins. A custom datasource that also resolves
other values - composer's php platform entries resolved through the same
datasource are semver - leaves those unasked. Findings behave like any
other OSV finding: the `vulnerabilityAlerts` branch, no release age, CISA's
catalog. The advisory watch asks the same, from the ecosystem the run wrote
into the consumer index.

Every dependency the run checks lands in one of three states, written on it
in the plan ([`advisoryCoverage`](plan-format.md#advisorycoverage)): an
advisory source was asked; only its publisher's withdrawal list speaks for
it (an apk mirror's `withdrawn.json`, the installation's
`PINUP_WITHDRAWN_IMAGES` list); or nothing does, with the reason. The
dashboard states both gaps under *Detected dependencies*, and the watch in
its output - "covered by a withdrawal list only: 12 (custom.koh-apk 12); not
covered by advisories: 30 (docker 30)" - so that no advisory is never read
as checked.

An older pinup ignores `osvEcosystem`: `customDatasources` reads only the
keys it knows, so a configuration can carry it before the runner does.

### Mapping a pin to an OSV package

A binary released on GitHub, a terraform provider, a tool pinned by tag:
their datasource has no advisory ecosystem, yet the software is often a
package OSV knows. A rule says so, explicitly:

```json
"packageRules": [{
  "matchDepNames": ["aquasecurity/trivy"],
  "osvPackage": {"ecosystem": "Go", "name": "github.com/aquasecurity/trivy"}
}]
```

The matched dependencies are asked in that ecosystem under that name - trivy
0.50.0 has 8 advisories there (checked 2026-10-06) - and take OSV's path like
any other finding; the plan names the rule (`osvPackageBy`). pinup never
guesses a mapping from a repository name: a pin no rule maps stays
`no-ecosystem`. An explicit mapping is asked in its ecosystem even where the
datasource has one of its own. `osvPackage` is pinup's own key: it belongs in
a `.pinup.*` file or the run's own configuration
([pinup's own keys and Renovate](#pinups-own-keys-and-renovate)).

### Private advisories

No public advisory database carries an installation's own artefacts - a
composer package on its GitLab registry, a CI component, a project tagged
on its instance. `PINUP_PRIVATE_ADVISORIES` names a feed of their
advisories in the [OSV schema](https://ossf.github.io/osv-schema/), and pinup
answers it like OSV: a finding takes the `vulnerabilityAlerts` path, its
labels, no release age, CISA's catalog through the record's CVE alias; an
advisory OSV or Packagist already carries under one of its ids is not
listed twice. With a feed, gitlab-packages, gitlab-tags and gitlab-releases
dependencies are asked and count as covered (`sources: ["private"]`).

It is an installation setting, not a configuration key: the runs and the
advisory watch - which reads no configuration - ask the same feed, and no
repository's file carries a key Renovate would refuse. A feed on the
instance's package registry is read with the platform token.

A record names its package by purl, the form a CSAF product tree carries,
or by OSV ecosystem and name; a dependency is asked as:

| Datasource | purl | Ecosystem, name |
|---|---|---|
| `packagist`, `gitlab-packages` | `pkg:composer/<vendor>/<name>` | `Packagist`, `<vendor>/<name>` |
| `gitlab-tags`, `gitlab-releases` | `pkg:gitlab/<group>/<project>` | `GitLab`, `<group>/<project>` |
| `npm` | `pkg:npm/<name>` (`@` as `%40`) | `npm`, `<name>` |
| `go` | `pkg:golang/<module>` | `Go`, `<module>` |
| `pypi` | `pkg:pypi/<name>` | `PyPI`, `<name>` |

A gitlab-packages dependency's package name is `<project>:<vendor>/<name>`;
the purl names the composer package, so `pkg:composer/koh/sylius-passkey`
matches it on whichever project's registry it lives. Versions are compared
under the dependency's versioning with OSV's range walk (`SEMVER` and
`ECOSYSTEM` ranges, `versions` lists). A source that cannot be read is a
warning; the others still count. The withdrawal lists stay: they take a
version out of the releases, which an advisory does not.

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
| `PINUP_EXECUTION_TIMEOUT` | minutes per task, counted from the moment it gets its slot |
| `PINUP_REPOSITORY_CONCURRENCY` | repositories a run works on at once (default 4) |
| `PINUP_TASK_CONCURRENCY` | tasks - lock refreshes, `postUpgradeTasks` - that run at once across all of them (default 2) |
| `PINUP_RUNNER_PROJECT` | the project repositories extend the runner configuration from |
| `PINUP_DASHBOARD_TITLE` | the operator's override of `dependencyDashboardTitle`; empty means the configuration names the issue |
| `PINUP_APK_VIEWS` | apk indexes served natively besides the public Wolfi repository: `{"custom.<name>": {"mirrors": [...], "arches": [...]}}`; each mirror's `withdrawn.json` is read too (see managers-and-datasources.md) |
| `PINUP_WITHDRAWN_IMAGES` | https URL of the installation's `withdrawn-images.json`: withdrawn image tags leave the docker releases, and a dependency on one is moved to the replacement as a security fix (see managers-and-datasources.md) |
| `PINUP_PRIVATE_ADVISORIES` | the installation's own advisory feed in the OSV schema: comma-separated https URLs of an OSV export (`.zip`) or a `.json` document, or `file://` directories of records; runs and the advisory watch read it ([private advisories](#private-advisories)) |
