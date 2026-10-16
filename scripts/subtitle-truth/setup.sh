#!/usr/bin/env bash
# One-time setup: the Python venv with the pinned packages (requirements.lock) in the volume
# TRUTH_VOLUME (common.sh). Models are downloaded into the same volume on first use (about 4 GB:
# faster-whisper medium.en, wav2vec2 large, opus-mt-tc-big-tr-en). Runs in a throwaway container.
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/scripts/subtitle-truth/setup.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/common.sh"
docker volume create "$TRUTH_VOLUME" >/dev/null
docker run --rm --cpus=4 --memory=3g -v "$TRUTH_VOLUME:/cache" -v "$here/requirements.lock:/req.txt:ro" \
  -e PIP_CACHE_DIR=/cache/pip "$TRUTH_IMAGE" bash -c '
set -e
[ -x /cache/venv/bin/python ] || python -m venv /cache/venv
/cache/venv/bin/pip install -q --no-deps -r /req.txt --extra-index-url https://download.pytorch.org/whl/cpu
/cache/venv/bin/python -c "import torch, torchaudio, faster_whisper, transformers, rapidfuzz; print(\"venv ok:\", torch.__version__, faster_whisper.__version__, transformers.__version__)"
mkdir -p /cache/models /cache/apt
chmod -R a+rwX /cache/models /cache/apt
'
