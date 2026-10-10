#!/usr/bin/env bash
# Type-checks and builds the app in a container, then packages out/com.nextflix.tv_<version>_all.ipk.
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/webos/scripts/build.sh
set -euo pipefail
source "$(dirname "$0")/common.sh"

# The server trusts the packaged app's "Origin: null" requests only with this per-install key.
# It lives in deploy/config/server.env (gitignored) and is created on the first build.
ENV_FILE="$DIR/../../deploy/config/server.env"
key=""
if [ -f "$ENV_FILE" ]; then
  key="$(sed -n 's/^TV_APP_KEY=//p' "$ENV_FILE" | tail -n 1 | tr -d '\r"'"'"' ')"
fi
if [ -z "$key" ]; then
  key="$(od -An -tx1 -N24 /dev/urandom | tr -d ' \n')"
  if [ -f "$ENV_FILE" ] && grep -q '^TV_APP_KEY=' "$ENV_FILE"; then
    sed -i "s/^TV_APP_KEY=.*/TV_APP_KEY=$key/" "$ENV_FILE"
  else
    printf '\nTV_APP_KEY=%s\n' "$key" >> "$ENV_FILE"
  fi
  echo "Created TV_APP_KEY in deploy/config/server.env. Restart the server so it accepts this build:"
  echo "  docker compose -f deploy/docker-compose.yml up -d"
fi

docker run --rm -e VITE_TV_APP_KEY="$key" "${NODE_VOLUMES[@]}" "$NODE_IMAGE" bash -c "set -e; $install_cmd; npm run build"

ensure_ares_image
mkdir -p "$DIR/out"
rm -f "$DIR"/out/*.ipk
docker run --rm -v "$DIR:/work" -w /work "$ARES_IMAGE" ares-package dist -o out --no-minify
ls -l "$DIR"/out/*.ipk
