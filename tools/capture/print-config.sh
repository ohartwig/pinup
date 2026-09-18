#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
# SPDX-License-Identifier: Apache-2.0
#
# Captures Renovate's resolved configuration by RUNNING the pinned container
# and recording what it prints. Nothing is copied out of the Renovate source
# tree; see README.md in this directory for why that distinction is the whole
# licensing position of this project.
#
# Requires a container runtime and a network. Never run from test:go.
#
#   tools/capture/print-config.sh [renovate-version]
#
# Output goes to <fixture root>/renovate-<version>/ - the resolved options,
# the visited presets, the provenance - and REFUSES to overwrite an existing
# snapshot: a new capture is an addition, reviewed as a directory diff. That
# is how this repository keeps golden files read-only without an -update
# flag. The full expansion, which carries Renovate's preset data, goes under
# testdata/upstream/<root>, outside the public mirror.
#
# PINUP_FIXTURES picks the fixture root whose config is resolved (default
# testdata/estate; testdata/public is the synthetic twin). The version the
# tests read, testdata/renovate/CURRENT, is shared by every root.
set -euo pipefail

VERSION="${1:-43.288.0}"
IMAGE="renovate/renovate:${VERSION}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
FIX="${PINUP_FIXTURES:-testdata/estate}"
FIXTURES="${ROOT}/${FIX}"
OUT="${FIXTURES}/renovate-${VERSION}"
UPSTREAM="${ROOT}/testdata/upstream/$(basename "$FIX")/renovate-${VERSION}"
CFG="${FIXTURES}/config"

if [ -d "$OUT" ]; then
  echo "snapshot already exists: $OUT" >&2
  echo "a capture is an addition, not an overwrite - remove it deliberately if you mean to replace it" >&2
  exit 1
fi

command -v docker >/dev/null || { echo "no container runtime on PATH" >&2; exit 1; }
docker pull --quiet "$IMAGE"
DIGEST="$(docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE" | sed 's/.*@//')"

# Renovate needs a repository to operate on. It is deliberately almost empty:
# the object here is the resolved CONFIG, and a large repository would only add
# lookup traffic and minutes.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/repo"
printf 'FROM alpine:3.21\n' > "$WORK/repo/Containerfile"
git -C "$WORK/repo" init -q .
git -C "$WORK/repo" add -A
# gpgsign off: the scaffold is a throwaway for the container to read, and the
# global signing config would ask for a hardware key.
git -C "$WORK/repo" -c user.email=capture@example.invalid -c user.name=capture \
    -c commit.gpgsign=false commit -q -m init

echo "capturing ${IMAGE} (${DIGEST})..."
docker run --rm \
  -v "${CFG}:/cfg:ro" \
  -v "${WORK}/repo:/repo" \
  -e RENOVATE_CONFIG_FILE=/cfg/default.json \
  -e RENOVATE_PLATFORM=local \
  -e LOG_LEVEL=debug -e LOG_FORMAT=json \
  -e TZ=Europe/Berlin \
  -w /repo \
  "$IMAGE" --print-config --dry-run=full > "${WORK}/capture.ndjson"

mkdir -p "$OUT" "$UPSTREAM"
VERSION="$VERSION" IMAGE="$IMAGE" DIGEST="$DIGEST" OUT="$OUT" UPSTREAM="$UPSTREAM" CFG="$CFG" \
  python3 "${ROOT}/tools/capture/extract.py" "${WORK}/capture.ndjson"

echo "renovate-${VERSION}" > "${ROOT}/testdata/renovate/CURRENT"
echo "wrote ${OUT}"
ls -la "$OUT"
