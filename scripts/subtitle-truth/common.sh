# Shared settings of the subtitle-truth scripts (sourced, not run).
# Python image (Debian trixie, Python 3.12.15) pinned by digest; the venv, the pip and apt caches and the
# models live in the volume TRUTH_VOLUME (setup.sh); media are read through the server container.
TRUTH_IMAGE="python:3.12.15-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1"
TRUTH_VOLUME="${TRUTH_VOLUME:-nfx-asr-cache}"
TRUTH_SERVER="${TRUTH_SERVER:-nextflix-server-1}"
