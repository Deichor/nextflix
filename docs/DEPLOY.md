# Deploying Nextflix

Everything runs in Docker. Do not run `go`, `npm` or `node` on the host; images build from source.
All commands below run from the repository root (on Windows: inside WSL, e.g. `wsl -d Ubuntu-24.04`).

## Files

| Path | Purpose |
|---|---|
| `deploy/docker-compose.yml` | the stack, project name `nextflix` |
| `deploy/server.Dockerfile` | Go server, build context `server/`; runtime Debian slim + ffmpeg, user `nextflix` (UID/GID 999) |
| `deploy/web.Dockerfile` | Next.js standalone build, build context `web/` |
| `deploy/config/server.env` | secrets for the server (copy from `server.env.example`, gitignored) |
| `deploy/config/web.env` | web settings (copy from `web.env.example`, gitignored) |
| `deploy/data/jackett/` | Jackett config, bind-mounted (gitignored); override with `JACKETT_CONFIG_DIR` |

## First install

```bash
cp deploy/config/server.env.example deploy/config/server.env
cp deploy/config/web.env.example    deploy/config/web.env
# edit deploy/config/server.env: TMDB_API_KEY = TMDB v4 "API Read Access Token" (used as a Bearer token)

# The stack mounts the built TV app and refuses to start without it (tv/webos/README.md);
# without a webOS TV, `mkdir -p tv/webos/dist` is enough (tvapp then reports "unhealthy")
bash tv/webos/scripts/build.sh

docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml ps        # server and web report "healthy" after a few seconds
```

Then open Jackett at `http://127.0.0.1:9117`, add indexers and paste its API key into Nextflix under
**Settings -> Indexer** (see [jackett.md](jackett.md)). Nextflix is at `http://<host>:3008`.

### server.env

| Variable | Required | Notes |
|---|---|---|
| `TMDB_API_KEY` | yes | TMDB v4 Read Access Token |
| `JACKETT_API_KEY` | no | default for the indexer settings; the value saved in Settings wins |
| `ALLOWED_ORIGINS` | no | extra CORS origins, comma separated; localhost, private IPs and `*.local` are always allowed |
| `ALLOWED_HOSTS` | no | extra host names the server answers to (DNS rebinding guard); set the same list in `web.env` |
| `TV_APP_KEY` | for the packaged TV app | written by `tv/webos/scripts/build.sh` on the first build and embedded in the app; the server only accepts the app's `Origin: null` requests with it |

`PORT`, `DATABASE_URL` (`/app/data/nextflix.db`), `REDIS_URL`, `TORRSERVER_URL` and `JACKETT_URL` are set in the
compose file and point at the other services.

## Services, ports and volumes

| Service | Image | Published | Networks | Data |
|---|---|---|---|---|
| `server` | built | `8252` (LAN) | frontend, backend, cache | volume `db` -> `/app/data` (SQLite) |
| `web` | built | `3008`, `80` (LAN) | frontend | none |
| `tvapp` | `nginx:1.30.5-alpine` | no | frontend | `tv/webos/dist` (read-only) |
| `mdns` | built (`python:3.14.8-slim-trixie`) | `5353/udp` (host network) | host | none |
| `redis` | `redis:8.10.2-alpine` (AOF, 512 MB LRU) | no | cache | volume `redis-data` |
| `jackett` | `lscr.io/linuxserver/jackett:v0.24.2810-ls53` | `127.0.0.1:9117` | backend | `deploy/data/jackett` |
| `flaresolverr` | `ghcr.io/flaresolverr/flaresolverr:v3.5.2` | no | backend | none |
| `torrserver` | `ghcr.io/yourok/torrserver:MatriX.146` | `127.0.0.1:8090`, `41413` tcp+udp (peers) | backend | volume `torrserver-data` |

Keep the service name `flaresolverr`: Jackett's config points at `http://flaresolverr:8191`.

Every third-party image is pinned to an exact tag; update by changing the tag (Jackett's in-container
`AUTO_UPDATE` is off). Networks: `web` only reaches `server` and `tvapp` (`frontend`); Jackett, FlareSolverr and
TorrServer sit on `backend`; Redis is alone with `server` on `cache`, an internal network without internet access.
`server` waits for Redis' healthcheck (`redis-cli ping`) before it starts. Every service has a healthcheck; on
`compose up`, `server` also waits for Jackett and TorrServer, Jackett for FlareSolverr and `web` for `server`. These
waits are optional (`required: false`): an unhealthy one only logs a warning and does not block the stack.

All services use `no-new-privileges` and json-file logs capped at 3 x 10 MB. All except Jackett drop every Linux
capability; `redis` and `tvapp` add back only what their root entrypoints need to switch users. Jackett keeps the
defaults because s6-overlay starts as root and chowns `/config`. TorrServer runs as UID/GID 1000; the one-shot
`torrserver-init` hands the `torrserver-data` volume to that user before each start (only `CHOWN`, no network).

SQLite lives in a named volume on purpose: WAL and mmap are unreliable on Windows bind mounts (`/mnt/c`).

## Updating

```bash
git pull
docker compose -f deploy/docker-compose.yml up -d --build
```

Database migrations run automatically at server start (gorm AutoMigrate; tables are never dropped).
Redis data written by Redis 8 cannot be loaded by Redis 7 again, so do not roll the `redis` tag back.

## Backups

```bash
docker run --rm -v nextflix_db:/data:ro -v "$PWD":/backup alpine \
  tar czf /backup/nextflix-db.tgz -C /data .
```

## Checks without running the stack

```bash
docker compose -f deploy/docker-compose.yml config -q
bash scripts/check-backend.sh      # GO_BUILD=ok
bash scripts/check-frontend.sh     # TSC_EXIT=0
```

## Migrating from an old HomeFlix install

Older installs ran the compose project `homeflix-wifi` from the repo root with `config/backend.env`,
`config/frontend.env`, `./jackett-config` and the volumes `homeflix-wifi_homeflix-db`,
`homeflix-wifi_torrserver-data` and `homeflix-wifi_redis-data`. `scripts/migrate-from-homeflix.sh` moves all of that
over. It is idempotent (every step is skipped when it was already done) and it never deletes a volume.

```bash
bash scripts/migrate-from-homeflix.sh
docker compose -f deploy/docker-compose.yml up -d
```

What it does, in order:

1. `docker compose -p homeflix-wifi --profile production --profile torrent --profile setup down --remove-orphans`
   (no `-v`, so volumes stay). Falls back to removing the containers by their compose label.
2. Moves `config/backend.env` to `deploy/config/server.env` (`homeflix-redis` -> `redis`, the `REDIS_URL` line is
   dropped because compose sets it) and `config/frontend.env` to `deploy/config/web.env` without `SETTINGS_PIN`.
   Existing target files are never overwritten.
3. Moves `jackett-config/` to `deploy/data/jackett/`.
4. `docker compose -f deploy/docker-compose.yml up --no-start` to create the `nextflix_*` volumes
   (this builds the images if they do not exist yet).
5. Copies `homeflix-wifi_homeflix-db` -> `nextflix_db`, `homeflix-wifi_torrserver-data` -> `nextflix_torrserver-data`
   and `homeflix-wifi_redis-data` -> `nextflix_redis-data`, only when the target holds no files yet.
6. Renames `homeflix.db`, `homeflix.db-wal` and `homeflix.db-shm` to `nextflix.db*` together inside `nextflix_db`
   and runs `chown -R 999:999` on it.
7. Leaves `homeflix-wifi_homeflix-frontend-data` alone (it only held the old settings PIN) and prints how to remove
   it. The other old volumes are kept too; delete them by hand once Nextflix works:
   `docker volume rm homeflix-wifi_homeflix-db homeflix-wifi_torrserver-data homeflix-wifi_redis-data homeflix-wifi_homeflix-frontend-data`.

Unused keys from the old env file (for example `GEMINI_API_KEY`, `OPENSUB_API_KEY`) can be deleted from
`server.env`; the script points them out.
