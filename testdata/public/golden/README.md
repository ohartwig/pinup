# Golden repositories

Each directory is one repository tree (`repo/`), the answers a live run
recorded for it (`lookups.json`: release sets, digests, advisories and
failures by lookup key) and the plan pinup produced from them (`plan.json`),
with `golden.json` naming the moment the plan was made and what the
repository covers. `cmd/pinup`'s `TestGoldenRepositories` re-plans every tree
from the canned answers at that moment and compares byte for byte;
`TestGoldenCoverageIsComplete` asserts that the union of `covers` reaches
every manager, datasource and versioning `wire` knows, or that
`UNCOVERED.json` names the gap with an owner and a reason.

Golden files are read, never rewritten. A new repository is recorded once,
live, with `PINUP_GOLDEN_RECORD=<name> go test ./cmd/pinup -run
TestGoldenRepositories/<name>` and a token in the environment; recording
refuses a directory that already has a plan. A behaviour change is a new
directory reviewed as a diff, or the old plan deleted deliberately in the
commit that explains why.

| Repository | Covers |
|---|---|
| `estate.v2` | the fictional estate's own shapes: a digest-pinned base, annotated apk pins from Wolfi and the estate's index, component includes with a full version and a rolling `@N`, a first-party composer package through the templated gitlab-packages manager, a terraform provider and registry module, a bash tag versioned by its alpine suffix |
| `tfversion.v2` | `terraform-version` and `kustomize` (helm, git-tags declined and failing, docker, github) |
| `gomod.v2` | `gomod` under the runner's own configuration: `go-mod-directive`, `go` and `golang-version`; a pseudo-version pin moving as a digest, an `// indirect` requirement reached only by its security fix, `go mod tidy` beside every module that has a `go.sum` and none for the tree that has not |
| `gitrefs.v2` | `git-refs`: a branch pinned by commit (`expected-commit` under a `# renovate:` line, devops/wolfi-packages' shape) refreshed to the branch head, on the branch Renovate uses; and a `node:24-alpine` job image under the `node` versioning, where the tag is no version and the digest gets pinned - as Renovate pins it (measured on hub node:24-alpine), unlike a range no release satisfies |
| `lock.v2` | composer and npm with lock files: the lock-refresh tasks and a maintenance branch |
| `pyver.v2` | `pypi` under `pep440`: three Python tools pinned in CI variables (the annotation managers for `.gitlab-ci.yml` and component templates), and `engines.node` through `node-version`, a range the current line already satisfies |
| `osv.v2` | the vulnerability fast path: lodash 4.17.20, guzzle 7.4.4, symfony/http-kernel 6.0.0 against OSV |
| `packagist.v2` | Packagist's security advisories beside OSV: typo3/cms-backend 14.3.6 against TYPO3-CORE-SA-2026-022 (CVE-2026-77132), which OSV did not carry when it was recorded on 2026-10-05 - the fix opens from Packagist alone |

## v2 (2026-10-06)

Every directory moved to `<name>.v2` when the plan started to say, per
dependency, whether anything covers it against advisories
(`advisoryCoverage`, plan-format.md). The repositories, the recorded answers
and `golden.json` are byte for byte those of v1; each `plan.json` differs
only by the `advisoryCoverage` objects, and every `plan.md` is unchanged -
`git diff -M` against v1 shows exactly that. The recorded release sets
predate `withdrawalList`, so no golden dependency is in the `withdrawal`
state; that state is covered by the unit tests.
