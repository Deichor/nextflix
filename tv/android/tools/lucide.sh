#!/usr/bin/env bash
# Regenerates core/.../ui/icons/Lucide.kt from lucide-react in the webOS app's node_modules volume.
# Add icon names to ICONS in lucide.mjs (tv/webos/scripts/check.sh fills the volume once).
set -euo pipefail
DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$DIR/core/src/main/kotlin/com/nextflix/tv/core/ui/icons"
mkdir -p "$OUT"
docker run --rm -v nextflix-tv-node-modules:/nm:ro -v "$DIR/tools:/tools:ro" -v "$OUT:/out" node:24-bookworm \
  node /tools/lucide.mjs
