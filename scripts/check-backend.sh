#!/usr/bin/env bash
# Compiles the Go server inside a throwaway container (source mounted read-only).
# Usage: check-backend.sh [packages]   (default: ./...)
# Prints GO_BUILD=ok|failed, then gofmt offenders (GOFMT_FILES=<n>) and VET=ok|failed.
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/scripts/check-backend.sh
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PKGS="${1:-./...}"
docker run --rm \
  -v "$ROOT/server:/src:ro" \
  -v nextflix-gomod:/go/pkg/mod \
  -v nextflix-gocache:/root/.cache/go-build \
  -e PKGS="$PKGS" \
  golang:1.27-trixie sh -c '
    mkdir -p /app && cd /src && tar --exclude="*.db*" -cf - . | tar -C /app -xf - &&
    cd /app && { go build $PKGS && echo "GO_BUILD=ok" || echo "GO_BUILD=failed"; } &&
    files=$(gofmt -l .) ; echo "$files" | sed "/^$/d;s/^/gofmt: /"; echo "GOFMT_FILES=$(echo "$files" | sed "/^$/d" | wc -l)";
    go vet $PKGS && echo "VET=ok" || echo "VET=failed"'
