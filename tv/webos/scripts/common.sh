# Shared by the scripts in this folder (sourced, not run)
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NODE_IMAGE="node:24-bookworm"
ARES_IMAGE="nextflix-ares:3.2.6"
# node_modules lives in a named volume: fast installs and nothing on the Windows drive
NODE_VOLUMES=(-v "$DIR:/app" -v nextflix-tv-node-modules:/app/node_modules -v nextflix-npm-cache:/root/.npm -w /app)

ensure_ares_image() {
  if ! docker image inspect "$ARES_IMAGE" >/dev/null 2>&1; then
    docker build -t "$ARES_IMAGE" -f "$DIR/docker/ares.Dockerfile" "$DIR/docker"
  fi
}

install_cmd='if [ -f package-lock.json ]; then npm ci --no-audit --no-fund --loglevel=error; else npm install --no-audit --no-fund --loglevel=error; fi'
