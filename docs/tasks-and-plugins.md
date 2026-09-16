<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: MIT
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
| npm | `npm install --package-lock-only --no-audit --ignore-scripts` | `package-lock.json`, `npm-shrinkwrap.json` |
| npm (yarn classic) | `yarn install --ignore-scripts --ignore-engines --ignore-platform --non-interactive` | `yarn.lock` |
| gomod | `go mod tidy` | `go.mod`, `go.sum` |

A machine without the toolchain holds the branch with `pluginRequired`
and says which tool; a maintenance for a lock no plugin refreshes (a
terraform lock) is held the same way; a refresh that changes nothing is
`nothingToRefresh` and opens nothing.

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
One the allowlist does not admit holds the branch with `taskRefused`
naming the command; Renovate skipped the command and pushed the branch
anyway, which is how an estate lost every first-party lock refresh for
weeks without a red job.

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

`PINUP_EXECUTION_TIMEOUT` (minutes) bounds each task.

## Where the tools come from

Tasks run the toolchain on the job image's `PATH`: the runner's toolchain
image carries composer, npm and go, and the commands run in-process,
without a shell (`a || b` hands composer a package named `||`). A
container flavour of the same contract, a digest-pinned image per task,
is what the specification describes and what a runner with Docker-in-
Docker would use; the exec flavour is the one proven in production.
