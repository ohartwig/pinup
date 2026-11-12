#!/bin/sh
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
# SPDX-License-Identifier: Apache-2.0
#
# Captures how options combine across configuration layers by running merge-probe.mjs inside
# the pinned container. Refuses to overwrite an existing table.
set -eu

IMAGE="renovate/renovate:43.288.0"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="$ROOT/testdata/renovate/renovate-43.288.0/config/merge.ndjson"

if [ -e "$OUT" ]; then
  echo "refusing to overwrite $OUT; delete it first if a re-capture is intended" >&2
  exit 1
fi

STAGE="${HOME}/.cache/pinup-probe/merge"
rm -rf "$STAGE" && mkdir -p "$STAGE"
cp "$HERE/merge-probe.mjs" "$STAGE/"

mkdir -p "$(dirname "$OUT")"
docker run --rm -e LOG_LEVEL=fatal -e LOG_FORMAT=json \
  -v "$STAGE:/probe:ro" --entrypoint node "$IMAGE" /probe/merge-probe.mjs \
  | grep '^{' > "$OUT"
echo "wrote $(wc -l < "$OUT") lines to $OUT" >&2
