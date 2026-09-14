# Versioning behaviour tables

Input/output pairs recorded by **executing** Renovate's versioning modules
inside the pinned container (`tools/capture/versioning-probe.cjs`). Nothing is
copied from the Renovate source tree: these are facts about what the program
answers, and they are what the Go implementations in `versioning/*` are written
against.

Thirteen schemes, about 15,000 rows (go-mod-directive and, on 2026-09-12, node added with the same probe over the estate's node image tags; composer recaptured the same day with the estate's OR-ranges and realistic getNewValue pairs for them - every earlier row unchanged). The grid is generic versions plus the shapes
each scheme is actually used for in this estate, plus every distinct
`currentValue` the extraction corpus produced — real inputs beat invented ones.

## What the tables already settle

**The docker compatibility segment orders in reverse.** An earlier reading of
this file claimed it "does not order", on the grounds that the two directions
of `isGreaterThan` disagree. That was wrong — disagreeing directions are what
an order *is*. Checked against all 18 equal-version pairs, the rule is exact:
when the numeric parts are equal, suffixes compare in reverse lexicographic
order.

```text
isGreaterThan("1.0.0",         "1.0.0-alpha")   = true
isGreaterThan("22-alpine3.20", "22-alpine3.21") = true
isGreaterThan("22-alpine3.21", "22-bookworm")   = true
```

And fewer numeric components win a shared prefix: `"1" > "1.0.0"`. That is the
opposite of `loose`, where longer wins.

The conclusion in §9.3 stands and the argument is better for it: a
compatibility change needs its own update type not because the order is
undefined, but because it is *inverted*. Following it would offer
alpine3.21 → alpine3.20 as an upgrade.

**`isCompatible` and `isVersion` were captured later, for the planner.** Two
operations added once lookup existed and the questions became concrete:

- `isCompatible(candidate, current)` is "candidate is a single version" for
  every scheme but docker, where it also requires the same suffix and the
  same number of components: `isCompatible("22-alpine3.21", "22-alpine3.20")`
  is **false**, and so is `isCompatible("1.2", "1.0.0")`. A compatibility
  move is therefore never a candidate; it needs its own update type.
- `isVersion` differs from `isValid` exactly where a scheme has ranges:
  `semver-partial` says `isValid("1")` but not `isVersion("1")`, and
  `isGreaterThan("2.0.0", "1")` is **false**. A rolling major pin is a range
  to be satisfied, not a version to be compared - the planner resolves it to
  the highest admitted release first.

**`semver-partial` is subtler than it looks.** `matches("1.10.17", "1")` is
true while `matches("1.22", "1")` is false: a two-part version is not a
comparable version even though its major matches. Eight corpus vectors use this
scheme, and it is what the `@N` rolling-major notification reads.

**apk revisions order correctly** — `1.2.3-r5 > 1.2.3-r4`, `8.5.10-r0 >
1.27.0-r1` — which is exactly what Repology could not supply and the sidecar
exists to work around. Note the edge: `isGreaterThan("1.0.0-rc.1", "1.2.3-r4")`
is true, so a pre-release marker interacts with revisions in a way worth
matching rather than reasoning about.

## Regenerating

```sh
docker run --rm -v "$PWD/tools/capture:/probe:ro" --entrypoint node \
  renovate/renovate:<version> /probe/versioning-probe.cjs <scheme>...
```

A new capture goes in a new `renovate-<version>/` directory. It is an addition
reviewed as a directory diff, never an overwrite.
