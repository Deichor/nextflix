#!/usr/bin/env bash
# OCR of PGS cue bitmaps (pgs.py output) with tesseract in a throwaway container; the .deb files and apt
# lists are cached in the volume. Usage: ocr.sh <dir with cue*.pgm>; writes <name>.txt next to each.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/common.sh"
dir=$1
docker run --rm --cpus="${OCR_CPUS:-8}" --memory=2g -e HOST_ID="$(id -u):$(id -g)" \
  -v "$TRUTH_VOLUME:/cache" -v "$dir:/img" "$TRUTH_IMAGE" bash -c '
set -e
mkdir -p /cache/apt/archives/partial /cache/apt/lists/partial
O="-o Dir::Cache::Archives=/cache/apt/archives -o Dir::State::Lists=/cache/apt/lists"
apt-get $O update -qq >/dev/null 2>&1 || true
export DEBIAN_FRONTEND=noninteractive
# the version the measurements used; any trixie tesseract 5 when it is gone from the archive
apt-get $O install -y -qq --no-install-recommends tesseract-ocr=5.5.0-1+b1 tesseract-ocr-eng=1:4.1.0-2 >/dev/null 2>&1 ||
  apt-get $O install -y -qq --no-install-recommends tesseract-ocr tesseract-ocr-eng >/dev/null
tesseract --version | head -1
cd /img
ls cue*.pgm | xargs -P 8 -I{} sh -c "tesseract {} \$(basename {} .pgm) --psm 6 -l eng >/dev/null 2>&1"
chown -R "$HOST_ID" /img
'
