#!/usr/bin/env bash
# Regenerates the bundled Inter subsets and the banner wordmark (see fonts.py) in a python container.
# The container (which installs from PyPI) only sees an empty output dir, never the project or its keys.
set -euo pipefail
DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT
mkdir -p "$OUT/core/src/main/res/font" "$OUT/app/src/main/res/drawable"
docker run --rm -v "$OUT:/android" -v "$DIR/tools:/tools:ro" -e OWNER="$(id -u):$(id -g)" python:3.14.8-slim-trixie \
  sh -c 'pip install --quiet --root-user-action=ignore fonttools==4.60.1 && python -I /tools/fonts.py /android && chown -R "$OWNER" /android'
mkdir -p "$DIR/core/src/main/res/font" "$DIR/app/src/main/res/drawable"
cp "$OUT"/core/src/main/res/font/*.ttf "$DIR/core/src/main/res/font/"
cp "$OUT/core/INTER-LICENSE.txt" "$DIR/core/"
cp "$OUT/app/src/main/res/drawable/banner.xml" "$DIR/app/src/main/res/drawable/"
