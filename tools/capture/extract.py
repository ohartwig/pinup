# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
# SPDX-License-Identifier: Apache-2.0
"""Extract the artefacts from a recorded Renovate run.

Reads the NDJSON debug log the container produced and writes the pieces the
parity tests consume. It records what the program did; it does not read the
program.
"""
import datetime
import hashlib
import json
import os
import sys

src = sys.argv[1]
out = os.environ["OUT"]
upstream = os.environ["UPSTREAM"]
cfg = os.environ["CFG"]

full = host_rules = visited = None
for line in open(src, errors="replace"):
    line = line.strip()
    if not line:
        continue
    try:
        d = json.loads(line)
    except ValueError:
        continue
    msg = d.get("msg", "")
    if msg.startswith("Full resolved config"):
        full, host_rules = d.get("config"), d.get("hostRules")
    elif msg.startswith("Resolved shallow config") and d.get("visitedPresets"):
        visited = d["visitedPresets"]

if full is None:
    sys.exit("no resolved config in the capture - did Renovate fail early?")


def write(name, obj, base=None):
    path = os.path.join(base or out, name)
    with open(path, "w") as f:
        json.dump(obj, f, indent=2, sort_keys=True, ensure_ascii=False)
        f.write("\n")
    print(f"  {os.path.getsize(path):8d}  {name}")


# The full expansion carries Renovate's preset data (its rules, its custom
# managers, its descriptions) and stays out of the public tree. What the
# tests compare against is the rest: every option as the container resolved
# it. ignorePaths is the one option the onboarding config:recommended
# overrode in this run (testdata/renovate/.../presets/README.md); the file's
# own value is restored from the direct resolution when that capture exists,
# so run tools/capture/resolve-config.sh first.
write("full-resolved.json", full, upstream)
options = {k: v for k, v in full.items() if k not in ("packageRules", "customManagers", "description")}
direct = os.path.join(upstream, "presets", "default-resolved.json")
if os.path.exists(direct):
    with open(direct) as f:
        options["ignorePaths"] = json.load(f).get("ignorePaths", options.get("ignorePaths"))
else:
    print("  (no direct resolution yet; ignorePaths is the onboarding value - rerun after resolve-config.sh)")
write("resolved-options.json", options)
write("host-rules.json", host_rules or [])
write("visited-presets.json", visited or {})

raw = open(os.path.join(cfg, "default.json"), "rb").read()
write("provenance.json", {
    "renovateVersion": os.environ["VERSION"],
    "imageRef": os.environ["IMAGE"],
    "imageDigest": os.environ["DIGEST"],
    "configSha256": hashlib.sha256(raw).hexdigest(),
    "configSourceCommit": open(os.path.join(cfg, "SOURCE-COMMIT")).read().strip(),
    "capturedAt": datetime.datetime.now(datetime.timezone.utc)
                  .replace(microsecond=0).isoformat(),
    "capturedBy": os.environ.get("CI_JOB_URL", "tools/capture/print-config.sh, run locally"),
    "tz": "Europe/Berlin",
    "method": "executed the pinned container and recorded its output; "
              "nothing copied from the Renovate source tree",
    "partitions": ["default"],
})
