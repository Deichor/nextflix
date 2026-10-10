#!/usr/bin/env bash
# Compiles the Go server inside a throwaway container (source mounted read-only).
# Usage: check-backend.sh [packages]   (default: ./...)
# Prints GO_BUILD=ok|failed, then gofmt offenders (GOFMT_FILES=<n>) and VET=ok|failed.
# Exits non-zero when the build or vet fails.
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/scripts/check-backend.sh
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PKGS="${1:-./...}"
docker run --rm \
  -v "$ROOT/server:/src:ro" \
  -v nextflix-gomod:/go/pkg/mod \
  -v nextflix-gocache:/root/.cache/go-build \
  -e PKGS="$PKGS" \
  golang:1.27.2-trixie sh -c '
    mkdir -p /app && cd /src && tar --exclude="*.db*" -cf - . | tar -C /app -xf - && cd /app || exit 1
    rc=0
    if go build $PKGS; then echo "GO_BUILD=ok"; else echo "GO_BUILD=failed"; rc=1; fi
    files=$(gofmt -l .) ; echo "$files" | sed "/^$/d;s/^/gofmt: /"; echo "GOFMT_FILES=$(echo "$files" | sed "/^$/d" | wc -l)";
    if go vet $PKGS; then echo "VET=ok"; else echo "VET=failed"; rc=1; fi
    exit $rc'
