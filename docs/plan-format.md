<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# The plan

Every run produces a machine-readable plan before it writes anything:
`whatif` stops there, `run` continues. The plan is the backbone, not a
debug flag - the comparison job, the dashboard, the merge request body
and the estate report are all read off it. Two properties are invariant:

- **A plan explains why nothing happens.** Every update that is held
  carries its reason, when the hold lifts and which rule imposed it.
  Absence is never expressed as absence.
- **Nothing downstream of the plan invents a change.** Branches carry
  byte-range edits and tasks; apply consumes those and never a dependency.

The plan is JSON, `--report <path>`; several projects with a `%s` in the
path. `schemaVersion` is 1; the fields below are the ones a reader acts on.

## Top level

| Field | Meaning |
|---|---|
| `pinupVersion`, `generatedAt` | which pinup planned, when |
| `repo.path` | the project |
| `limits` | `prHourlyLimit`, `prConcurrentLimit` as resolved; the runner enforces them |
| `dashboard` | whether the dashboard issue is kept and its title |
| `branching` | `prefix` (`branchPrefix`) and, during a rename, `prefixOld` (`branchPrefixOld`); the runner counts and prunes under both |
| `deps` | every dependency extracted |
| `updates` | every update planned, held or not |
| `branches` | the branches the updates compose into |
| `warnings` | what could not be done and why, by stage |
| `stats` | counts and timings |

## deps

One entry per dependency per file: `manager`, `file`, `depName`,
`packageName`, `currentValue`, `currentDigest`, `lockedVersion`,
`datasource`, `versioning`, `registryUrls`, the `rangeStrategy`,
`minimumReleaseAge` and `pinDigests` the pre-lookup rules gave it, the
`locus` (byte offsets of the value and the digest in the file), and for a
custom manager its `customManager` index and the `captures` the pattern
made. A dependency that is not looked up says why in `skipReason`; one a
rule disabled says which rule in `disabled`.

## updates

One entry per move: `dep` (the dependency as above), `newValue` (the
bytes written), `newVersion`, `newDigest`, `updateType` (`major`,
`minor`, `patch`, `digest`, `pin`, `pinDigest`, `rollback`,
`lockFileMaintenance`, `majorAvailable`), `stream` on a `majorAvailable`
of a series pin (the sibling package of the higher series, `kubectl-1.37`
for a `kubectl-1.36` pin; `newValue` is its newest version, and the
update is held with `rollingMajor`: reported, never written), `declared`
and `effective` risk, the `analyzer` and its `evidence` where one ran
([effective classification](effective-classification.md)),
`releaseTime` with its `timeSource` (the datasource's timestamp or the
cache's first-seen record), `securityFix` with the advisory, and:

- `blocks`: every reason the update is held - `reason`, the `origin`
  (`config` or `packageRules[N]`), `until` where a hold thaws, `note`.
  Reasons: `minimumReleaseAge`, `schedule`, `dependencyDashboardApproval`,
  `disabled`, `allowedVersions`, `internalChecksFilter`, `hourlyLimit`,
  `concurrentLimit`, `rollingMajor`, `pluginRequired`, `taskRefused`,
  `nothingToRefresh`, `publishFailed`, `closedByHand` (the newest
  request on the branch was closed by a person without merging and
  carried exactly these edits; a changed update opens again).
- `suppressedBy`: the first reason, for readers that need one word.
- `notes`, `compareUrl`: the release notes between the versions and the
  forge's compare page, where the source is known.

## branches

One entry per branch: `name` (Renovate-compatible, so an estate switches
without duplicates; a branch whose request is open under `branchPrefixOld`
keeps that name and says in `plannedName` what it would be called under
the current prefix - a merge request cannot change its source branch, so
the old name lasts as long as the request), `title`, `commitBody` (the
configuration's, rendered), `groupName`, the `updateKeys` it carries,
`edits` (file, byte range, old and new bytes, manager), `tasks` (the
lock refreshes and post-upgrade commands with their scope), `automerge`,
`labels`, `schedule` (the window and when it next opens), `existing`
(the merge request already open, if any), and `suppressedBy` with
`heldWith` where the branch as a whole is held - a limit, a task the
allowlist refused, a lock found current, a push that failed.

## warnings

`stage` (`config`, `extract`, `lookup`, `analyze`, `apply`, `publish`,
`cache`), `file` where one applies, `msg`. A datasource that could not be
reached, a preset that resolves to nothing, a task whose output left its
scope, a cache too young to judge release ages: warnings, never a failed
run for the rest of the repository.

One of them is about a pin that is not being updated at all: a
`# renovate:` or `# pinup:` annotation that no dependency claims. The
annotation managers allow exactly one whitespace character between the
comment and the pinned line; a blank line or a comment in between does not
hold the update, it removes the dependency from the plan. The run warns per
annotation (`extract`, with file and line) instead of staying silent - the
shape found on three of fifty golden-image Containerfiles on 2026-09-17,
one of them a package with a fixed CVE waiting in the index.

## Reading a plan

```sh
# every held update with its reason
jq -r '.updates[] | select(.blocks) | "\(.dep.depName) \(.dep.currentValue) -> \(.newValue): \(.blocks[0].reason) (\(.blocks[0].origin.source))"' plan.json

# the branches that would be written now
jq -r '.branches[] | select(.suppressedBy == null) | .name' plan.json
```
