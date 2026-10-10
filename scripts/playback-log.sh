#!/usr/bin/env bash
# Prints the players' playback diagnostics (see docs/API.md), kept by the server in daily files
# (UTC) under /app/data/playback-logs for 14 days.
# Usage: playback-log.sh [DAYS] [FILTER]   (default: today; DAYS=3 prints the last 3 days;
#        FILTER picks whole sessions whose header mentions it, e.g. S02E09;
#        playback-log.sh S02E09 = today's sessions only)
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/scripts/playback-log.sh 2 S02E09
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DAYS=1
FILTER="${2:-}"
if [[ ${1:-} =~ ^[0-9]+$ ]]; then
  DAYS="$1"
elif [ -n "${1:-}" ]; then
  FILTER="$1"
fi
out="$(docker compose -f "$ROOT/deploy/docker-compose.yml" exec -T server sh -c \
  "ls /app/data/playback-logs/*.log 2>/dev/null | tail -n $DAYS | xargs -r cat")"
if [ -z "$FILTER" ]; then
  printf '%s\n' "$out"
  exit 0
fi
# Lines are "<date> <time> <session> ...": keep every line of the matching sessions
sids="$(printf '%s\n' "$out" | grep -F -- "$FILTER" | awk '{print " " $3 " "}' | sort -u)"
[ -n "$sids" ] && printf '%s\n' "$out" | grep -F -- "$sids"
