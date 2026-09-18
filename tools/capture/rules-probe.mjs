// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0
//
// Drives Renovate's packageRules resolution over real dependency vectors and
// records what it answers. It RUNS the program; it does not read it. The
// result is the differential table the Go rules engine is checked against.
//
// Input: the resolved configuration (full-resolved.json) and the extraction
// corpus. Every dependency the corpus recorded is resolved as-is and again
// under each update type Renovate distinguishes, since matchUpdateTypes is
// the most used matcher in the estate config (499 of 770 rules).
//
// Output: NDJSON - a header line, then one line per vector with the input
// fields, the indices of the rules that matched in order, and the DELTA the
// rules produced: every key whose value differs from the resolved base config. Recording the delta rather than the whole
// output keeps the table readable and makes a rule that fires by mistake
// visible as an extra key, not a changed one among three hundred.
//
// Runs inside the pinned container, where the module is installed.

import { readFileSync, readdirSync } from 'node:fs';
import { init as initLogger } from '/usr/local/renovate/dist/logger/index.js';
import { applyPackageRules } from '/usr/local/renovate/dist/util/package-rules/index.js';

// Renovate's logger buffers every record until it is initialised - and
// applyPackageRules traces the full rule list per call. Over three thousand
// calls that buffer is what ran the first attempt out of heap. LOG_LEVEL is
// set by the wrapper; initialising here is what makes it take effect.
initLogger();

const [configPath, corpusDir] = process.argv.slice(2);
const base = JSON.parse(readFileSync(configPath, 'utf8'));

// Every rule's description is replaced by its own index. `description` is a
// key mergeChildConfig accumulates, so after resolution it lists exactly the
// rules that fired, in order - which is what the harness needs to check
// "rule 33 over 32 over 0", and what --explain has to reproduce. The real
// descriptions are in full-resolved.json; repeating them per vector made the
// first table 24 MB.
base.packageRules = base.packageRules.map((r, i) => ({ ...r, description: `#${i}` }));
const baseDescriptions = base.description ?? [];

const updateTypes = [null, 'major', 'minor', 'patch', 'digest', 'pin', 'pinDigest', 'rollback', 'replacement'];

function stable(v) {
  if (v === null || typeof v !== 'object') return v;
  if (Array.isArray(v)) return v.map(stable);
  const out = {};
  for (const k of Object.keys(v).sort()) out[k] = stable(v[k]);
  return out;
}
const canon = (v) => JSON.stringify(stable(v));

process.stdout.write(JSON.stringify({
  source: 'executed in renovate/renovate:43.288.0: applyPackageRules over the extraction corpus',
  config: configPath.split('/').pop(),
  updateTypes,
  rules: base.packageRules.length,
}) + '\n');

let count = 0;
for (const file of readdirSync(corpusDir).filter((f) => f.endsWith('.json')).sort()) {
  const corpus = JSON.parse(readFileSync(`${corpusDir}/${file}`, 'utf8'));
  const repo = file.replace(/\.json$/, '');
  for (const [manager, packageFiles] of Object.entries(corpus)) {
    for (const pf of packageFiles) {
      for (const dep of pf.deps ?? []) {
        for (const updateType of updateTypes) {
          const input = {
            depName: dep.depName,
            packageName: dep.packageName ?? dep.depName,
            datasource: dep.datasource,
            currentValue: dep.currentValue,
            currentVersion: dep.currentVersion ?? dep.currentValue,
            depType: dep.depType,
            versioning: dep.versioning,
            manager,
            packageFile: pf.packageFile,
          };
          if (updateType) input.updateType = updateType;
          for (const k of Object.keys(input)) if (input[k] === undefined) delete input[k];

          const cfg = await applyPackageRules({ ...base, ...input });
          const delta = {};
          const matched = (cfg.description ?? []).slice(baseDescriptions.length)
            .map((d) => Number(String(d).replace(/^#/, '')));
          for (const k of Object.keys(cfg).sort()) {
            if (k === 'packageRules' || k === 'description') continue;
            if (k in input) {
              if (canon(cfg[k]) !== canon(input[k])) delta[k] = cfg[k];
              continue;
            }
            if (canon(cfg[k]) !== canon(base[k])) delta[k] = cfg[k];
          }
          for (const k of Object.keys(base)) {
            if (k !== 'packageRules' && !(k in cfg) && !(k in input)) delta[k] = { __deleted: true };
          }
          process.stdout.write(JSON.stringify({ repo, input, matched, delta }) + '\n');
          count++;
        }
      }
    }
  }
}
process.stderr.write(`${count} vectors\n`);
