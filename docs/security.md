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

  Said precisely, because the filtered environment on its own does not
  carry the claim: a task runs as the same user, in the same process tree,
  so on Linux it can read the parent's `/proc/<pid>/environ` directly. The
  environment filter is hygiene. What holds the line is that a task has
  nothing to *execute* the credential with: the commands it may run are an
  operator allowlist, a package manager it names is hardened so it cannot
  run code out of the checkout (`--no-scripts`, `--no-plugins`,
  `--ignore-scripts`, appended before the allowlist is consulted), and the
  git configuration that makes git execute a program - `core.hooksPath`,
  `core.fsmonitor`, `gpg.program` - is pinned on every invocation and the
  `.git` control surface is fingerprinted around every task. Real isolation
  (a separate uid, a namespace) is what would make the environment filter a
  boundary; it is not there today, and this list should not read as if it
  were.
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

A repository's `renovate.json` can disable itself, change versionings
and registries, add rules, ask for tasks. It cannot:

- run a command the runner's `PINUP_ALLOWED_COMMANDS` does not admit
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
