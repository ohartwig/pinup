// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT
//
// Drives Renovate's versioning modules over an input grid and records what
// they answer. It RUNS the program; it does not read it. The result is a table
// of facts about behaviour, which is what the Go implementation is written
// against.
//
// Runs inside the pinned container, where the modules are installed.

const path = '/usr/local/renovate/dist/modules/versioning/index.js';
const versioning = require(path);

// The grid. Generic versions plus the shapes each scheme is actually used for
// in this estate, plus every distinct currentValue the extraction corpus
// produced - real inputs beat invented ones.
const generic = [
  '1.0.0', '1.0.1', '1.1.0', '2.0.0', '0.9.0', '1.0.0-alpha', '1.0.0-alpha.1',
  '1.0.0-rc.1', 'v1.2.3', '1.2', '1', '1.2.3.4', '', 'latest', 'not-a-version',
];
const perScheme = {
  docker: ['22-alpine3.21', '22-alpine3.20', '22-bookworm', '3.21', '24.04',
           'v0.74.0', '1.27', 'latest', 'v0.32.2-rootless', '8.5.10-r0'],
  composer: ['^8.5', '~0.9', '^5.0', '1.0.*', 'dev-main', '1.0.0-RC1',
             '1.0.0-beta2', '>=1.0 <2.0', '13.4.*', '^13.4'],
  apk: ['1.2.3-r4', '1.2.3-r5', '1.27.0-r1', '8.5.10-r0', '1.2.3'],
  npm: ['^1.2.3', '~1.2.3', '1.x', '>=1.0.0 <2.0.0', '1.2.3 || 2.0.0'],
  go: ['v1.27.0', 'v0.0.0-20260101000000-abcdef123456', 'v2.0.0+incompatible'],
  'semver-partial': ['1', '2', '3', '1.22', '1.10.17', '1.1.2'],
  loose: ['1', '2', '3', '1.33.59', '1.33.64'],
};

// A parameterised regex scheme needs inputs its own pattern can match, or the
// capture records nothing but "invalid" and proves nothing. Keyed by pattern
// so a second regex scheme gets its own grid rather than inheriting this one.
const perPattern = {
  'alpine': ['alpine3.21', 'alpine3.20', 'alpine3.9', 'alpine4.0',
             'alpine3', 'alpine', '3.21', 'bookworm'],
};

const ranges = {
  'alpine': ['alpine3.21', 'alpine3'],
  semver: ['1.0.0', '^1.0.0', '~1.0.0', '>=1.0.0'],
  docker: ['22', '3.21', 'v0.74.0'],
  composer: ['^8.5', '~0.9', '^13.4'],
  npm: ['^1.2.3', '~1.2.3', '1.x'],
  apk: ['1.2.3'],
  go: ['v1.27.0'],
  loose: ['1'],
  'semver-partial': ['1'],
};

const strategies = ['replace', 'bump', 'pin', 'widen', 'update-lockfile', 'auto'];

function safe(fn) {
  try {
    const out = fn();
    return out === undefined ? null : out;
  } catch (e) {
    return { __error: String(e && e.message || e).slice(0, 120) };
  }
}

const schemes = process.argv.slice(2);
const result = {};

for (const name of schemes) {
  let api;
  try {
    api = versioning.get(name);
  } catch (e) {
    result[name] = { __unavailable: String(e.message).slice(0, 120) };
    continue;
  }
  let extra = perScheme[name] || [];
  for (const [key, values] of Object.entries(perPattern)) {
    if (name.includes(key)) extra = [...extra, ...values];
  }
  const inputs = [...generic, ...extra];
  let rs = ranges[name] || ['1.0.0'];
  for (const [key, values] of Object.entries(ranges)) {
    if (key !== name && name.includes(key)) rs = values;
  }

  const t = {
    module: name,
    source: 'executed in renovate/renovate:43.288.0',
    isValid: [], isStable: [], getMajor: [], getMinor: [], getPatch: [],
    equals: [], isGreaterThan: [], sortVersions: [],
    matches: [], getSatisfyingVersion: [], getNewValue: [],
  };

  for (const v of inputs) {
    t.isValid.push({ in: v, out: safe(() => api.isValid(v)) });
    t.isStable.push({ in: v, out: safe(() => api.isStable(v)) });
    t.getMajor.push({ in: v, out: safe(() => api.getMajor(v)) });
    t.getMinor.push({ in: v, out: safe(() => api.getMinor(v)) });
    t.getPatch.push({ in: v, out: safe(() => api.getPatch(v)) });
  }
  for (const a of inputs) {
    for (const b of inputs) {
      t.equals.push({ a, b, out: safe(() => api.equals(a, b)) });
      t.isGreaterThan.push({ a, b, out: safe(() => api.isGreaterThan(a, b)) });
      t.sortVersions.push({ a, b, out: safe(() => api.sortVersions(a, b)) });
    }
  }
  for (const r of rs) {
    for (const v of inputs) {
      t.matches.push({ version: v, range: r, out: safe(() => api.matches(v, r)) });
    }
    t.getSatisfyingVersion.push({
      versions: inputs, range: r,
      out: safe(() => api.getSatisfyingVersion(inputs, r)),
    });
    for (const s of strategies) {
      t.getNewValue.push({
        currentValue: r, rangeStrategy: s,
        currentVersion: '1.0.0', newVersion: '1.1.0',
        out: safe(() => api.getNewValue({
          currentValue: r, rangeStrategy: s,
          currentVersion: '1.0.0', newVersion: '1.1.0',
        })),
      });
    }
  }
  result[name] = t;
}

process.stdout.write(JSON.stringify(result, null, 2));
