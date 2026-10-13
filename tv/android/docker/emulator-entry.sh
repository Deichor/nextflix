#!/usr/bin/env bash
# Starts a virtual display, the Android TV emulator in it, and noVNC (127.0.0.1:6080) to watch and control it.
set -euo pipefail
AVD=nextflix-tv
W=1280 H=720

if [ ! -d "/root/.android/avd/$AVD.avd" ]; then
  echo no | avdmanager create avd -n "$AVD" -k "$SYSTEM_IMAGE" -d tv_720p >/dev/null
  cfg="/root/.android/avd/$AVD.avd/config.ini"
  sed -i '/^hw.ramSize=/d;/^hw.keyboard=/d;/^disk.dataPartition.size=/d' "$cfg"
  printf 'hw.ramSize=3072\nhw.keyboard=yes\ndisk.dataPartition.size=6G\n' >> "$cfg"
fi

Xvfb :99 -screen 0 "${W}x${H}x24" -nolisten tcp &
export DISPLAY=:99
sleep 1
x11vnc -display :99 -localhost -rfbport 5901 -forever -shared -nopw -quiet &
websockify --web /usr/share/novnc 127.0.0.1:6080 127.0.0.1:5901 >/dev/null 2>&1 &

exec emulator -avd "$AVD" -gpu swiftshader_indirect -no-boot-anim -no-snapshot-save \
  -no-audio -cores 4 -memory 3072
