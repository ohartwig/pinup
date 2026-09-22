<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Effective classification

Every update carries two labels.

**Declared** comes from the version strings, always: `major`, `minor`,
`patch`, `digest`, `pin`, `pinDigest`, `rollback`, computed by the
dependency's versioning. It is what `matchUpdateTypes` reads and what
Renovate calls `updateType`.

**Effective** comes from an analyzer that looked at what actually
changed. It is `unknown` unless a rule asked for it and an analyzer could
read the thing; then `patch`, `minor`, `major` or `breaking-values`.

The case that motivates it: a chart vendor that raises the major on every
release. Under the declared label every one of those is a major, held for
approval; the application inside and the values a consumer sets may be
what they were.

## The safety rule

An automerge decision uses the stricter of the two labels. An analyzer
may only *relax* a decision where a rule says its word is to be trusted,
`trustEffective: true`. Without that, an automerge a `matchEffective` rule
switched on stays off, the request is still opened, and its evidence
table says why. An analyzer can never make an update stricter than the
rules already made it, and never fires a rule by being absent: `unknown`
is not a value `matchEffective` matches.

## Asking for it

```jsonc
"packageRules": [
  { "matchDatasources": ["docker"], "matchPackageNames": ["registry-1.docker.io/bitnamicharts/**"],
    "versioning": "semver", "analyze": true },
  { "matchDatasources": ["docker"], "matchUpdateTypes": ["major"],
    "matchEffective": ["patch", "minor"], "trustEffective": true,
    "automerge": true, "dependencyDashboardApproval": false }
]
```

The first rule marks the dependencies (`analyze` is off by default: the
analyzer fetches both versions). The second reads the label: a declared
major whose effective label is patch or minor opens without approval and
merges on its own. A `breaking-values` verdict matches neither, and the
major stays what the other rules make of it.

## The helm-chart analyzer

Applies to `helm` dependencies (a chart repository's `index.yaml` names
the archive per version) and to `docker` dependencies whose tag is a
chart (the manifest's config is `application/vnd.cncf.helm.config.v1+json`,
the layer the archive) - which is how OCI registries carry charts. A
docker tag that is an image is not the analyzer's and stays `unknown`.

It reads `Chart.yaml` and `values.yaml` of both versions and reports:

| Compared | Verdict |
|---|---|
| `appVersion` | the application's own move: `major`, `minor`, `patch`, `unchanged`; `not stated` or `not comparable` leave the label unknown |
| `values.yaml` keys, flattened to paths | a key the new chart no longer has is a value a consumer may have set that now does nothing: `breaking-values`, the strictest label, whatever `appVersion` did; added keys are counted |
| `kubeVersion` | a changed constraint, as evidence |
| `dependencies` | each subchart's move, as evidence |
| the images the values place | how many the chart brings, how many without a digest, and every reference that moved between the two versions - as evidence |

### The images a chart places

A chart carries container images in its own `values.yaml`, and a consumer who
sets nothing still runs them. They are in no file of the consumer's repository,
so no manager reads them: nothing updates them, nothing pins them, and until
this analyzer nothing named them. The chart version is the only thing the
consumer sees; the image set behind it is the vendor's to change.

The evidence is a count and a diff:

```
images   3 image(s) placed by the chart's own values, 2 without a digest
images   image docker.io/bitnami/redis:7.4.2 -> docker.io/bitnami/redis:8.0.0
images   metrics.image added quay.io/exporter@sha256:…
```

**It carries no risk of its own, and that is deliberate.** Whether a chart pins
its images is a standing property of that chart, true of every version of it.
Letting it colour the label would put nearly every chart on the strictest one,
and a verdict that says the same thing about everything says nothing. What a
bump *moved* is evidence a reviewer can act on; what the chart has always done
is a number.

**What reading values cannot see.** A chart may compose a reference in its
templates - `{{ .Values.global.registry }}/{{ .Values.image.name }}` - and then
`values.yaml` holds the parts rather than the reference. Those are skipped
rather than reported as fragments, so the count is a floor and not a total. Full
fidelity would need a render, which would need Helm; the evidence says so on
every run, including one that found nothing, because silence would read as "this
chart places none".

Measured against Docker Hub's `bitnamicharts/redis`, 20.13.4 → 28.1.0:
`appVersion` 7.4.3 → 8.10.1 (major), two values keys removed
(`sentinel.externalAccess.service.loadBalancerIP`, …) → `breaking-values`,
automerge off. The same chart from 20.13.4 to a 21.0.0 whose application
and values stood still reads `patch`.

## What the merge request shows

Under the update table, per analyzed update:

```markdown
**registry-1.docker.io/bitnamicharts/redis**: declared major, effective breaking-values (helm-chart)

| Compared | From | To | Finding |
|---|---|---|---|
| appVersion | `7.4.3` | `8.10.1` | major |
| values | — | — | 2 keys removed, 19 added: sentinel.externalAccess.service.loadBalancerIP, … |
| dependencies | `2.x.x` | `2.41.0` | common |
```

and, where a rule switched automerge on without `trustEffective`, one
more row saying which rule did and what would let it.

## Not yet

`helm template` with the consumer's own values, diffing the rendered
manifests, as a plugin; a docker-image analyzer over OCI labels and
attestations. The interface is `classify.Analyzer`; an analyzer is one
package under `analyzer/` and one line in `wire`.
