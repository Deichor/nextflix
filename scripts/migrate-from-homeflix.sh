#!/usr/bin/env bash
# One-time migration from the old homeflix-wifi compose stack to the nextflix stack.
# Safe to run more than once. It never deletes Docker volumes.
# Run inside WSL/Linux with Docker: bash scripts/migrate-from-homeflix.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

OLD_PROJECT=homeflix-wifi
NEW_COMPOSE=deploy/docker-compose.yml

log()  { printf '[migrate] %s\n' "$*"; }
skip() { printf '[migrate] skip: %s\n' "$*"; }
warn() { printf '[migrate] WARNING: %s\n' "$*" >&2; }

command -v docker >/dev/null || { echo "docker not found" >&2; exit 1; }

volume_exists() { docker volume inspect "$1" >/dev/null 2>&1; }

# A volume counts as empty when it holds no regular files (images may pre-create empty folders)
volume_has_files() {
  [ -n "$(docker run --rm -v "$1":/v:ro alpine find /v -type f -print -quit)" ]
}

# --- 1. Stop the old stack (volumes are kept) -------------------------------
log "step 1: stopping old '$OLD_PROJECT' stack"
if [ -n "$(docker ps -aq --filter "label=com.docker.compose.project=$OLD_PROJECT")" ]; then
  if ! docker compose -p "$OLD_PROJECT" --profile production --profile torrent --profile setup \
       down --remove-orphans; then
    # Older compose versions need the compose file for "down"; remove by label instead
    warn "compose down failed, removing containers by project label"
    docker ps -aq --filter "label=com.docker.compose.project=$OLD_PROJECT" | xargs -r docker rm -f
    docker network ls -q --filter "label=com.docker.compose.project=$OLD_PROJECT" | xargs -r docker network rm
  fi
  log "old containers removed (volumes kept)"
else
  skip "no containers of project '$OLD_PROJECT'"
fi

# --- 2. Env files ------------------------------------------------------------
log "step 2: env files"
mkdir -p deploy/config

if [ -f config/backend.env ] && [ ! -f deploy/config/server.env ]; then
  # REDIS_URL is set by docker-compose.yml, so the old line is dropped
  sed -e 's/homeflix-redis/redis/g' -e '/^[[:space:]]*REDIS_URL=/d' config/backend.env > deploy/config/server.env
  rm config/backend.env
  log "moved config/backend.env -> deploy/config/server.env (homeflix-redis -> redis, REDIS_URL dropped)"
  for k in GEMINI_API_KEY OPENSUB_API_KEY; do
    if grep -q "^$k=" deploy/config/server.env; then
      log "note: $k in server.env is no longer used and can be removed"
    fi
  done
elif [ -f config/backend.env ]; then
  warn "both config/backend.env and deploy/config/server.env exist; left both untouched"
else
  skip "config/backend.env not found"
fi

if [ -f config/frontend.env ] && [ ! -f deploy/config/web.env ]; then
  sed -e '/^[[:space:]]*SETTINGS_PIN=/d' config/frontend.env > deploy/config/web.env
  rm config/frontend.env
  log "moved config/frontend.env -> deploy/config/web.env (SETTINGS_PIN removed)"
elif [ -f config/frontend.env ]; then
  warn "both config/frontend.env and deploy/config/web.env exist; left both untouched"
else
  skip "config/frontend.env not found"
fi

for f in server web; do
  if [ ! -f "deploy/config/$f.env" ]; then
    cp "deploy/config/$f.env.example" "deploy/config/$f.env"
    warn "deploy/config/$f.env did not exist; created it from the example, fill it in"
  fi
done

if [ -d config ] && rmdir config 2>/dev/null; then log "removed empty config/"; fi

# --- 3. Jackett config -------------------------------------------------------
log "step 3: Jackett config"
if [ -d jackett-config ] && [ ! -e deploy/data/jackett ]; then
  mkdir -p deploy/data
  mv jackett-config deploy/data/jackett
  log "moved jackett-config -> deploy/data/jackett"
elif [ -d jackett-config ]; then
  warn "both jackett-config and deploy/data/jackett exist; left both untouched"
else
  skip "jackett-config not found"
fi

# --- 4. Create the new containers and volumes (nothing is started) ------------
log "step 4: creating nextflix containers and volumes (builds images if needed)"
docker compose -f "$NEW_COMPOSE" up --no-start
log "nextflix containers created"

# --- 5. Copy volume data -----------------------------------------------------
log "step 5: copying volume data"
copy_volume() {
  local old="$1" new="$2"
  if ! volume_exists "$old"; then skip "$old does not exist"; return; fi
  if ! volume_exists "$new"; then warn "$new does not exist (step 4 failed?)"; return; fi
  if volume_has_files "$new"; then skip "$new already has data"; return; fi
  docker run --rm -v "$old":/from:ro -v "$new":/to alpine cp -a /from/. /to/
  log "copied $old -> $new"
}
copy_volume "${OLD_PROJECT}_homeflix-db"      nextflix_db
copy_volume "${OLD_PROJECT}_torrserver-data"  nextflix_torrserver-data
copy_volume "${OLD_PROJECT}_redis-data"       nextflix_redis-data

# --- 6. Rename the SQLite files and fix ownership ----------------------------
log "step 6: renaming homeflix.db -> nextflix.db"
if volume_exists nextflix_db; then
  docker run --rm -v nextflix_db:/data alpine sh -c '
    cd /data
    if [ -f homeflix.db ] && [ ! -e nextflix.db ]; then
      for s in "" -wal -shm; do
        [ -e "homeflix.db$s" ] && mv "homeflix.db$s" "nextflix.db$s"
      done
      echo "[migrate] renamed homeflix.db(-wal,-shm) -> nextflix.db(-wal,-shm)"
    elif [ -f homeflix.db ]; then
      echo "[migrate] WARNING: both homeflix.db and nextflix.db exist; left both untouched" >&2
    else
      echo "[migrate] skip: no homeflix.db in nextflix_db"
    fi
    chown -R 999:999 /data
    echo "[migrate] chown -R 999:999 on nextflix_db"
  '
else
  warn "nextflix_db does not exist"
fi

# --- 7. Leftovers ------------------------------------------------------------
log "step 7: done"
if [ -d media ] && rmdir media 2>/dev/null; then log "removed empty media/"; fi
log "old volumes were NOT deleted:"
{ docker volume ls -q | grep "^${OLD_PROJECT}_" || true; } | sed 's/^/[migrate]   /'
if volume_exists "${OLD_PROJECT}_homeflix-frontend-data"; then
  log "${OLD_PROJECT}_homeflix-frontend-data only held the old settings PIN; remove it with:"
  log "  docker volume rm ${OLD_PROJECT}_homeflix-frontend-data"
fi
if [ -d downloads ]; then log "downloads/ is no longer used and can be deleted"; fi
log "start Nextflix with: docker compose -f $NEW_COMPOSE up -d"
