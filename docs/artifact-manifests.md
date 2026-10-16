<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Artifact manifests

pinup updates what is written: a line a manager reads, a lock a refresh
regenerates. A build ships more than that. A Go binary carries every module
its graph resolved, an image carries every package its layers installed, and
most of them are written nowhere in the repository that builds them. An
advisory against one of those has no line to be found on, so no manager
extracts it, no advisory source is asked about it, and the advisory watch
never hears of it.

Measured 2026-10-15/16 in the author's estate: six advisories against
`github.com/getkin/kin-openapi`, a transitive module of a Grafana binary
built from a melange recipe. OSV listed all six by 20:20 UTC. pinup's
advisory watch, which asks OSV every fifteen minutes about every dependency
it knows, asked nothing - the module had no line. A container scanner found
them on the published package at 07:26 UTC the next day, a tool proposed a
pin line, and from that line on pinup moved it within the hour. Eleven hours
of the chain were spent waiting for a scanner database to learn what OSV had
answered the evening before.

`artifactManifests` closes that gap: a repository names the manifest of what
its build shipped - a CycloneDX or SPDX SBOM, a Trivy report, a module list -
and pinup reads it like a lock. Every component becomes a dependency of the
file that built it: asked about advisories, carried into the consumer index
for the watch, and - where the configuration says how a missing pin is
written - raised past a fix the moment an advisory names one.

## What it is not

A manifest is not a lock. A lock pins what a manifest declares and a refresh
regenerates it; a manifest records what a build produced and nothing in the
repository regenerates it. pinup therefore never writes a manifest, never
treats one as current for longer than the run that read it, and never moves a
component the manifest names without a configured way to write the pin. A
component without one is stated - in the plan, on the dashboard, in the
watch - not moved.

It is not a scanner either. pinup asks the advisory sources it already asks
([advisory sources](advisory-sources.md)) about the versions the manifest
names; a vulnerability no advisory database records stays invisible here as
everywhere else in pinup. The scanner remains the instrument that finds what
the databases do not yet say.

## Configuration

`artifactManifests` is a list; each entry describes one kind of manifest and
the files whose builds it belongs to. It is pinup's own key: Renovate refuses
a configuration that carries it, so it belongs in a `.pinup.*` file or the
run's own configuration ([pinup's own keys and
Renovate](configuration.md#pinups-own-keys-and-renovate)).

```json
"artifactManifests": [{
  "description": "the Go modules of every published package, from the audit's report",
  "matchFileNames": ["/^[^/]+\\.yaml$/"],
  "format": "trivy",
  "artifact": {"job": "audit:published:aarch64", "path": "scan-reports/{{{fileStem}}}.trivy.json"},
  "pin": {
    "after": "\\n(?<indent>[ \\t]+)[a-z0-9.-]+\\.[a-z]{2,}/[^\\s@]+@v[0-9][^\\s]*",
    "line": "{{{indent}}}{{{depName}}}@{{{newValue}}}"
  }
}]
```

| Key | Meaning |
|---|---|
| `matchFileNames` | the files whose build the manifest describes, in the `/regex/` or glob form file patterns use everywhere in pinup. One manifest is read per matching file; the file is what the components belong to and where a pin is written. |
| `format` | `cyclonedx` (JSON, components with a purl or a name and version), `spdx` (JSON, packages with a purl external reference or a name and `versionInfo`), `trivy` (Trivy's JSON report, the packages of every result), `go-modules` (text: one `module version` per line, as `go list -m all` and `go version -m` print it). Required. |
| `path` | a file in the repository, rendered per matching file. |
| `url` | a URL, rendered per matching file, fetched through the shared client: host rules apply, and the platform token is sent only to the instance paths pinup itself calls, as for a custom datasource ([security](security.md)). |
| `artifact` | a CI artifact read through the platform: `job` (required), `path` within the artifact (required), `project` (default: the repository itself) and `ref` (default: its default branch), each rendered per matching file. Read with the platform token, like a `local>` preset. GitLab only. |
| `datasources` | which components to keep, by the datasource their purl type maps to (below). Default: every type pinup can look up. |
| `pin` | how a component without a line is written when an advisory names a fix (below). Without it, findings are reported and nothing is written. |
| `description` | for the reader of the configuration; pinup does not act on it. |

Exactly one of `path`, `url` and `artifact` names the source. The templates
render with `file` (the matching file's path), `fileName`, `fileStem` (its
name without extension) and `fileDir`.

### Components and datasources

A component is kept when its purl type maps to a datasource pinup looks up
and an advisory source asks about:

| purl type | datasource | asked at |
|---|---|---|
| `golang` | `go` (`stdlib` as `golang-version`, at the toolchain's version) | OSV `Go` |
| `npm` | `npm` | OSV `npm` |
| `composer` | `packagist` | OSV `Packagist`, Packagist |
| `pypi` | `pypi` | OSV `PyPI` |

Every other type - `apk`, `deb`, `rpm`, `cargo`, `maven`, a file, a binary
without a purl - is skipped and counted, by type, in the plan's `artifacts`
entry for the manifest: what was not read is stated, never left to look
like "nothing there". The four above are the types pinup has a datasource
for; a component OSV knows but pinup cannot look up would be a finding
nothing here could move, and is left to the scanner until the datasource
exists. A component the file already declares - the same datasource and
package name on a line a manager extracted - is counted as `declared` and
not read a second time: the line is the dependency, and it moves as a line
does.

### What a component becomes

Each kept component is a dependency of the matching file, manager
`artifact`, with the component's name as `depName` and `packageName`, its
version as `currentValue`, the datasource above, and that datasource's
default versioning. It carries an `artifact` object naming the manifest it
came from ([the plan](plan-format.md#deps)). Rules match it as they match any
dependency: `matchManagers: ["artifact"]`, `matchDatasources`,
`matchPackageNames`, `allowedVersions`, `enabled: false`.

It is a disabled dependency, in the sense a `// indirect` Go module is one:
not looked up and not updated on its own - there is no line that could move -
but asked about advisories under `osvVulnerabilityAlerts`, and recorded in
the consumer index so the advisory watch asks about it between full runs.
Such an entry names no consumer: a release of a transitive module is not news
to the fast lane, an advisory against it is.

### Writing the pin

An advisory that names a fix turns the component into a security update,
planned as every other one: the lowest release at or above the fix the
datasource lists, under the `vulnerabilityAlerts` configuration - its own
branch, its labels, no release age, no schedule, CISA's catalog first. What
differs is the edit. There is no value to replace, so `pin` says where a
line goes and what it says:

- `after` is a regular expression (RE2, as `matchStrings` are). Its last
  match in the file marks the place: the new line is inserted after the
  line that match ends on. A named group `indent` is the indentation of the
  new line; without one, the indentation of the line the match ends on is
  used.
- `line` is a template, rendered with `indent`, `depName`, `packageName`,
  `newValue`, `newVersion` and every named group of the match. Rendered
  exactly; pinup adds the line break before it and nothing else.
- `datasources`, optional, narrows the pin to components of these
  datasources; the rest stay reported.

The edit is a byte-range insertion like any other edit: refused when `after`
no longer matches the file as it was read, carried in the plan, applied
without re-serialising anything. The repository's `postUpgradeTasks` run
for it as for any update of that datasource - a melange recipe's epoch
raise, say.

The line must be one a manager of the repository reads afterwards: on the
next run the pin is a dependency of that manager, and pinup moves it as it
moves every line. The run checks exactly that - it extracts the edited file
and looks for the dependency the line should have become - and warns when
no manager reads it: `the line artifactManifests[0].pin writes for
github.com/getkin/kin-openapi is read by no manager; it would be written
once and never moved`. The branch is still written; the warning says what
to fix in the configuration.

Without `pin`, or for a component outside `pin.datasources`, a finding is a
statement: the dependency's `skipReason` names the advisories and the fix,
the plan warns on every run while it lasts, and the dashboard lists it. An
image built from distribution packages is the common case: the module
inside a Go binary installed from an apk has no line in the Containerfile
and cannot have one; the fix is the package's next build, which the apk pin
takes when it exists.

## In the plan and on the dashboard

`deps` carries each component as a dependency with `manager: "artifact"`
and an `artifact` object; `artifacts` lists every manifest the run read -
file, source, format, how many components it had, how many became
dependencies, how many were declared, how many skipped by type, and the
error when it could not be read ([the plan](plan-format.md#artifacts)). A
manifest that cannot be read is a warning: the rest of the run is not held
by a report that was not there.

The dashboard's *Detected dependencies* lists the components under their
file with the reason they are not updated, and sums the manifests up:
`shipped by the build: 523 components from 1 manifest, 519 watched, 4
declared, 23 skipped (apk 23)`.

## Why not a scanner, why not a lock

Two other places could have carried this. A scanner finds the same
components in the same artifacts and knows the advisories too - but it
knows them when its database does, and the database is built from the
advisory sources pinup asks directly. The eleven hours above were that
distance. And a scanner finds; it does not know which line in which file
would move the finding, which is the thing pinup is for.

A lock-file manager would have been the closer shape: the lock advisory
check (`osvTransitiveAlerts`) asks about every package a lock pins and no
manifest names, and a finding becomes the lock's security refresh. A
manifest of a build is read the same way, and the index and the watch treat
both alike. The difference is the remedy. A lock is regenerated by its
toolchain; a build's module graph is regenerated only by the next build,
and what pinup can change is the input to that build - a pin line - which is
why `pin` is configuration and not a manager: the shape of the line is the
repository's, not pinup's.

## Limits

- A manifest describes one build. A file that produces several artifacts
  (two architectures, several images) is described by the manifest of one of
  them, or by several entries; pinup merges components by datasource and
  name and keeps the lowest version it saw, which is the one an advisory
  range is most likely to contain.
- `artifact` reads GitLab job artifacts. On GitHub the entry is a
  configuration error: GitHub serves artifacts as archives only.
- The manifest is read on every full run and every targeted run; it is not
  cached. A report of a few megabytes is read in well under a second; a
  manifest behind a slow endpoint costs the run that time.
- pinup does not produce manifests. The build does - `syft`, `trivy`,
  `cyclonedx-gomod`, `go version -m` - and publishes one where the
  configuration can read it.
