#!/usr/bin/env bash
# Runs `go mod tidy` for the server inside a container (server/ mounted read-write).
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
docker run --rm \
  -v "$ROOT/server:/app" \
  -v nextflix-gomod:/go/pkg/mod \
  -w /app golang:1.27-trixie sh -c 'go mod tidy && echo "TIDY=ok" || echo "TIDY=failed"'
