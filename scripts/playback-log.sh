#!/usr/bin/env bash
# Prints the players' playback diagnostics (see docs/API.md), kept by the server in daily files
# (UTC) under /app/data/playback-logs for 14 days, with the server's subtitle timing lines
# (/app/data/playback-logs/timing, the same days) of the sessions' files.
# Usage: playback-log.sh [DAYS] [FILTER]   (default: today; DAYS=3 prints the last 3 days;
#        FILTER picks whole sessions whose header mentions it, e.g. S02E09, and adds the server's
#        timing lines of their files (src=, to=) from 30 min before each session to 2 min after
#        it, all in time order: player events by their own time, server lines marked "server";
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
cat_days() { # dir, days
  docker compose -f "$ROOT/deploy/docker-compose.yml" exec -T server sh -c \
    "ls $1/*.log 2>/dev/null | tail -n $2 | xargs -r cat"
}
out="$(cat_days /app/data/playback-logs "$DAYS")"
if [ -z "$FILTER" ]; then
  printf '%s\n' "$out"
  exit 0
fi
# Lines are "<date> <time> <session> ...": keep every line of the matching sessions
sids="$(printf '%s\n' "$out" | grep -F -- "$FILTER" | awk '{print " " $3 " "}' | sort -u)"
[ -z "$sids" ] && exit 0
# One day more of timing lines: a session may start before midnight
{
  printf '%s\n' "$out" | grep -F -- "$sids" | sed 's/^/P\t/'
  cat_days /app/data/playback-logs/timing "$((DAYS + 1))" | sed 's/^/S\t/'
} | awk -F '\t' -f "$ROOT/scripts/playback-merge.awk" | sort -t "$(printf '\t')" -k1,1n -k2,2n -s | cut -f3-
