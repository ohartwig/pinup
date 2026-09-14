# Synthetic runner configuration

`default.json` is the twin of a real runner configuration for a fictional
estate on `git.acme.test`: the same eleven presets, the same ten enabled
managers, twenty-five custom managers and forty-nine package rules of the
same shapes - templated package names with a nested Handlebars conditional,
JSONata transforms in custom datasources, cron and natural-language
schedules, groups, release age, allow- and deny-lists, post-upgrade tasks,
`matchCurrentValue` on rolling majors - with generic names in place of the
original's. Its descriptions are derived from the rules' own content, so
nothing of the original's incident history travels with it.

It is *authored*, not captured: `SOURCE-COMMIT` says so, and the checksum
beside it pins the bytes the captures under `../renovate-<version>/` were
made from. Change the file and the captures are stale; recapture them in the
pinned container with `PINUP_FIXTURES=testdata/public tools/capture/*.sh`.

`gomod.json` enables the one manager the runner never does, for the
synthetic gomod tree under `testdata/renovate/synthetic/gomod`.
