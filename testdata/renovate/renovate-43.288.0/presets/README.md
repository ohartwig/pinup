# Preset closure

`closure.json` records, for every preset in the transitive closure of
`default.json`'s `extends` (1085 names), two things the pinned container's
resolver answered - captured by **executing** it (`tools/capture/presets.sh`,
`presets-probe.mjs`), nothing copied from the Renovate source tree:

- `definition` - what `getPreset(name)` hands back, the preset as the resolver
  sees it before its own `extends` are expanded;
- `resolved` - what `resolveConfigPresets({extends: [name]})` produces, with
  the list of presets it visited.

`config/preset/library.json` is generated from the definitions by
`tools/presetgen` (a test asserts the two never drift), and the resolver in
`config/preset` is checked against every `resolved` entry.

Each fixture root's `presets/default-resolved.json` is `resolveConfigPresets`
over that root's `default.json` itself (`tools/capture/resolve-config.sh`):
the 771 rules and every top-level key, as the acceptance surface for
`print-config` parity. The paragraphs below were measured on the estate's
root; the public twin, of the same shape, resolves the same way.

## What the capture settled

**Descriptions have a rule nobody would guess.** Measured over all 152
parent/child pairs in the closure: a nested preset that has a description of
its own drops the descriptions of children whose definition carries
`packageRules`; every other child's description is appended. So
`workarounds:all` describes itself only, `config:recommended` - no description
of its own - lists its children's, and the top-level configuration keeps every
child's description regardless. `packageRules`, `customManagers` and
`description` concatenate; everything else, arrays and objects included, is
replaced by the later value.

**`full-resolved.json` is not `default.json` resolved.** Its `ignorePaths` is
`:ignoreModulesAndTests`'s list, not the file's `**/prometheus-exporter/**`.
`--print-config` in the capture ran against a repository without its own
`renovate.json`, and Renovate applied its default onboarding configuration -
`extends: ["config:recommended"]` - *over* the global file. Every estate
repository carries `extends: ["local>devops/renovate-runner"]`, so in
production the file's value wins; but a repository that loses its
`renovate.json` silently loses the runner's ignore list too. The direct
resolution in `default-resolved.json` has the file's value.

**Production is the file twice, and it makes no difference.**
`runner-and-repo-resolved.json` is `--print-config` with `default.json` as the
global configuration *and* as the repository's `renovate.json` - the shape every
estate repository has through `extends: ["local>devops/renovate-runner"]`. It
holds **1540** rules in four segments: the global file's own 48, the global
file's 722 preset rules, the repository's 722 preset rules, the repository's
own 48. Since a later rule wins, the last segment decides every conflict, and
that is exactly the order of the direct resolution: presets, then the file's
own rules. pinup therefore resolves once, in that order, and numbers rules
0-769 - the file's rule 46 is `packageRules[768]`, where Renovate in
production would say 1538 and the earlier `--print-config` capture 46.

The same capture shows what `--print-config` migrates on the way:
`description` strings become arrays, and `minimumReleaseAge: "0"` becomes
`null`. Neither changes a decision; both are what `pinup migrate` will apply.

**Two presets are inert on purpose.** `mergeConfidence:all-badges` (from
`default.json`) and `mergeConfidence:age-confidence-badges` (through
`config:recommended`) add rules whose only effect is merge-confidence badges
fetched from developer.mend.io. pinup carries the rules - so the resolved
`packageRules` keep Renovate's numbering, which the harness, `--explain` and
the shadow comparator all speak in - but the keys they set have no effect, and
resolving either preset warns with the reason.
