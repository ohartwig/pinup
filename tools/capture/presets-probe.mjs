// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0
//
// Records what Renovate's preset resolver answers for each preset name in the
// transitive closure of a configuration's `extends`. It RUNS the program; it
// does not read it. Two things are recorded per preset: the definition the
// resolver hands back (`getPreset`), and the fully resolved form
// (`resolveConfigPresets` over `{extends: [name]}`), so that pinup's own
// resolver can be checked against both the parts and the whole.
//
// Usage inside the container: node presets-probe.mjs <name>...
// Output: JSON on stdout.

import { init as initLogger } from '/usr/local/renovate/dist/logger/index.js';
import { getPreset, resolveConfigPresets } from '/usr/local/renovate/dist/config/presets/index.js';

initLogger();

const roots = process.argv.slice(2);
const out = { source: 'executed in renovate/renovate:43.288.0: config/presets getPreset + resolveConfigPresets', presets: {} };

const queue = [...roots];
while (queue.length) {
  const name = queue.shift();
  if (name in out.presets) continue;
  let def;
  try {
    def = await getPreset(name, {});
  } catch (e) {
    out.presets[name] = { error: String(e && e.message || e).slice(0, 200) };
    continue;
  }
  out.presets[name] = { definition: def };
  for (const ext of def?.extends ?? []) {
    if (!(ext in out.presets)) queue.push(ext);
  }
  // Nested extends inside packageRules count too: group:monorepos is a
  // list of rules each extending monorepo:<name>.
  for (const rule of def?.packageRules ?? []) {
    for (const ext of rule?.extends ?? []) {
      if (!(ext in out.presets)) queue.push(ext);
    }
  }
}

for (const name of Object.keys(out.presets)) {
  if (out.presets[name].error) continue;
  try {
    const resolved = await resolveConfigPresets({ extends: [name] }, {});
    out.presets[name].resolved = resolved;
  } catch (e) {
    out.presets[name].resolveError = String(e && e.message || e).slice(0, 200);
  }
}

process.stdout.write(JSON.stringify(out, null, 1));
