// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT
//
// Records resolveConfigPresets over a configuration file itself - the direct
// resolution, presets expanded and the file's own keys last, without the
// onboarding defaults --print-config lays over a repository. Runs inside the
// pinned container, where the module is installed.
//
//   node resolve-config-probe.mjs /cfg/default.json
import { readFileSync } from 'node:fs';
import { init as initLogger } from '/usr/local/renovate/dist/logger/index.js';
import { resolveConfigPresets } from '/usr/local/renovate/dist/config/presets/index.js';

initLogger();
const config = JSON.parse(readFileSync(process.argv[2], 'utf8'));
const resolved = await resolveConfigPresets(config, {});
process.stdout.write(JSON.stringify(resolved, null, 1));
