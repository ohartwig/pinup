#!/bin/sh
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
# SPDX-License-Identifier: MIT
#
# Captures the direct resolution of the fixture root's default.json -
# resolveConfigPresets over the file - as presets/default-resolved.json, and
# --print-config with the file as the global configuration AND as the
# repository's renovate.json as presets/runner-and-repo-resolved.json (the
# shape a repository extending the runner has). Both are Renovate's full
# expansions - they carry its preset data - and go under testdata/upstream/
# <root>, outside the public mirror; PINUP_FIXTURES (default
# testdata/estate) names whose configuration. Refuses to overwrite.
set -eu

IMAGE="renovate/renovate:43.288.0"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
FIX="${PINUP_FIXTURES:-testdata/estate}"
CFG="$ROOT/$FIX/config"
OUT="$ROOT/testdata/upstream/$(basename "$FIX")/renovate-43.288.0/presets"

for f in default-resolved.json runner-and-repo-resolved.json; do
  if [ -e "$OUT/$f" ]; then
    echo "refusing to overwrite $OUT/$f; delete it first if a re-capture is intended" >&2
    exit 1
  fi
done
mkdir -p "$OUT"

# colima shares only $HOME and the projects volume, so stage under $HOME.
STAGE="${HOME}/.cache/pinup-probe/resolve"
rm -rf "$STAGE" && mkdir -p "$STAGE/repo"
cp "$HERE/resolve-config-probe.mjs" "$STAGE/"

docker run --rm -e LOG_LEVEL=fatal -v "$STAGE:/probe:ro" -v "$CFG:/cfg:ro" --entrypoint node "$IMAGE" \
  /probe/resolve-config-probe.mjs /cfg/default.json \
  | python3 -c "import json,sys;d=json.load(sys.stdin);json.dump(d['config'],open('$OUT/default-resolved.json','w'),indent=1,sort_keys=True);open('$OUT/default-resolved.json','a').write('\n')"
echo "wrote $OUT/default-resolved.json" >&2

# The repository: the file itself as renovate.json, one Containerfile so the
# run has something to look at.
cp "$CFG/default.json" "$STAGE/repo/renovate.json"
printf 'FROM alpine:3.21\n' > "$STAGE/repo/Containerfile"
git -C "$STAGE/repo" init -q .
git -C "$STAGE/repo" add -A
git -C "$STAGE/repo" -c user.email=capture@example.invalid -c user.name=capture \
    -c commit.gpgsign=false commit -q -m init
docker run --rm \
  -v "$CFG:/cfg:ro" -v "$STAGE/repo:/repo" \
  -e RENOVATE_CONFIG_FILE=/cfg/default.json -e RENOVATE_PLATFORM=local \
  -e LOG_LEVEL=debug -e LOG_FORMAT=json -e TZ=Europe/Berlin -w /repo \
  "$IMAGE" --print-config --dry-run=full 2>/dev/null \
  | python3 -c "
import json,sys
full=None
for line in sys.stdin:
    try: d=json.loads(line)
    except ValueError: continue
    if str(d.get('msg','')).startswith('Full resolved config'): full=d.get('config')
if full is None: sys.exit('no resolved config in the capture')
json.dump(full,open('$OUT/runner-and-repo-resolved.json','w'),indent=1,sort_keys=True);open('$OUT/runner-and-repo-resolved.json','a').write('\n')"
echo "wrote $OUT/runner-and-repo-resolved.json" >&2
