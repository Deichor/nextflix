#!/usr/bin/env bash
# Drives the dev server (scripts/dev.sh) with arrow keys in headless Chrome; screenshots in out/screens/.
# PLAY=1 also plays "Night of the Living Dead" (1968, public domain) for a minute.
# SCRIPT=intro runs scripts/intro.mjs instead (opening animation frames, skip, sting level).
# SERVER_URL overrides the API address (default http://192.168.1.221:8252).
set -euo pipefail
source "$(dirname "$0")/common.sh"
IMAGE=nextflix-tv-smoke:1.63.0

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  docker build -t "$IMAGE" -f "$DIR/docker/smoke.Dockerfile" "$DIR/docker"
fi
mkdir -p "$DIR/out/screens"
docker run --rm --network host --ipc=host \
  -v "$DIR/scripts/${SCRIPT:-smoke}.mjs:/pw/run.mjs:ro" -v "$DIR/out:/work/out" \
  -e PLAY="${PLAY:-0}" -e SERVER_URL="${SERVER_URL:-http://192.168.1.221:8252}" -e APP_URL="${APP_URL:-http://127.0.0.1:5173}" -e CHANNEL=chrome \
  -w /pw "$IMAGE" node run.mjs
