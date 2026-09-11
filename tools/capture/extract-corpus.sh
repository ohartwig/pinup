#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
# SPDX-License-Identifier: MIT
#
# Captures real dependency vectors by running the pinned Renovate container's
# extraction over copies of real estate repositories.
#
# Why real repositories rather than hand-written fixtures: a fixture contains
# what its author thought to include. This corpus produced a versioning scheme
# that was not on the plan's list (semver-partial) and a defect in the estate
# config on its first run - neither of which anyone would have invented.
#
#   tools/capture/extract-corpus.sh <repo-path>...
#
# CONFIG_NAME picks another file from testdata/parity/config (default.json
# by default) - gomod.json enables the one manager the runner never does.
#
# NOTE ON MOUNTS: colima shares only $HOME and /Volumes/Samsung_X5. A corpus
# staged anywhere else mounts EMPTY inside the container, and Renovate then
# reports "Found 0 package file(s)" - which reads exactly like a repository
# with no dependencies. The staging directory below is under $HOME for that
# reason.
set -euo pipefail

VERSION="${RENOVATE_VERSION:-43.288.0}"
IMAGE="renovate/renovate:${VERSION}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${ROOT}/testdata/parity/renovate-${VERSION}/extract"
CFG="${ROOT}/testdata/parity/config"
STAGE="${HOME}/.cache/pinup-corpus"

[ $# -gt 0 ] || { echo "usage: $0 <repo-path>..." >&2; exit 2; }
mkdir -p "$OUT" "$STAGE"

for src in "$@"; do
  name="$(basename "$src")"
  dst="${STAGE}/${name}"
  rm -rf "$dst"; mkdir -p "$dst"
  git -C "$src" archive HEAD | tar -x -C "$dst"

  # The repo's own renovate.json extends local>devops/renovate-runner, which
  # needs platform access. default.json is supplied as the config file instead,
  # so the extends is dropped and the per-repo overrides kept. A substitution,
  # recorded here rather than done silently.
  if [ -f "${dst}/renovate.json" ]; then
    python3 -c "
import json,sys
p=sys.argv[1]; d=json.load(open(p))
d.pop('extends',None); d.pop('\$schema',None)
json.dump(d, open(p,'w'), indent=2)" "${dst}/renovate.json"
  fi

  git -C "$dst" init -q .
  git -C "$dst" add -A
  # gpgsign off: the staging repo is a throwaway scaffold for the container to
  # read, and inheriting the global signing config would ask for a YubiKey PIN
  # once per corpus entry.
  git -C "$dst" -c user.email=capture@example.invalid -c user.name=capture \
          -c commit.gpgsign=false commit -q -m corpus

  log="$(mktemp)"
  docker run --rm \
    -v "${CFG}:/cfg:ro" -v "${dst}:/repo" \
    -e RENOVATE_CONFIG_FILE=/cfg/${CONFIG_NAME:-default.json} -e RENOVATE_PLATFORM=local \
    -e LOG_LEVEL=debug -e LOG_FORMAT=json -e TZ=Europe/Berlin -w /repo \
    "$IMAGE" --dry-run=extract > "$log" 2>&1

  python3 - "$log" "${OUT}/${name}.json" <<'PY'
import json, sys
src, dst = sys.argv[1], sys.argv[2]
pf = None
for line in open(src, errors="replace"):
    line = line.strip()
    if not line:
        continue
    try:
        d = json.loads(line)
    except ValueError:
        continue
    if d.get("msg") == "Extracted dependencies":
        pf = d.get("packageFiles")
if not pf:
    sys.exit(f"{dst}: no extraction in the log - check the mount is visible in the VM")
with open(dst, "w") as f:
    json.dump(pf, f, indent=2, sort_keys=True)
    f.write("\n")
n = sum(len(f.get("deps", [])) for m in pf.values() for f in m)
print(f"  {dst.split('/')[-1]:24s} {n:4d} deps")
PY
  rm -f "$log"
done
