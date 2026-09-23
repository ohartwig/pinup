<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Security

A dependency bot holds a credential that can write to every repository
it scans and reads configuration from all of them. These are the lines
pinup holds, and what a repository can and cannot make the bot do.

## Invariants

- **Every run produces a plan before any write.** What will change is
  known, in bytes, before the first byte changes.
- **Apply consumes byte-range edits, never dependencies.** Nothing
  downstream of the plan can invent a change; YAML and JSON are never
  re-serialised.
- **Core decides, plugins apply.** A task regenerates a lock or runs a
  command on a checkout it was handed, in a scope it must stay in; one
  path outside the scope discards its whole result.
- **The stricter of two labels wins.** An analyzer's verdict relaxes an
  automerge only where a rule says `trustEffective: true`.
- **Plugins get no credentials.** A task sees `PATH`, `LANG`, `TZ` and what
  the runner's `PINUP_PLUGIN_ENV` names; never the platform token, never
  the signing key. A read-only `.netrc` for private modules is written into
  the task's scratch `HOME`, not handed over as a variable.

  The platform credentials are kept out twice: by **name** (`Blocked`) and
  by **value** (`Secrets`). A task whose environment or `.netrc` would carry
  the value of `PINUP_GITLAB_TOKEN`, `GITLAB_TOKEN`, `CI_JOB_TOKEN` or
  `PINUP_GPG_PRIVATE_KEY` under any other name is refused before it starts,
  and the refusal names the variable, never the value. The name check alone
  did not hold: until 2026-09-23 the estate's runner built `COMPOSER_AUTH`
  and `NPM_TOKEN` from the platform token and put both on
  `PINUP_PLUGIN_ENV`, so every task held the estate's write token under two
  names the filter did not know.

  Said precisely, because the filtered environment on its own does not
  carry the claim: a task runs as the same user, in the same process tree.
  Unisolated, on Linux, it can read the parent's `/proc/<pid>/environ`,
  open any file the job owns (the imported signing key and the gpg-agent
  socket beside it among them), attach to the parent where
  `kernel.yama.ptrace_scope` is 0 - as measured on the estate's runners -
  and write into the checkouts of the other repositories the job is
  working on at the same time, moments before pinup commits them under its
  own signature. The filter alone is hygiene.

  That matters because of what the allowlist admits. A package manager it
  names is hardened so it cannot run code out of the checkout
  (`--no-scripts`, `--no-plugins`, `--ignore-scripts`, appended before the
  allowlist is consulted), and the git configuration that makes git execute
  a program - `core.hooksPath`, `core.fsmonitor`, `gpg.program` - is pinned
  on every invocation, with the `.git` control surface fingerprinted around
  every task. But an entry of the form `node scripts/<file>.mjs` runs code
  **from the scanned repository** by design: whoever can write to that
  repository runs code as the task.

  So every task runs in a **sandbox** (package `sandbox`). pinup re-executes
  itself as a shim in a new user and mount namespace; the shim binds an
  empty directory over everything the task must not see, binds back what
  it was handed, drops every capability, sets `no_new_privs`, and only then
  executes the task under the job's own uid:

  | Hidden | Why |
  |---|---|
  | the temporary directory | the other checkouts live there, and in the estate's runner the imported key |
  | the job's `HOME`, `GNUPGHOME`, `SSH_AUTH_SOCK`, `XDG_RUNTIME_DIR`, `/run/user/<uid>` | keys, agent sockets, credentials wherever the job keeps them |
  | pinup's lookup cache and consumer index | the first-seen records decide `minimumReleaseAge`; the index steers the fast lane |
  | the parent's `environ`, `ptrace` | closed by the namespace boundary itself |

  Kept visible: the task's own checkout, its scratch `HOME` with the
  `.netrc`, and every directory its environment names - the caches
  (`COMPOSER_HOME`, `GOMODCACHE`, `npm_config_cache`) and `PATH`. The task
  gets a private temporary directory, discarded afterwards.

  **It fails closed.** Before its first task a process runs the sandbox on
  itself and checks from inside what a task must not manage - the parent's
  environment unreadable, every hidden entry gone, no capability left - and
  that a kept path is still there. If the check fails, every task is
  refused with the reason and its branch held, never run as if isolation
  had held. That happens off Linux, under a seccomp profile that refuses
  user namespaces, and on a kernel that strips them of capabilities (Ubuntu
  24.04's `apparmor_restrict_unprivileged_userns`). The estate's executor
  permits the sandbox only because it applies no seccomp profile - a
  property of the runner configuration, not a promise; `test:sandbox:*`
  proves it in the images production runs tasks in, before a release.
  `PINUP_TASK_ISOLATION=off` runs tasks unisolated, by name and said on
  stderr.

  **What it does not cover.** The network: a task that fetches packages
  needs it, and can reach anything the job can. Files outside the hidden
  paths that the uid can read stay readable; the hidden list is what the
  job is known to keep secrets in, not a whitelist of the filesystem.

  And the caches its environment names (`COMPOSER_HOME`,
  `npm_config_cache`, `GOMODCACHE`): kept visible so lock refreshes stay
  fast, shared between repositories, and **writable by every task**. That
  is a way to poison the next repository's lock refresh, and none of the
  three tools stops it: composer revalidates its metadata with
  `If-Modified-Since` and keeps a poisoned entry until upstream changes,
  with `dist.url` pointing anywhere and `shasum` usually empty; an npm
  packument carries `resolved` and `integrity` together, so a poisoned one
  brings a hash that matches the poisoned tarball; Go checks public modules
  against the checksum database, but the estate's own are `GOPRIVATE` and
  are not checked. Lock-file maintenance merges automatically. Not closed
  yet; per-repository caches are the planned answer.
- **A plan explains why nothing happens.** A held update carries its
  reason and the rule that held it; a refused command is named, not
  skipped.

## The platform credential

The platform token is bound to the instance's host and, on GitLab, to
the API paths the bot itself calls: releases, tags, raw files, packages,
the group composer registry, its own user. A URL a repository
configuration names on the instance - a `customDatasources` template, a
`registryUrls` entry - is requested without it. Otherwise any developer
on any scanned repository could point the bot's `api`-scoped token at
`/api/v4/groups/<id>/variables` and read the result as a "release list".

The binding is to the path **as it goes on the wire**, and it is checked
on every hop. The pattern is matched against the escaped path, so an
escaped project path is the one segment it is; a path carrying a `.` or
`..` segment, in any encoding a server might decode first, is refused
rather than cleaned - whether the instance resolves it before routing is
the instance's decision, not ours; and a redirect re-earns the credential
or loses it, because net/http copies the first request's headers onto
every hop and a same-host 3xx would otherwise carry the token to a path
the pattern never admitted.

The container registry credential exists only for the estate's own
registry (`PINUP_REGISTRY_HOST`), exchanged at the instance's token realm
over TLS, scoped to the one repository a lookup needs. Every other
registry is asked anonymously.

The GitHub token is bound to `api.github.com` (or the enterprise host).
A redirect off a platform's host is refused, not followed: net/http
drops `Authorization` across hosts but not a `PRIVATE-TOKEN` header.

No token ever lands in a file or a URL. git asks for credentials through
`GIT_ASKPASS`, which is the pinup binary itself, and it answers only a
prompt for the platform's host.

## What a repository can decide

A repository's own configuration file can disable itself, change versionings
and registries, add rules, ask for tasks. It cannot:

- run a command the runner's `PINUP_ALLOWED_COMMANDS` - or, absent that
  variable, the runner's own configuration file - does not admit. The list
  is never read from a repository's document: `allowedCommands` is a
  global-only key, stripped from anything a repository brings, because a
  repository that could set it would be authorising its own commands.
  Neither source set means every task is refused
  (matched against the compiled command, after templating);
- raise `executionTimeout` (the runner's `PINUP_EXECUTION_TIMEOUT`);
- reach the platform token through a URL it names;
- write outside a task's declared scope;
- be automerged on a label the runner's rules did not trust.

## Commits

Every commit is signed as the runner is configured (`openpgp` or `ssh`
with `PINUP_SIGNING_KEY`), by an identity the key vouches for
(`CheckIdentity` refuses a key that does not name the author), and the
run reads the platform's verdict on its own commits. Unsigned is only
possible with the explicit word `none`.

A branch somebody else committed to is theirs: the run neither rebuilds
it nor closes its request, and says whose it is.

## Reporting

Security reports to the address in `SECURITY.md` of the repository.
