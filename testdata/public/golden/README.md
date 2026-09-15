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
| `estate` | the fictional estate's own shapes: a digest-pinned base, annotated apk pins from Wolfi and the estate's index, component includes with a full version and a rolling `@N`, a first-party composer package through the templated gitlab-packages manager, a terraform provider and registry module, a bash tag versioned by its alpine suffix |
| `tfversion` | `terraform-version` and `kustomize` (helm, git-tags declined and failing, docker, github) |
| `gomod` | `gomod` under the runner's own configuration: `go-mod-directive`, `go` and `golang-version`; a pseudo-version pin moving as a digest, an `// indirect` requirement reached only by its security fix, `go mod tidy` beside every module that has a `go.sum` and none for the tree that has not |
| `gitrefs` | `git-refs`: a branch pinned by commit (`expected-commit` under a `# renovate:` line, devops/wolfi-packages' shape) refreshed to the branch head, on the branch Renovate uses; and a `node:24-alpine` job image under the `node` versioning, where the tag is no version and the digest gets pinned - as Renovate pins it (measured on hub node:24-alpine), unlike a range no release satisfies |
| `lock` | composer and npm with lock files: the lock-refresh tasks and a maintenance branch |
| `pyver` | `pypi` under `pep440`: three Python tools pinned in CI variables (the annotation managers for `.gitlab-ci.yml` and component templates), and `engines.node` through `node-version`, a range the current line already satisfies |
| `osv` | the vulnerability fast path: lodash 4.17.20, guzzle 7.4.4, symfony/http-kernel 6.0.0 against OSV |
