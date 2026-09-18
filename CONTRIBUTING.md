<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Contributing

Thank you for looking at this. Most contributions are a measured behaviour,
a test that fails without it, and the lines that make it pass.

## Where to send what

- **Bugs and ideas:** issues on [GitHub](https://github.com/ohartwig/pinup/issues).
- **Changes:** pull requests on GitHub are welcome. Development and the
  release pipeline run on the author's GitLab; a pull request is reviewed on
  GitHub and lands there through the mirror, so a merge may take a day.
- **Security problems:** see [SECURITY.md](SECURITY.md), not an issue.

## Setup

Any Go 1.27 toolchain and `git`. The signing tests also want `gpg` and
`ssh-keygen` on `PATH` and skip without them. Nothing else: no linter
beyond `go vet` and `gofmt`, no test framework beyond the standard library.

## The one rule that is not negotiable

**Nothing is copied from Renovate.** Renovate is AGPL-3.0 and pinup is Apache-2.0.
Behaviour is *observed* — by running the pinned Renovate container over an
input and recording what comes out — and re-implemented from the recorded
pairs. A contribution that ports a Renovate function, a preset file or a
fixture table cannot be accepted, however small. If you want pinup to match
a Renovate behaviour, record it: the input, the container's output, the
version, and put the pair where the existing tables live.

## Commits

Conventional Commits, in English: `feat`, `fix`, `docs`, `refactor`,
`perf`, `test`, `ci`, `chore`; a breaking change takes `!` or a `BREAKING
CHANGE:` footer. The tool releases itself from them. The body says *why*,
and where a behaviour was measured, it names the measurement.

## Tests

Every behavioural change needs a test, and every test must be able to fail:

- HTTP is served by `httptest` servers that speak the real protocol, behind
  a transport that fails the test on any host nobody registered.
- Golden repositories under `testdata/<root>/golden` are read, never rewritten.
  A behaviour change that moves a golden is recorded into a new directory
  and reviewed as a diff (`PINUP_GOLDEN_RECORD=<name> go test ./cmd/pinup`).
- Layering, `time.Now()` placement and other house rules are tests in
  `tools/lint`.
- A change to a harness layer comes with a mutation in `tools/mutate` that
  the layer must detect.

Before pushing:

```sh
gofmt -l . && go vet ./... && go test -race ./...
```

## Architecture in one paragraph

Six layers; a package imports strictly lower ones. `model` and the pure
parsers at the bottom; `config`, `versioning`, `httpx`, `cache`, `git`
above; the pipeline stages (`discover`, `extract`, `lookup`, `rules`,
`planner`, `apply`, `publish`, `osv`, `changelog`) declare interfaces that
`manager/*`, `datasource/*`, `versioning/*`, `platform/*` and `plugin`
implement; `runner` and `report` orchestrate; `wire` knows every
implementation; `cmd/pinup` is the binary. Two invariants: `apply` consumes
byte-range edits, never dependencies, so nothing downstream of the plan can
invent a change; and a plan explains why nothing happens.
