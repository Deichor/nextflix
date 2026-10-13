#!/usr/bin/env bash
# Runs adb in a container with host networking. One adb server container (nextflix-adb) stays up, so
# 'adb connect' survives between calls; its key lives in tv/android/.adb (gitignored), so the TV asks
# to allow this computer only once. Only tv/android/out is mounted (not the sources or .signing); APK
# paths are relative to tv/android.
#   adb.sh connect 192.168.1.50:5555
#   adb.sh install -r out/nextflix-tv-0.1.0.apk
#   adb.sh logcat -s Nextflix
#   adb.sh stop          stops the adb server container
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh devices
set -euo pipefail
source "$(dirname "$0")/common.sh"
ensure_image
NAME="nextflix-adb"

if [ "${1:-}" = "stop" ]; then
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  exit 0
fi

mkdir -p "$DIR/.adb" "$DIR/out"
if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" != "true" ]; then
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker run -d --name "$NAME" --network host \
    -v "$DIR/.adb:/root/.android" -v "$DIR/out:/work/out:ro" -w /work \
    "$IMAGE" adb server nodaemon >/dev/null
  sleep 1
fi

tty=(-i)
[ -t 0 ] && [ -t 1 ] && tty=(-it)
docker exec "${tty[@]}" -w /work "$NAME" adb "$@"
