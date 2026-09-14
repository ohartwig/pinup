# Behaviour capture

These scripts run a **pinned Renovate container** and record what it does.
Nothing here copies anything out of the Renovate tree.

That distinction is the whole licensing position of this project, so it is
worth stating plainly rather than leaving implicit:

- **Recording behaviour is fine.** An input/output pair — "given this config,
  Renovate resolves that value" — is a fact about a program, not a copy of it.
  Facts are not copyrightable expression.
- **Transcribing source is not.** No preset JSON, no fixture table, no code
  is copied from upstream. If a capture step ever finds itself reading
  Renovate's source rather than running it, that step is wrong.

Renovate is AGPL-3.0; pinup is MIT. The separation above is what makes those
two compatible here. See `docs/tech-spec.md` §0.1 and `NOTICE`.

## What is captured

| Script | Produces | Root |
|---|---|---|
| `print-config.sh` | `resolved-options.json` (every option as resolved, without rules, managers and descriptions), `host-rules.json`, `visited-presets.json`, `provenance.json`: `--print-config` over the root's `default.json`; the full expansion goes to `testdata/upstream/<root>` | `PINUP_FIXTURES` |
| `resolve-config.sh` | `presets/default-resolved.json` (the direct resolution) and `presets/runner-and-repo-resolved.json` (the file as global config and as the repository's own) under `testdata/upstream/<root>` - Renovate's full expansions, not published | `PINUP_FIXTURES` |
| `extract-corpus.sh <tree>...` | `extract/<name>.json`: what Renovate extracts from each repository, a checkout's HEAD or a plain tree such as `testdata/public/repos/*` | `PINUP_FIXTURES` |
| `rules.sh` | `rules/vectors.ndjson`: `packageRules` resolution over the corpus, on the production-shape base from `resolve-config.sh` | `PINUP_FIXTURES` |
| `presets.sh` | `presets/closure.json` under `testdata/upstream`: every preset the root's `extends` reaches, as Renovate's resolver defines it - reference only, the library is authored | `testdata/upstream` |
| `sourceurls.sh` | `rules/source-urls.ndjson`: `matchSourceUrls` over synthetic inputs | `testdata/renovate` |
| `versioning-probe.cjs` | input/output pairs per versioning module | `testdata/renovate` |

A root is captured in the order: `resolve-config.sh`, `print-config.sh`
(it reads the direct resolution for one option), `extract-corpus.sh`,
`rules.sh` (it needs the corpus and the production-shape resolution). Both
roots were captured that way on 2026-09-14.

`testdata/upstream` is where Renovate's own data lands - its preset closure,
its full expansions of a configuration. Those are AGPL and never published:
the mirror filters the directory, and no test reads it.

## Rules for every capture

1. **The version is in the path.** Output goes to
   `<fixture root>/renovate-<version>/`. A new capture is an *addition*
   reviewed as a directory diff, never an overwrite. That is how this
   repository keeps its no-golden-rewrite rule without needing an `-update`
   flag.
2. **Every snapshot carries `provenance.json`**: the Renovate version, the
   image digest, the config checksum, when it was captured and by what. A
   snapshot missing any field is rejected by a test.
3. **These scripts never run in the ordinary test job.** They need a container
   and a network; `test:go` needs neither and must stay that way.
4. **`testdata/renovate/CURRENT`** names the snapshot the tests compare against.
5. **Two roots.** What is Renovate's behaviour alone - versioning tables, the
   preset closure, the advisory captures, the synthetic repositories - lives
   in `testdata/renovate` and is the same for everyone. What depends on a
   runner configuration - its resolution, the extraction of repositories under
   it, the rule vectors, the goldens - lives in the root `PINUP_FIXTURES`
   names: `testdata/estate` by default (the author's estate, filtered out of
   the public mirror), `testdata/public` for the synthetic twin. Each root
   pins its denominators in `expect.json`; the tests read them from there.
