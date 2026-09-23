<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Getting started

pinup is one static binary. It needs `git` on the machine, a token for the
platform it writes to, and a configuration in Renovate's language.

## The binary

Every release publishes `pinup-linux-amd64`, `pinup-linux-arm64`,
`pinup-darwin-arm64` and `pinup-darwin-amd64` with a signed `SHA256SUMS`
on the release page. Or build it:

```sh
go install github.com/ohartwig/pinup/cmd/pinup@latest
```

`pinup version` prints what you have.

## The container image

Two images per release, `linux/amd64` and `linux/arm64`:

| Image | What is in it |
|---|---|
| `ghcr.io/ohartwig/pinup` | pinup, git, gpg, ssh-keygen. No Node, no PHP - the build fails if either turns up. |
| `ghcr.io/ohartwig/pinup-toolchain` | the same, plus composer, npm, node, go and yarn for lock-file refreshes and `postUpgradeTasks` |

```sh
docker run --rm -v "$PWD:/workspace" ghcr.io/ohartwig/pinup:0 \
  pinup whatif --repo . --config .pinup.json --report plan.json
```

Tags are the exact version (`0.35.0`), the minor line (`0.35`), the major
line (`0`) and `latest`. Take the slim one unless the repositories you
scan carry composer, npm or Go locks: pinup only needs a package manager
when an update has a lock file to regenerate, and the toolchain image is
five times the size.

The binary in the image is the one from the release - the workflow that
builds the image downloads it and checks it against `SHA256SUMS` rather
than compiling its own. Both images are signed keyless, so the signature
is verifiable without a key from us. The image runs as uid 1000 in
`/workspace`.

[Container images](container-images.md) has the package pages, the
`cosign verify` line, what each image carries and what running one needs.

## A plan of a checkout, without a token

The first thing to run is `whatif`: it resolves the configuration,
extracts the dependencies, looks versions up and writes a plan. It changes
nothing and needs no platform token (private registries aside).

```sh
cd a-repository
pinup whatif --repo . --config .pinup.json --report plan.json
```

The plan says what pinup would do and, for everything it would not do,
why: every held update carries its reason, its thaw time and the rule that
held it ([the plan](plan-format.md)). `--now 2026-01-01T00:00:00Z` plans
as of another moment, which is how a schedule or a release age is checked
without waiting.

## A run against a project

`run` does what `whatif` does and then pushes the branches, opens or
updates the merge requests and writes the dashboard issue. It needs the
platform and a token:

```sh
export PINUP_GITLAB_URL=https://gitlab.example.org
export PINUP_GITLAB_TOKEN=glpat-…            # api, write_repository
export PINUP_GIT_NAME="Dependency Bot" PINUP_GIT_EMAIL=bot@example.org
export PINUP_SIGNING_FORMAT=none              # or openpgp / ssh with PINUP_SIGNING_KEY

pinup run --project group/project --config 'local>group/runner-config' --report plan.json
```

`--dry-run` stops after the plan, with the platform's view of the
existing merge requests folded in. `--repo .` runs against a checkout
instead of cloning. On GitHub the same command reads `PINUP_GITHUB_TOKEN`
and `--project owner/repository` ([platforms](platforms.md)).

`--config` is a file or a `local>` preset the platform serves - a
`default.json` in a runner project that every repository's own
configuration file extends. The repositories keep their files; pinup answers
the runner's name from the file it was started with, without a fetch
([configuration](configuration.md)).

## Every project the token can see

```sh
pinup run --autodiscover '["group/**", "!group/archive/**"]' --config … --report 'reports/%s.json'
```

The list is every project the token can see that is not archived,
filtered by the globs (`!` negates); eight projects run at a time, each
its own clone. A `%s` in `--report` becomes the project path.

## A CI job

The shape of a scheduled job on GitLab: the toolchain image carries pinup
and whatever the lock refreshes need (composer, npm, go), the token is a
masked variable, the plan is an artefact.

```yaml
pinup:scan:
  image: ghcr.io/ohartwig/pinup-toolchain:0
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule"
  variables:
    PINUP_CACHE: .pinup/cache.db
  cache:
    key: pinup-$CI_JOB_NAME
    paths: [.pinup/cache.db]
  script:
    - pinup run --autodiscover '["group/**"]' --config 'local>group/runner' --report 'reports/%s.json'
  artifacts:
    paths: [reports/]
    when: always
```

`PINUP_CACHE` keeps release lists, first-seen records, advisories and
release notes between runs: a release whose registry publishes no
timestamp gets its age from when this cache first saw it, which is why a
run with an empty cache warns about `minimumReleaseAge` rather than
trusting it.

The runner that operates an estate of two hundred repositories on this
shape - partitions, a release fast lane, an advisory watch - is
described in the architecture notes of the development repository.

## What to read next

[Configuration](configuration.md) for the keys, [the plan](plan-format.md)
for what the run writes, [security](security.md) for what a repository can
and cannot make the bot do.
