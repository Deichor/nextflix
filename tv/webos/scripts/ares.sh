#!/usr/bin/env bash
# Runs a webOS CLI command in a container with host networking (the TV must be reachable on the LAN).
# Device list and SSH keys persist in tv/webos/.ares/ (gitignored). Paths are relative to tv/webos.
# Examples:
#   scripts/ares.sh ares-setup-device -a tv -i "host=192.168.1.50" -i "port=9922" -i "username=prisoner"
#   scripts/ares.sh ares-novacom --device tv --getkey
#   scripts/ares.sh ares-install --device tv out/com.nextflix.tv_0.1.1_all.ipk
#   scripts/ares.sh ares-launch --device tv com.nextflix.tv
set -euo pipefail
source "$(dirname "$0")/common.sh"
[ $# -gt 0 ] || { echo "usage: $0 <ares-command> [args...]" >&2; exit 2; }

ensure_ares_image
mkdir -p "$DIR/.ares/webos" "$DIR/.ares/ssh"
TTY=()
if [ -t 0 ]; then TTY=(-it); fi
docker run --rm "${TTY[@]}" --network host \
  -v "$DIR:/work" \
  -v "$DIR/.ares/webos:/root/.webos" \
  -v "$DIR/.ares/ssh:/root/.ssh" \
  -w /work "$ARES_IMAGE" "$@"
