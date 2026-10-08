#!/usr/bin/env bash
# Desktop dev mode: Vite dev server in a container on http://127.0.0.1:5173 (arrow keys, Enter, Esc = Back).
# DETACH=1 runs it in the background (stop with: docker stop nextflix-tv-dev).
set -euo pipefail
source "$(dirname "$0")/common.sh"

docker rm -f nextflix-tv-dev >/dev/null 2>&1 || true
MODE=(--rm)
if [ "${DETACH:-0}" = "1" ]; then MODE+=(-d); elif [ -t 0 ]; then MODE+=(-it); fi
docker run "${MODE[@]}" --name nextflix-tv-dev -p 127.0.0.1:5173:5173 "${NODE_VOLUMES[@]}" "$NODE_IMAGE" \
  bash -c "set -e; $install_cmd; npm run dev"
