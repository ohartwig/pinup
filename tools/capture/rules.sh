#!/bin/sh
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
# SPDX-License-Identifier: MIT
#
# Captures Renovate's packageRules resolution over the extraction corpus by
# running rules-probe.mjs inside the pinned container. Output is NDJSON.
#
# Refuses to overwrite an existing table: a captured behaviour table is a
# golden file, and golden files change by deliberate deletion, not by re-run.
set -eu

IMAGE="renovate/renovate:43.288.0"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
PARITY="$ROOT/testdata/parity/renovate-43.288.0"
OUT="$PARITY/rules/vectors.ndjson"

if [ -e "$OUT" ]; then
  echo "refusing to overwrite $OUT; delete it first if a re-capture is intended" >&2
  exit 1
fi

# colima shares only $HOME and the projects volume, so stage under $HOME.
STAGE="${HOME}/.cache/pinup-probe/rules"
rm -rf "$STAGE" && mkdir -p "$STAGE/corpus"
cp "$HERE/rules-probe.mjs" "$STAGE/"
cp "$PARITY/full-resolved.json" "$STAGE/"
cp "$PARITY"/extract/*.json "$STAGE/corpus/"

mkdir -p "$(dirname "$OUT")"
# LOG_LEVEL=fatal: the probe initialises Renovate's logger so this takes
# effect; without it every trace record is buffered and the run exhausts
# the heap.
docker run --rm -e LOG_LEVEL=fatal -e LOG_FORMAT=json \
  -v "$STAGE:/probe:ro" --entrypoint node "$IMAGE" \
  --max-old-space-size=4096 /probe/rules-probe.mjs /probe/full-resolved.json /probe/corpus \
  > "$OUT"
echo "wrote $(wc -l < "$OUT") lines to $OUT" >&2
