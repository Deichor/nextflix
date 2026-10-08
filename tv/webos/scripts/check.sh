#!/usr/bin/env bash
# Type-checks the app in a container (no build output). Prints TSC_EXIT=0 on success.
set -uo pipefail
source "$(dirname "$0")/common.sh"
docker run --rm "${NODE_VOLUMES[@]}" "$NODE_IMAGE" bash -c "$install_cmd >/tmp/npm.txt 2>&1 || { tail -20 /tmp/npm.txt; exit 1; }; npx tsc --noEmit -p .; echo TSC_EXIT=\$?"
