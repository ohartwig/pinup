<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: MIT
-->

# pinup

Dependency updates as one static Go binary: it reads your existing Renovate
configuration, plans every change before it writes one, and opens merge
requests that say what they bring. The datasources and managers are
platform-neutral; the platform behind them - where the merge requests, the
dashboard issue and the project listing live - is an interface with a
GitLab implementation proven in production and a GitHub one on its way
(`docs/tasks.md`, D.24).

```text
config → resolve → checkout → discover → extract → lookup → classify → plan → apply → publish
```

Every run produces a machine-readable **plan** before the first byte is
written — `pinup whatif` stops there, `pinup run` continues. A plan explains
absence: every update that is held carries its reason, when the hold lifts
and which rule imposed it.

## Why

A Renovate runner across two hundred repositories took twenty to thirty
minutes an hour on a Node runtime with a sidecar for every datasource it did
not speak. pinup replaces that runner for the same repositories and the same
configuration in three to nine minutes, with no runtime and two third-party
dependencies, and without moving a line of anyone's `renovate.json`:

- **Renovate-compatible configuration.** `extends`, presets, `packageRules`
  (concatenated, applied in order), custom regex managers with Handlebars
  templates, JSONata transforms, schedules, `minimumReleaseAge`,
  `internalChecksFilter`, lock-file maintenance, `postUpgradeTasks`,
  vulnerability alerts (OSV), the dependency dashboard with its checkboxes.
  `pinup migrate` reports, key by key, what a configuration uses and what
  pinup supports.
- **Renovate-compatible branches.** The same branch names and titles, so an
  estate switches without a single duplicated merge request; open branches
  are adopted.
- **A shadow mode.** `pinup shadow` compares pinup's plans with the merge
  requests Renovate has open, run after run, and refuses to call itself
  right until the two agree — with a control repository that must differ.
- **Clean room.** Renovate is AGPL-3.0; pinup is MIT. Nothing was copied.
  Behaviour was observed by executing the pinned Renovate container and
  re-implemented from recorded input/output pairs, which are the test suite.

## Install

```sh
go install github.com/ohartwig/pinup/cmd/pinup@latest
```

Static binaries for Linux and macOS (amd64, arm64) come with every
[release](https://github.com/ohartwig/pinup/releases), with `SHA256SUMS` and
a detached cosign signature over it. They are built and signed once, in the
author's pipeline, and the same files are published on GitHub — nothing is
rebuilt there.

```sh
cosign verify-blob --key <public-key> --signature SHA256SUMS.sig \
  --insecure-ignore-tlog=true SHA256SUMS
sha256sum -c SHA256SUMS
```

## Use

```sh
# Plan a checkout under a configuration; write nothing.
pinup whatif --repo . --config renovate.json --report plan.json

# The same, then apply and publish: branches, merge requests, dashboard.
export PINUP_GITLAB_URL=https://gitlab.example.org PINUP_GITLAB_TOKEN=glpat-…
pinup run --project group/project --config 'local>devops/renovate-runner'

# Every project an autodiscover filter matches, eight at a time.
pinup run --autodiscover '["devops/**","!devops/archive/**"]' --config … --report 'reports/%s.json'

# What a configuration resolves to, and which rule set each value.
pinup print-config --config renovate.json --explain

# Which of a configuration's keys pinup supports, and a YAML rewrite of it.
pinup migrate --config renovate.json
pinup migrate --config renovate.json --to yaml
```

`pinup <command>` without flags prints the usage of each command.

## Configure

pinup reads `renovate.json`, `.renovaterc.json`, `.pinup.jsonc` or
`.pinup.yaml` in the repository, resolved over the configuration the run was
started with (`--config`), which may itself be a `local>` preset fetched
from the instance. The vocabulary is Renovate's; the merge semantics are
Renovate's (objects deep-merge, arrays replace, `packageRules` concatenate,
`null` clears). Every resolved value keeps its origin chain, and
`print-config --explain` shows it.

Environment:

| Variable | Meaning |
|---|---|
| `PINUP_GITLAB_URL` / `CI_SERVER_URL` | the instance |
| `PINUP_GITLAB_TOKEN` / `GITLAB_TOKEN` | a personal access token (`api`, `write_repository`); `CI_JOB_TOKEN` is used when none is set (read-only) |
| `PINUP_REGISTRY_HOST` / `CI_REGISTRY` | the estate's container registry; the token is exchanged for a pull token there and nowhere else |
| `GITHUB_COM_TOKEN` | for GitHub lookups and release notes, bound to `api.github.com` |
| `PINUP_GIT_NAME`, `PINUP_GIT_EMAIL`, `PINUP_SIGNING_FORMAT`, `PINUP_SIGNING_KEY` | who commits and how commits are signed (`openpgp` or `ssh`); unsigned only with the explicit word `none` |
| `PINUP_CACHE` | the lookup cache (bbolt); release lists, first-seen records, advisories, release notes |
| `PINUP_ALLOWED_COMMANDS` | JSON array of anchored patterns a `postUpgradeTasks` command must match — the runner's decision, never a repository's |
| `PINUP_PLUGIN_ENV` | variables a task may see besides `PATH`, `LANG`, `TZ`; tokens and keys never cross |
| `PINUP_TASK_NETRC` | a `.netrc` written into each task's scratch `HOME` (`machine <host> login <user> password <read-only token>`), for a toolchain that fetches first-party modules over https - `go mod tidy` on a private Go module; never handed over as a variable |
| `PINUP_RUNNER_PROJECT` | the project repositories extend the runner configuration from, as `local><project>`; taken from `--config local>…` when that names it |
| `PINUP_APK_VIEWS` | apk indexes served natively besides the public Wolfi repository: `{"custom.<name>": {"mirrors": [...], "arches": [...]}}`; a configuration's `customDatasources` entry of the same name is superseded |

## Security

- The platform token is sent only to the API paths pinup itself calls, never
  to a URL a repository configuration names; it is dropped on any redirect
  off its host.
- Tasks (`postUpgradeTasks`, lock refreshes) run as argv without a shell,
  against an allowlist on the compiled command, with an allowlisted
  environment and a scratch `HOME`; their result is validated against the
  file scope they were given.
- Text from elsewhere — release notes, `prBodyNotes` — is sanitised before it
  reaches a merge request: no quick actions, no stray mentions.
- The lookups' HTTP client contacts no third-party service beyond the
  registries a dependency names and OSV when `osvVulnerabilityAlerts` is on.

Report vulnerabilities as [SECURITY.md](SECURITY.md) says.

## Develop

Go 1.27, `git`; `gpg` and `ssh-keygen` for the signing tests. No other
tooling.

```sh
go build ./cmd/pinup
go test ./...
go vet ./... && gofmt -l .
```

Six layers, enforced by a test rather than a convention; hermetic HTTP
through servers that speak the protocol, with a transport that fails a test
on any unregistered host; golden repositories that are versioned by
directory, never rewritten; a mutation suite that breaks each harness layer
on purpose and must see it go red. [`docs/plan.md`](docs/plan.md) has the
architecture, [`docs/tech-spec.md`](docs/tech-spec.md) the specification
with its corrections, [CONTRIBUTING.md](CONTRIBUTING.md) the rules.

## Where this lives

The canonical public home is <https://github.com/ohartwig/pinup>; that is
the module path and where tags and releases appear. Development and the
release pipeline run on the author's GitLab, which mirrors here — issues and
pull requests here are read.

## License

MIT — see [LICENSE](LICENSE). Renovate, whose behaviour pinup reproduces,
is AGPL-3.0 and none of it is included here; see [NOTICE](NOTICE).
