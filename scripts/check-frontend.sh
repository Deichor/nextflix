#!/usr/bin/env bash
# Type-checks the web app inside a throwaway container (source mounted read-only).
# Usage: check-frontend.sh [REGEX]   OWN_ERRORS counts errors whose file path matches REGEX.
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/scripts/check-frontend.sh
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
docker run --rm \
  -v "$ROOT/web:/src:ro" \
  -v nextflix-npm-cache:/root/.npm \
  -e OWN_REGEX="${1:-}" \
  node:24-alpine sh -c '
    mkdir -p /app && cd /src &&
    tar --exclude=./node_modules --exclude=./.next -cf - . | tar -C /app -xf - &&
    cd /app &&
    if ! npm ci --no-audit --no-fund --loglevel=error > /tmp/npm.txt 2>&1; then tail -20 /tmp/npm.txt; echo "NPM_CI=failed"; exit 1; fi
    ./node_modules/.bin/tsc --noEmit -p . > /tmp/tsc.txt 2>&1; code=$?
    cat /tmp/tsc.txt
    if [ -n "$OWN_REGEX" ]; then
      echo "OWN_ERRORS=$(grep -E "error TS" /tmp/tsc.txt | grep -cE "$OWN_REGEX")"
    fi
    echo "TSC_EXIT=$code"'
