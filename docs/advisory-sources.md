<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Advisory sources

A security fix in pinup starts with an advisory: a record that a version of
a package is vulnerable and which version fixes it. pinup asks more than one
source for them, merges what they answer, and says per dependency which
sources were asked - or why none was ([`advisoryCoverage`](plan-format.md#advisorycoverage)).

## The sources

| Source | `sources` name | Asked for | Since |
|---|---|---|---|
| [OSV](https://osv.dev) | `osv` | every datasource with an OSV ecosystem: npm, packagist, go, pypi, maven, crate, rubygems, nuget; a custom datasource whose configuration names its ecosystem (`customDatasources.<name>.osvEcosystem`, e.g. `Wolfi` for apk pins) | 0.1.0; custom ecosystems 0.56.0 |
| [Packagist](https://packagist.org/apidoc#list-security-advisories) | `packagist` | composer packages (`packagist`), beside OSV | 0.55.0 |
| a withdrawal list | — (`withdrawal`) | apk pins whose mirror publishes `withdrawn.json`; images on the registry the installation's `PINUP_WITHDRAWN_IMAGES` list names | apk 0.50.0, images 0.51.0 |

All of them are switched on by `osvVulnerabilityAlerts` and off by
`vulnerabilityAlerts.enabled: false`. A source that cannot be reached is a
warning on the run, never a failed run: the others still answer, and the
ordinary updates still happen.

A withdrawal list is not an advisory database: it does not say a version is
vulnerable, it says its publisher took it back. pinup moves a dependency off
a withdrawn version the way it moves one off an advisory, and counts a
dependency only a withdrawal list speaks for as `withdrawal`, not as covered
by advisories.

## How their answers merge

Every source answers per dependency at its current version - the locked
version where there is a lock, the lowest release a range admits otherwise.

- **One advisory, listed once.** An advisory a finding already carries under
  any of its ids - OSV id, CVE, GHSA, Packagist's PKSA - is not listed again
  from a later source. OSV is asked first; Packagist adds only what OSV lacks.
- **One bound.** The fix is the highest of the fixes the advisories name for
  the range that contains the current version: the lowest release that is out
  of every advisory's reach. Packagist names no fix, only the affected range;
  pinup derives it as the lowest known release the range no longer covers.
- **One path.** A finding from any source takes the same way: its own
  branch under `vulnerabilityAlerts`, the `security` label, no release age,
  no schedule, no dashboard approval, no merge-request limits - and CISA's
  [Known Exploited Vulnerabilities](configuration.md#known-exploited-vulnerabilities)
  catalog, matched by the advisory's CVE aliases, puts it first.

## Why more than one source

Because one is not fast enough for everything. Measured for TYPO3's core
security advisories (TYPO3-CORE-SA), which TYPO3 publishes with the patch
release and Packagist lists within 0-2 days:

| Advisory | Released | In OSV |
|---|---|---|
| TYPO3-CORE-SA-2026-015 | 2026-06-09 | 3.4 days later |
| TYPO3-CORE-SA-2026-020 | 2026-07-14 | 48 days later |
| TYPO3-CORE-SA-2026-021 | 2026-08-11 | 21 days later |
| TYPO3-CORE-SA-2026-022 (CVE-2026-77132) | 2026-09-08 | not at all, as of 2026-10-05 |

With OSV alone, a TYPO3 security patch waited out its release age like any
routine update, or was merged by hand. With Packagist beside it, the fix
opens on the run after the advisory appears.

## What no source covers

Whatever no source is asked about is stated, never left to look like "no
advisory": per dependency in the plan (`state: none` with its reason), and
summed up on the dashboard and in the advisory watch - "covered by a
withdrawal list only: 68 (custom.koh-apk 68); not covered by advisories: 219
(docker 219, …)". That gap is what the next source has to close.
