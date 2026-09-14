#!/bin/sh
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
# SPDX-License-Identifier: MIT
#
# Captures matchSourceUrls behaviour by running sourceurls-probe.mjs inside
# the pinned container. Refuses to overwrite an existing table.
set -eu

IMAGE="renovate/renovate:43.288.0"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="$ROOT/testdata/renovate/renovate-43.288.0/rules/source-urls.ndjson"

if [ -e "$OUT" ]; then
  echo "refusing to overwrite $OUT; delete it first if a re-capture is intended" >&2
  exit 1
fi

STAGE="${HOME}/.cache/pinup-probe/sourceurls"
rm -rf "$STAGE" && mkdir -p "$STAGE"
cp "$HERE/sourceurls-probe.mjs" "$STAGE/"

mkdir -p "$(dirname "$OUT")"
docker run --rm -e LOG_LEVEL=fatal -e LOG_FORMAT=json \
  -v "$STAGE:/probe:ro" --entrypoint node "$IMAGE" /probe/sourceurls-probe.mjs \
  | grep '^{' > "$OUT"
echo "wrote $(wc -l < "$OUT") lines to $OUT" >&2
