#!/usr/bin/env bash
# Type-checks and builds the app in a container, then packages out/com.nextflix.tv_<version>_all.ipk.
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/webos/scripts/build.sh
set -euo pipefail
source "$(dirname "$0")/common.sh"

docker run --rm "${NODE_VOLUMES[@]}" "$NODE_IMAGE" bash -c "set -e; $install_cmd; npm run build"

ensure_ares_image
mkdir -p "$DIR/out"
rm -f "$DIR"/out/*.ipk
docker run --rm -v "$DIR:/work" -w /work "$ARES_IMAGE" ares-package dist -o out --no-minify
ls -l "$DIR"/out/*.ipk
