#!/usr/bin/env bash
# Android TV emulator in a container (needs /dev/kvm). Watch and control it at http://127.0.0.1:6080/vnc.html
# (arrow keys = D-pad, Enter = OK, Esc = Back). The AVD lives in the volume nextflix-tv-emulator-avd.
#   emulator.sh start            builds the image on first use, boots the emulator, waits for Android
#   emulator.sh install [apk]    installs out/nextflix-tv-<version>-debug.apk (or the given path under tv/android)
#   emulator.sh launch           starts the app
#   emulator.sh shot [name]      screenshot to out/emulator/<name>.png
#   emulator.sh key <keys...>    sends key events (DPAD_UP, DPAD_CENTER, BACK, ...)
#   emulator.sh adb <args...>    any adb command against the emulator
#   emulator.sh logs | stop
# In the app, the server address is http://10.0.2.2:8252 (the emulator's alias for this machine).
set -euo pipefail
source "$(dirname "$0")/common.sh"
EMU_IMAGE="nextflix-android-emulator:1"
NAME="nextflix-tv-emulator"
APP_ID="com.nextflix.tv"

running() { [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = "true" ]; }
dadb() { docker exec "$NAME" adb -s emulator-5554 "$@"; }

case "${1:-}" in
  start)
    ensure_image
    if ! docker image inspect "$EMU_IMAGE" >/dev/null 2>&1; then
      docker build -t "$EMU_IMAGE" -f "$DIR/docker/emulator.Dockerfile" "$DIR/docker"
    fi
    if ! running; then
      docker rm -f "$NAME" >/dev/null 2>&1 || true
      mkdir -p "$DIR/out"
      # Host network so 10.0.2.2 reaches the server; every port below binds to 127.0.0.1 only
      docker run -d --name "$NAME" --network host --device /dev/kvm \
        -v nextflix-tv-emulator-avd:/root/.android -v "$DIR/out:/work/out:ro" \
        "$EMU_IMAGE" >/dev/null
    fi
    echo "waiting for Android to boot..."
    for _ in $(seq 1 120); do
      if [ "$(dadb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ]; then
        echo "BOOTED  screen: http://127.0.0.1:6080/vnc.html"
        exit 0
      fi
      running || { docker logs --tail 30 "$NAME"; exit 1; }
      sleep 3
    done
    echo "boot timed out"; docker logs --tail 30 "$NAME"; exit 1
    ;;
  install)
    apk="${2:-out/nextflix-tv-$(app_version)-debug.apk}"
    case "$apk" in out/*) ;; *) echo "APK must be under tv/android/out" >&2; exit 2 ;; esac
    dadb install -r "/work/$apk"
    ;;
  launch) dadb shell monkey -p "$APP_ID" -c android.intent.category.LEANBACK_LAUNCHER 1 >/dev/null; echo launched ;;
  shot)
    mkdir -p "$DIR/out/emulator"
    dadb exec-out screencap -p > "$DIR/out/emulator/${2:-shot}.png"
    echo "out/emulator/${2:-shot}.png"
    ;;
  key) shift; for k in "$@"; do dadb shell input keyevent "KEYCODE_$k"; done ;;
  adb) shift; dadb "$@" ;;
  logs) docker logs --tail 50 "$NAME" ;;
  stop) docker rm -f "$NAME" >/dev/null 2>&1 || true; echo stopped ;;
  *) sed -n '2,12p' "$0"; exit 2 ;;
esac
