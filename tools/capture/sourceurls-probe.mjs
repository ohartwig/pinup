// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT
//
// Records how Renovate's matchSourceUrls treats a sourceUrl: which pattern
// forms it accepts, whether case matters, and what a trailing slash does on
// either side. Every pattern is tried against every URL through
// applyPackageRules itself, so the table records the matcher as it runs, not
// as it is described.
//
// Output: NDJSON - a header line, then {pattern, sourceUrl, matched} per pair.
// Runs inside the pinned container, where the module is installed.

import { init as initLogger } from '/usr/local/renovate/dist/logger/index.js';
import { applyPackageRules } from '/usr/local/renovate/dist/util/package-rules/index.js';

initLogger();

const patterns = [
  'https://github.com/foo/bar',
  'https://github.com/foo/bar/',
  'https://GitHub.com/Foo/Bar',
  'https://github.com/foo/bar.git',
  'github.com/foo/bar',
  'https://github.com/foo/*',
  'https://github.com/foo/**',
  'https://github.com/foo/bar/**',
  'https://github.com/**',
  'https://github.com/*',
  String.raw`/github\.com\/foo/`,
  String.raw`/^https:\/\/github\.com\/foo\/bar$/`,
  String.raw`/bar\/$/`,
  String.raw`/GITHUB/i`,
  '!/gitlab/',
];
const urls = [
  'https://github.com/foo/bar',
  'https://github.com/foo/bar/',
  'https://github.com/Foo/Bar',
  'https://github.com/foo/bar.git',
  'https://github.com/foo/bar/tree/main',
  'https://github.com/foo/baz',
  'https://github.com/foo',
  'https://gitlab.com/foo/bar',
  '',
  null,
];

process.stdout.write(JSON.stringify({
  source: 'executed in renovate/renovate:43.288.0: applyPackageRules with one matchSourceUrls rule per pattern',
  patterns: patterns.length,
  urls: urls.length,
}) + '\n');

for (const pattern of patterns) {
  for (const sourceUrl of urls) {
    const cfg = {
      packageRules: [{ matchSourceUrls: [pattern], enabled: false }],
      enabled: true,
      depName: 'x', packageName: 'x', datasource: 'npm', currentValue: '1.0.0',
    };
    if (sourceUrl !== null) cfg.sourceUrl = sourceUrl;
    const out = await applyPackageRules(cfg);
    process.stdout.write(JSON.stringify({ pattern, sourceUrl, matched: out.enabled === false }) + '\n');
  }
}
