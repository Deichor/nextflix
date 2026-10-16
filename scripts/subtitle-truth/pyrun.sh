#!/usr/bin/env bash
# Runs one Python step in a throwaway container as the calling user: the venv and models of the volume
# (setup.sh), this folder read-only at /scripts, WORK (the run's folder) at /work.
# Usage: WORK=<dir> pyrun.sh <script.py> [args...]   (ASR_CPUS default 16, ASR_MEM default 5g)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/common.sh"
: "${WORK:?set WORK to the run folder}"
docker run --rm --cpus="${ASR_CPUS:-16}" --memory="${ASR_MEM:-5g}" --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$TRUTH_VOLUME:/cache" -v "$here:/scripts:ro" -v "$WORK:/work" \
  -e HF_HOME=/cache/models/hf -e TORCH_HOME=/cache/models/torch -e ASR_THREADS="${ASR_CPUS:-16}" \
  -w /tmp "$TRUTH_IMAGE" /cache/venv/bin/python -I -u "/scripts/$1" "${@:2}"
