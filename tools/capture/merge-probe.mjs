// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0
//
// Records how the pinned container combines a parent's and a child's value
// of every object- and array-typed option when one configuration is laid
// over another - the combination resolveConfigPresets applies between
// presets and to the file's own keys. Each option is asked with synthetic
// values, never read: the probe runs the merge and classifies its output.
//
//   node merge-probe.mjs > merge.ndjson
//
// One line per option: {"name", "type", "merge"}, merge one of
//   concat  - the child's array is appended to the parent's
//   merge   - the objects combine key by key; a nested object that is not
//             itself such an option is replaced, not merged
//   replace - the child's value replaces the parent's
// then one line per edge vector: {"vector", "result"}, and last one line per
// preset vector: the same question asked of resolveConfigPresets over the
// container's own presets, so the table is known to be what resolution does.
import { init as initLogger } from '/usr/local/renovate/dist/logger/index.js';
import { getOptions } from '/usr/local/renovate/dist/config/options/index.js';
import { mergeChildConfig } from '/usr/local/renovate/dist/config/utils.js';
import { resolveConfigPresets } from '/usr/local/renovate/dist/config/presets/index.js';

initLogger();
const lines = [];
for (const o of getOptions().sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))) {
  if (o.type !== 'object' && o.type !== 'array') continue;
  let merge;
  if (o.type === 'object') {
    const r = mergeChildConfig(
      { [o.name]: { fromParent: 1, both: 'parent', nested: { fromParent: 1 } } },
      { [o.name]: { fromChild: 2, both: 'child', nested: { fromChild: 2 } } },
    )[o.name];
    if (r && r.fromParent === 1 && r.fromChild === 2) {
      merge = r.nested.fromParent === 1 ? 'deep' : 'merge';
    } else {
      merge = 'replace';
    }
  } else {
    const r = mergeChildConfig({ [o.name]: ['parent'] }, { [o.name]: ['child'] })[o.name];
    merge = JSON.stringify(r) === '["parent","child"]' ? 'concat' : 'replace';
  }
  lines.push({ name: o.name, type: o.type, merge });
}
const vectors = {
  'null child of a merge object': [{ vulnerabilityAlerts: { labels: ['a'] } }, { vulnerabilityAlerts: null }],
  'empty child of a merge object': [{ vulnerabilityAlerts: { labels: ['a'] } }, { vulnerabilityAlerts: {} }],
  'null child of a concat array': [{ ignoreDeps: ['a'] }, { ignoreDeps: null }],
  'empty child of a concat array': [{ ignoreDeps: ['a'] }, { ignoreDeps: [] }],
  'concat array inside a merge object': [{ vulnerabilityAlerts: { addLabels: ['a'] } }, { vulnerabilityAlerts: { addLabels: ['b'] } }],
  'merge object inside a merge object': [{ npm: { major: { a: 1 } } }, { npm: { major: { b: 2 } } }],
  'replace array inside a merge object': [{ vulnerabilityAlerts: { labels: ['a'] } }, { vulnerabilityAlerts: { labels: ['b'] } }],
  'object over a scalar': [{ vulnerabilityAlerts: true }, { vulnerabilityAlerts: { a: 1 } }],
};
for (const [vector, [parent, child]] of Object.entries(vectors)) {
  lines.push({ vector, parent, child, result: mergeChildConfig(parent, child) });
}
const presetVectors = {
  'a file over a preset that sets the same merge object': { extends: [':disableVulnerabilityAlerts'], vulnerabilityAlerts: { labels: ['security'] } },
  'a preset over a preset, same merge object': { extends: [':disableVulnerabilityAlerts', ':automergeDigest'], digest: { labels: ['digest'] } },
};
for (const [vector, config] of Object.entries(presetVectors)) {
  const r = (await resolveConfigPresets(config, {})).config;
  lines.push({ vector, config, result: { vulnerabilityAlerts: r.vulnerabilityAlerts, digest: r.digest } });
}
for (const l of lines) process.stdout.write(JSON.stringify(l) + '\n');
