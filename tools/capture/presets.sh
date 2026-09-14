#!/bin/sh
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
# SPDX-License-Identifier: MIT
#
# Captures the preset closure of the fixture root's configuration's
# `extends` by running presets-probe.mjs inside the pinned container, then
# writes it under testdata/upstream, outside the public mirror: the
# closure is Renovate's own preset data, kept for reference while
# config/preset/presets.json - pinup's own, authored - is checked against
# the vectors instead (config/preset/README.md). PINUP_FIXTURES (default
# testdata/estate) only says whose extends are the roots.
#
# Refuses to overwrite an existing closure: a captured behaviour table is a
# golden file, and golden files change by deliberate deletion, not by re-run.
set -eu

IMAGE="renovate/renovate:43.288.0"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
FIXTURES="$ROOT/${PINUP_FIXTURES:-testdata/estate}"
OUT="$ROOT/testdata/upstream/renovate-43.288.0/presets/closure.json"

if [ -e "$OUT" ]; then
  echo "refusing to overwrite $OUT; delete it first if a re-capture is intended" >&2
  exit 1
fi

# The roots are default.json's extends. Read them rather than listing them
# here, so a change to the file changes the capture.
ROOTS="$(python3 -c "import json;print(' '.join(json.load(open('$FIXTURES/config/default.json'))['extends']))")"

# colima shares only $HOME and the projects volume, so stage under $HOME.
STAGE="${HOME}/.cache/pinup-probe/presets"
rm -rf "$STAGE" && mkdir -p "$STAGE"
cp "$HERE/presets-probe.mjs" "$STAGE/"

mkdir -p "$(dirname "$OUT")"
# shellcheck disable=SC2086 -- the roots are separate arguments on purpose
docker run --rm -e LOG_LEVEL=fatal -v "$STAGE:/probe:ro" --entrypoint node "$IMAGE" \
  /probe/presets-probe.mjs $ROOTS \
  | python3 -c "import json,sys;d=json.load(sys.stdin);json.dump(d,open('$OUT','w'),indent=1,sort_keys=True);open('$OUT','a').write('\n')"
echo "wrote $OUT" >&2
