<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Managers, datasources, versionings

Three registries, keyed by the names a configuration uses. A manager
reads a file and produces dependencies with byte ranges; a datasource
answers a dependency's releases; a versioning orders them and rewrites the
value. `enabledManagers` is a closed list: a manager not named there does
not run, and `custom.regex` must be named to run the custom managers.

## Managers

| Name | Reads | Notes |
|---|---|---|
| `dockerfile` | `Dockerfile`, `Containerfile`, `*.Dockerfile` | `FROM`, `COPY --from`, `# syntax=`; ARG-interpolated images; digest pins kept or added with `pinDigests`; warns about an unmanaged version pin (see below) |
| `gitlabci` | `.gitlab-ci.yml` | `image:` and `services:` at every nesting, string and object form; `include: component:` pins (`gitlab-tags` on the component's project); `$VAR` tags are reported as unresolvable rather than guessed |
| `kustomize` | `kustomization.yaml` | `images:` with `newTag`/`digest`, remote `resources` and `components` with `?ref=`, `helmCharts:` |
| `npm` | `package.json`, lock files (`package-lock.json`, `npm-shrinkwrap.json`, `yarn.lock`, `pnpm-lock.yaml`) | dependencies of every type, `engines.node` (`node-version`), `packageManager`, `pnpm.overrides`; workspaces read and refresh the root lock; a lock pinup cannot refresh (`bun.lock`, `bun.lockb`) holds the branch; a release age the project sets (`.npmrc`, `pnpm-workspace.yaml`, `.yarnrc.yml`) is a floor under `minimumReleaseAge` |
| `composer` | `composer.json`, `composer.lock` | `require`, `require-dev`, `config.platform.php`, repositories of type `composer` in file order followed by Packagist; the locked version is what an update moves from |
| `gomod` | `go.mod`, `go.sum` | the `go` directive (`golang-version`), requires, `// indirect` ones reached by a security fix; `go mod tidy` beside every module with a `go.sum` |
| `terraform` | `*.tf`, `.terraform.lock.hcl` | `required_providers`, registry modules; a provider bump moves the lock's version and hashes with it |
| `terraform-version` | `.terraform-version` | tfenv's file, the value taken as written |
| `custom.regex` | what `managerFilePatterns` name | [configuration](configuration.md) |

Both `# renovate:` and `# pinup:` annotation prefixes are read.

An `ARG` or `ENV` whose name ends in `_VERSION`, `_TAG` or `_REF`, whose
value is shaped like a release (three numeric components at least,
optionally `-rN` or `@sha256:…`) and which carries no annotation is
reported as an **unmanaged version pin**. It is a warning on the plan, not
an update: the value has no datasource, so nothing can be proposed for it
— which is exactly why it ages unseen. Measured on 2026-09-22 in a
customer's scheduler image: the pin one line above it was annotated and
current, this one had stood still for four upstream releases, and the
sixteen findings a container scan reported were all its Go standard
library. Two numeric components (`8.5`, `1.27`) name a line rather than a
release and stay quiet, as does a checksum beside a pin.

## Datasources

| Name | Asks | Notes |
|---|---|---|
| `docker` | the registry's `/v2/` API | tags, the digest of one tag when pinning; bearer tokens by realm; the estate's own registry with the platform credential, others anonymously |
| `gitlab-tags`, `gitlab-releases`, `gitlab-packages` | a GitLab instance's REST API | the instance from the environment unless `registryUrls` names another; paginated |
| `github-releases`, `github-tags` | `api.github.com` | with `GITHUB_COM_TOKEN` when set; the 60-an-hour limit otherwise |
| `npm` | the registry's package document | `registryUrls`; `deprecated` as a string or a boolean |
| `packagist` | the Composer repository protocol | Packagist and every registry that speaks it, the GitLab group registry's provider-format metadata included; repositories tried in file order until one has the package |
| `pypi` | the JSON API | by the PEP 503 name; a release yanked in every file is deprecated |
| `node-version` | `nodejs.org/dist/index.json` | read once per process |
| `golang-version` | `go.dev/dl` | the Go toolchain |
| `go` | the module proxy, or the instance for modules on it | a `go/vN` tag in a subdirectory of a project is resolved through the project's tags |
| `helm` | a chart repository's `index.yaml` | needs `registryUrls`; the index is fetched once per repository |
| `terraform-provider`, `terraform-module` | the registry protocol (discovery first) | OpenTofu's or HashiCorp's registry; provider hashes for the lock |
| `git-tags`, `git-refs` | `git ls-remote` | any repository git can reach; `git-refs` for a branch pinned by commit |
| `custom.*` | what `customDatasources` declares | `defaultRegistryUrlTemplate`, `format`, `transformTemplates`; apk indexes (`custom.wolfi` and the ones `PINUP_APK_VIEWS` names) are served natively from the APKINDEX |

An apk index carries whole families, and a package named after its series
(`kubectl-1.36`, `mysql-9.7-client`, `valkey-cli`) cannot see the next
series through its own releases - the name is the series. When the index
carries a sibling of a higher series (`kubectl-1.37`, `valkey-9.1-cli`),
the plan says so as a `majorAvailable` update naming it in `stream`, held
like a rolling major and listed by `notify rolling-major`. Wolfi retires
a series by removing its recipe: the index keeps serving the last build
and the security feed goes on naming fixes that are never built
(`mariadb-11.8-client` at 11.8.3-r0 against thirteen CVEs "fixed" in a
11.8.9-r1 that does not exist, 2026-09-18). The notice is how that stops
being invisible.

A datasource's failure is a warning on the plan against that dependency,
never a failed run for the repository.

## Versionings

| Name | Orders | Notes |
|---|---|---|
| `semver` | strict semver | a leading `v` is kept as written |
| `semver-partial` | semver with partial versions | `1` and `1.2` are ranges |
| `semver-coerced` | anything that starts like a version | |
| `loose` | leading numeric components | no prerelease notion: `1.0.0-alpha` is stable here, and `Satisfies` is equality |
| `docker` | image tags | version and compatibility segment (`22-alpine3.21`); a different suffix or component count is another image, not an update |
| `apk` | Wolfi/Alpine package versions | `-rN` revisions, epochs |
| `composer` | Composer constraints | `^`, `~`, `*`, stability flags, `dev-` branches |
| `npm` | npm ranges | `^`, `~`, `x`, `\|\|`, hyphen ranges |
| `node` | Node.js | semver, with `engines` ranges as npm ranges |
| `go` | Go modules | `v` prefix, pseudo-versions, `+incompatible` |
| `go-mod-directive` | the `go` directive | `1.27` admits `1.27.x` |
| `hashicorp` | Terraform constraints | `~>`, `>=`, `,` |
| `regex` | `regex:<pattern>` | named groups `major`, `minor`, `patch`, `prerelease`, `compatibility` |
| `pep440` | Python versions | epoch, `a`/`b`/`rc`, `.post`, `.dev`, local; specifier sets |

A datasource names its default versioning; a rule's `versioning` or an
annotation's `versioning=` overrides it. `regex:` versionings are compiled
per pattern.
