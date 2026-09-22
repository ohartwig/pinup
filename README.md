<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# pinup

Dependency updates as one static Go binary: it reads your existing Renovate
configuration, plans every change before it writes one, and opens merge
requests that say what they bring. The datasources and managers are
platform-neutral; the platform behind them - where the merge requests, the
dashboard issue and the project listing live - is an interface with two
implementations: GitLab, proven in production across an estate of two
hundred repositories, and GitHub, proven against a fake that speaks the
API and in a read-only run against this repository's own mirror, waiting
for its first production repository.

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
- **An effective label beside the declared one.** A rule with `analyze:
  true` asks an analyzer what actually changed: for a Helm chart, the
  application's own version, the values keys the new chart no longer has
  (`breaking-values`), the subcharts. `matchEffective` rules read the
  label, the merge request shows the evidence, and the stricter of the two
  labels decides automerge unless a rule says `trustEffective: true` -
  for the chart vendor that raises the major on every release.
- **Renovate-compatible branches.** The same branch names and titles, so an
  estate switches without a single duplicated merge request; open branches
  are adopted.
- **A shadow mode.** `pinup shadow` compares pinup's plans with the merge
  requests Renovate has open, run after run, and refuses to call itself
  right until the two agree — with a control repository that must differ.
- **Clean room.** Renovate is AGPL-3.0; pinup is Apache-2.0. Nothing was copied.
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

Or as a container image — [`ghcr.io/ohartwig/pinup`](https://github.com/ohartwig/pinup/pkgs/container/pinup)
(git, gpg, nothing else) and
[`ghcr.io/ohartwig/pinup-toolchain`](https://github.com/ohartwig/pinup/pkgs/container/pinup-toolchain)
(plus composer, npm, node, go, yarn for lock refreshes), `linux/amd64` and
`linux/arm64`, signed keyless:

```sh
docker run --rm -v "$PWD:/workspace" ghcr.io/ohartwig/pinup:0 \
  pinup whatif --repo . --config renovate.json --report plan.json
```

The image carries the released binary, checked against `SHA256SUMS`; it is
not compiled a second time there either.
[Container images](docs/container-images.md) has the tags, the verification
and what each one is for.

## Quickstart

Four steps, each complete on its own. Stop where you have what you need.

### 1. Plan a repository you already have

`whatif` resolves the configuration, reads the manifests, looks the
versions up and writes a plan. It changes nothing and needs no token
(private registries aside). Any repository with a `renovate.json` will do;
without one, `{"extends": ["config:recommended"]}` is enough to start.

```sh
cd a-repository
pinup whatif --repo . --config renovate.json --report plan.json

# what would move, and what is held back and why
jq -r '.updates[] | "\(.dep.depName) \(.dep.currentValue) -> \(.newValue)  \(.blocks[0].reason // "ready") \(.blocks[0].origin | if . then "\(.source)[\(.rule)]" else "" end)"' plan.json
```

A line like `lodash 4.17.20 -> 4.17.21  ready` becomes a branch on the
next `run`; `redis 20.13.4 -> 28.1.0  dependencyDashboardApproval
packageRules[3]` says which rule holds the major and where it lives
(`config[-1]` is the top level of the configuration).
`--now 2026-01-01T00:00:00Z` plans as of another moment - the way to check
a schedule or a `minimumReleaseAge` without waiting for it.

### 2. Run against one project

`run` does what `whatif` does and then pushes the branches, opens or
updates the merge requests and keeps the dashboard issue. It needs the
platform and a token that may write to the project.

```sh
export PINUP_GITLAB_URL=https://gitlab.example.org
export PINUP_GITLAB_TOKEN=glpat-…              # scopes: api, write_repository
export PINUP_GIT_NAME="Dependency Bot" PINUP_GIT_EMAIL=bot@example.org
export PINUP_SIGNING_FORMAT=none               # or openpgp / ssh + PINUP_SIGNING_KEY

pinup run --project group/project --config renovate.json --report plan.json
```

On GitHub the same command reads `PINUP_GITHUB_TOKEN` and
`--project owner/repository`; a GitHub token with no GitLab instance in
the environment selects the platform. `--dry-run` stops after the plan
with the platform's view of the open merge requests folded in - the
safe first run against a real project.

What you get: one branch per update or group, named the way Renovate
names it (`renovate/lodash-4.x`), a merge request whose description lists
the edits and the release notes, `automerge` set where a rule says so, and
a "Dependency Dashboard" issue that lists every held update with a
checkbox to approve it.

### 3. One configuration for many repositories

Put the estate's rules in a runner project and let each repository extend
it. Nothing else has to change in the repositories, and a repository with
no file at all still gets the runner's defaults.

```jsonc
// runner project: default.json
{ "extends": ["config:recommended"], "packageRules": [ … ] }

// any repository: renovate.json
{ "extends": ["local>group/pinup-runner"] }
```

```sh
pinup run --autodiscover '["group/**", "!group/archive/**"]' \
          --config 'local>group/pinup-runner' --report 'reports/%s.json'
```

`--autodiscover` runs every non-archived project the token can see that
matches the globs, eight at a time; `%s` becomes the project path.
[`docs/examples/renovate.json`](docs/examples/renovate.json) is a
complete runner configuration - automerge for patch and minor, dashboard
approval for majors, an analyzer rule for a chart vendor that raises the
major on every release.

### 4. Schedule it

[`docs/examples/gitlab-ci.yml`](docs/examples/gitlab-ci.yml) is a
scheduled GitLab job: a toolchain image with pinup, composer, npm and go
on the `PATH`, the token as a masked variable, the lookup cache kept
between runs, the plans as artefacts.
[`docs/examples/github-actions.yml`](docs/examples/github-actions.yml)
is the same on GitHub Actions. Hourly is fine: a run over two hundred
repositories takes ten minutes of wall time, and a run that finds nothing
new writes nothing.

## Coming from Renovate

- **Keep the configuration.** `pinup migrate --config renovate.json`
  classifies every key as supported, partial or unsupported, with the
  difference spelled out ([configuration](docs/configuration.md)). Nothing
  in the repositories changes; `pinup migrate --to yaml` rewrites a file
  as `.pinup.yaml` if you want it to, and only then. `pinup advise` then
  says what the file could do better - an `ignorePaths` that dropped the
  preset's list, an automerge over majors, a preset extended twice - and
  `--fix` applies it, byte for byte, behind a check that the resolution
  changed only where the fixes said.
- **Keep the branches.** The same branch names and titles, so an open
  Renovate merge request is adopted, not duplicated. A branch somebody
  else committed to is left alone.
- **Run both for a while.** `pinup shadow --plans 'reports/*.json'`
  compares pinup's plans with what Renovate has open and fails when a
  difference persists across two runs without a triaged reason - with a
  control repository that must differ, so the comparison is proven able
  to fail before its first verdict is believed.
- **What is not there:** Bitbucket and Azure DevOps; managers pinup does
  not read (Maven, Gradle, NuGet, Cargo, pip requirements); Mend Merge
  Confidence - pinup classifies itself ([effective
  classification](docs/effective-classification.md)) and calls no third
  party.

## Commands

```sh
pinup whatif --repo . --config renovate.json --report plan.json   # plan, write nothing
pinup run --project group/project --config … --report plan.json    # plan, apply, publish
pinup run --autodiscover '["group/**"]' --config … --report 'reports/%s.json'
pinup run --released group/library@1.4.0 --config …                # the fast lane: only that dependency's consumers
pinup print-config --config renovate.json --explain                # what a configuration resolves to, and which rule set each value
pinup migrate --config renovate.json [--to yaml]                   # which keys pinup supports; a YAML rewrite
pinup advise --config renovate.json [--plan plan.json] [--fix]     # what to change - performance, security, hygiene - and why
pinup advisories --index '.pinup/consumers.json'                   # OSV over everything the runs have seen, no clone
pinup shadow --plans 'reports/*.json'                              # compare with the merge requests another tool has open
```

`pinup <command> -h` prints every flag. The documentation - getting
started, every command, the configuration keys, the plan format,
platforms, tasks, security - is in [docs/](docs/README.md) and, as web
pages, at <https://ole-hartwig.eu/en/open-source/pinup> (German:
<https://ole-hartwig.eu/open-source/pinup>).

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
| `PINUP_PLATFORM` | `gitlab` (the default) or `github`; a GitHub token with no GitLab instance in the environment means `github` |
| `PINUP_GITLAB_URL` / `CI_SERVER_URL` | the GitLab instance |
| `PINUP_GITLAB_TOKEN` / `GITLAB_TOKEN` | a personal access token (`api`, `write_repository`); `CI_JOB_TOKEN` is used when none is set (read-only) |
| `PINUP_GITHUB_URL` / `GITHUB_SERVER_URL` | the GitHub host; `github.com` when unset |
| `PINUP_GITHUB_TOKEN` / `GITHUB_TOKEN` | a token with `repo` (pull requests, issues, contents) - a fine-grained one with contents, pull requests and issues read/write; on `github.com` it serves the `github-*` datasources as well |
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
on purpose and must see it go red. [CONTRIBUTING.md](CONTRIBUTING.md) has
the rules. The architecture notes, the specification with its measured
corrections and the task list live with the development repository, not in
this mirror; ask if you need them.

## Where this lives

The canonical public home is <https://github.com/ohartwig/pinup>; that is
the module path and where tags and releases appear. Development and the
release pipeline run on the author's GitLab, which mirrors here — issues and
pull requests here are read.

## License

Apache-2.0 — see [LICENSE](LICENSE); releases up to 0.33.0 were MIT. Renovate, whose behaviour pinup reproduces,
is AGPL-3.0 and none of it is included here; see [NOTICE](NOTICE).
