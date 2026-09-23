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
  carry the claim: a task runs as the same user, in the same process tree,
  so on Linux it can read the parent's `/proc/<pid>/environ` directly, open
  any file the job owns (the imported signing key among them), and - where
  `kernel.yama.ptrace_scope` is 0, as measured on the estate's runners -
  attach to the parent. The environment filter is hygiene, not a boundary.

  How much that matters depends on what the allowlist admits. A package
  manager it names is hardened so it cannot run code out of the checkout
  (`--no-scripts`, `--no-plugins`, `--ignore-scripts`, appended before the
  allowlist is consulted), and the git configuration that makes git execute
  a program - `core.hooksPath`, `core.fsmonitor`, `gpg.program` - is pinned
  on every invocation, with the `.git` control surface fingerprinted around
  every task. But an entry of the form `node scripts/<file>.mjs` runs code
  **from the scanned repository** by design: for such a task, whoever can
  write to that repository runs code under the job's uid, and only what the
  task is handed and what that uid can read decide what they get.

  Real isolation is what would turn the filter into a boundary, and it is
  not there today. Measured on the estate's executor (2026-09-22, both the
  toolchain and the golden image): the job runs as uid 1000 with no
  capabilities, so dropping a task to another uid is not available; an
  unprivileged user namespace is, and it denies the parent's `environ` and
  `ptrace`, but not a file the same uid owns - hiding those takes a mount
  inside the namespace before the task's `exec`. It is permitted only
  because the executor applies no seccomp profile; isolation built on it
  has to check at run time that it took effect.
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
