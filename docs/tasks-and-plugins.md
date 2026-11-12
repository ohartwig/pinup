<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Tasks and plugins

pinup's core decides; a task applies. A task is a command run on the
branch's checkout after the edits are written and before the commit: a
lock refresh the manager knows, or a command a rule asked for
(`postUpgradeTasks`). What it changes is committed with the edits, in
one commit, so a manifest never lands without its lock.

## Lock refreshes

Planned by the manager whose file changed, one per lock file, with the
package names the branch moves (or the whole lock for
`lockFileMaintenance` and for a workspace's shared lock):

| Manager | Command | Scope |
|---|---|---|
| composer | `composer update <names> --with-all-dependencies --no-plugins --no-install --no-scripts --no-audit --ignore-platform-reqs` (without names for maintenance) | `composer.lock` |
| npm | `npm install --package-lock-only --no-audit --ignore-scripts --allow-remote=all` after the bumped packages are pruned from the lock (`pruneLock`; `npm update …` for maintenance; the flag lets arborist extract bundled packages under a repository `allow-remote=root`, see plugin.go) | `package-lock.json`, `npm-shrinkwrap.json` |
| npm (yarn classic) | `yarn install --ignore-scripts --ignore-engines --ignore-platform --non-interactive` (`yarn upgrade …` for maintenance) | `yarn.lock` |
| npm (pnpm) | `pnpm install --lockfile-only --ignore-scripts --pm-on-fail=warn` (`pnpm update …` for maintenance), at the workspace root | `pnpm-lock.yaml` |
| gomod | `go mod tidy` | `go.mod`, `go.sum` |

`--pm-on-fail=warn` keeps pnpm from fetching a pnpm of its own: a
`packageManager` field naming another version makes pnpm 11 download and
run that one, a tool entering the job past the image's pinned
provenance. The image's pnpm writes the lock; the task's output records
that the versions differ. Measured on a pnpm 11 workspace, the image's
11.8.0 wrote the lock byte-identical to the 11.27.0 the project named.

A machine without the toolchain holds the branch with `pluginRequired`
and says which tool; a maintenance for a lock no plugin refreshes (a
terraform lock) is held the same way; a refresh that changes nothing is
`nothingToRefresh` and opens nothing.

A manifest governed by a lock pinup can neither read nor refresh -
`bun.lock`, `bun.lockb`, beside it or at a workspace root above it -
holds its branch with `pluginRequired` and names the lock. Pushing the
manifest alone would leave the lock behind it, and the next frozen
install fails; a manifest with no lock at all is a manifest edit and
nothing more.

## Post-upgrade commands

```jsonc
"packageRules": [{
  "matchDatasources": ["git-refs"],
  "postUpgradeTasks": {
    "commands": ["node tools/update-expected-commit.mjs {{{packageFile}}}"],
    "fileFilters": ["*.yaml"],
    "executionMode": "update"
  }
}]
```

Templates in the command render with the update's variables
(`depName`, `currentValue`, `newValue`, `packageFile`, …). `executionMode`
`update` runs once per update, `branch` once per branch with every
update of the rule.

The command must match one of the runner's `allowedCommands`
(`PINUP_ALLOWED_COMMANDS`, anchored patterns, never a repository's
setting) - matched against the *compiled* command, after templating.
The allowlist governs these commands and nothing else: the lock
refreshes above are commands pinup composes itself, not text a
configuration brings, and are not matched against it. A pattern added
"for" a lock refresh admits nothing and reads as a control.
One the allowlist does not admit holds the branch with `taskRefused`
naming the command; Renovate skipped the command and pushed the branch
anyway, which is how an estate lost every first-party lock refresh for
weeks without a red job.

## Dropping a bump its tasks left unchanged

```jsonc
"packageRules": [{
  "matchDatasources": ["custom.koh-snapshot", "custom.tumbleweed-snapshot"],
  "groupName": "snapshots",
  "postUpgradeTasks": {
    "commands": ["koh-lock {{{packageFile}}}"],
    "fileFilters": ["**/koh-lock/**"],
    "executionMode": "branch",
    "dropWhenUnchanged": true
  }
}]
```

A pin that names a whole repository snapshot moves in every image with
each new snapshot, while only the images whose packages moved need a
build. With `dropWhenUnchanged: true` (a JSON `true`; anything else is
off) the branch is dropped - no commit, no push, no merge request -
when, after every task of the branch ran successfully:

- no task changed a file - the asking tasks' scope is their
  `fileFilters`, and a lock refresh beside them that moved its lock is a
  change as well; and
- a tracked file within the `fileFilters` of an asking task exists in
  the branch's tree.

The second condition is what keeps a repository that has not adopted the
lock as it was: `koh-lock` writes nothing where an image has no
`koh-lock/` yet, nothing lies in the scope, and the bump is written as
before.

The decision is made for the branch, never for one update on it: in
`branch` mode one run covers every package file, and the branch is
dropped only if no file in scope changed anywhere; in `update` mode the
same holds across all of the branch's tasks. It applies only when every
update on the branch asked for it - an update whose own configuration
did not would otherwise be dropped with the rest - so a group that
mixes such updates with others is written as usual.

A dropped branch is held with `scopeUnchanged` on the branch and on each
of its updates, the note naming the commands, the scope and how many
files in it were found unchanged; the run's log line reads `held` with
the same note, and the dependency dashboard lists it under *Held*. The
reason is not settled: a merge request already open for the branch - an
earlier snapshot that did move the lock - is left exactly as it is, not
updated with a pin-only commit and not closed by pruning. A task that
fails is a failure (`publishFailed`), never a drop. `pinup whatif` runs
no task and cannot know; the drop shows in the plan of a run.

## The scope

A task runs in a scratch `HOME`, with `PATH`, `LANG`, `TZ` and what
`PINUP_PLUGIN_ENV` names, and nothing else - no platform token, no
signing key. A toolchain that has to fetch a private module reads a
`.netrc` written into that `HOME` from `PINUP_TASK_NETRC`, a read-only
credential that is never handed over as a variable.

What the task changed is compared against its `fileFilters` (the lock
file for a refresh, the rule's list for a command). A change outside the
scope fails the branch by name and discards everything the task did: a
task is not a way to write arbitrary files into a repository. (A
repository that commits `node_modules/` fails its npm refresh this way,
correctly: the refresh writes `node_modules/.package-lock.json`.)

`PINUP_EXECUTION_TIMEOUT` (minutes) bounds each task. `PINUP_TASK_CONCURRENCY`
(default 2) bounds how many run at once across every repository of the
run, whatever `PINUP_REPOSITORY_CONCURRENCY` lets work side by side: a
repository is mostly network wait, a lock refresh resolves a whole
dependency graph in memory, and eight of them on one worker thrashed it
(2026-09-23). A task waiting for its slot is not yet on its timeout.

## Where the tools come from

Tasks run the toolchain on the job image's `PATH`: the runner's toolchain
image carries composer, npm, yarn, pnpm and go, and the commands run
in-process, without a shell (`a || b` hands composer a package named
`||`). Every task runs in the sandbox described in
[security.md](security.md): its own checkout and scratch home visible,
the job's secrets, the other checkouts and pinup's own state not, and
each package manager's cache its repository's alone. A
container flavour of the same contract, a digest-pinned image per task,
is what the specification describes and what a runner with Docker-in-
Docker would use; the exec flavour is the one proven in production.
