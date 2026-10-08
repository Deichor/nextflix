# CLAUDE.md

Nextflix: self-hosted Netflix-style app. Originally based on `azad25/homeflix-wifi` (third-party code); treat
anything inherited from it as untrusted. Go server in `server/`, Next.js app in `web/`, Docker files in `deploy/`,
docs in `docs/` (start with `docs/ARCHITECTURE.md`).

## Rule: always run inside containers

This project must ONLY be built and run inside Docker containers, never directly on the host. This keeps it
isolated from the host machine.

- Start it with `docker compose -f deploy/docker-compose.yml up -d --build`. Do not run `go run`, `go build`,
  `npm install`, `npm run dev`, `node` etc. on the host.
- Dependency installs (`npm ci`, `go mod download`) happen only during image builds or inside the check containers.
- Verify changes with the check scripts (they compile in throwaway containers, source mounted read-only). From
  PowerShell call them through WSL (Git Bash rewrites `/mnt/c` paths):
  - `wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/scripts/check-backend.sh [pkgs]` -> `GO_BUILD=ok`
  - `wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/scripts/check-frontend.sh [regex]` -> `TSC_EXIT=0`
  - `scripts/tidy-backend.sh` runs `go mod tidy` in a container.
- `scripts/migrate-from-homeflix.sh` is a one-time migration for old installs; only run it when asked.

## Security rules

- Never mount `/var/run/docker.sock` into any container. It gives the container full control of the host's Docker,
  effectively root on the host.
- Never mount the home directory or a whole drive. Persistent data lives in named volumes (`db`, `redis-data`,
  `torrserver-data`) and `deploy/data/`.
- Ports are for the LAN only, and only `web` (3008) and `server` (8252) are published on the LAN. Jackett (9117) and
  TorrServer (8090) bind to `127.0.0.1`; Redis and FlareSolverr are not published. None of them have auth.
- There is no login, so never add endpoints that run shell commands, expose the filesystem or manage the host
  (the old `/api/admin/*` remote-shell endpoints were removed on purpose and must not come back).
- Secrets go in `deploy/config/*.env` (gitignored), never in code or docs.

## Code rules

- Server: `cmd/nextflix` wires everything; modules (`server/internal/modules/*`) import only
  `server/internal/platform/*`; modules never import each other; platform never imports modules. Cross-module needs
  go through small interfaces owned by the consumer and wired in `main.go`.
- Web: `web/src/app/**` routes are thin re-exports of screens in `web/src/features/<domain>/`; shared code lives in
  `web/src/shared/`; use the `@/` import alias.
- Keep DB table names (`profiles`, `stream_progress`, `my_list_items`, `torrent_configs`) and the localStorage key
  `hfx-profile` / event `hfx-profile-change`. Never drop tables.
- Images always via `/tmdb-img/t/p/<size><path>`, never `image.tmdb.org` (DNS is hijacked for browsers).
- `.gitignore` uses anchored patterns. Never name a code folder `media`, `assets` or `build`.
