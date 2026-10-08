# Nextflix

A self-hosted, Netflix-style streaming app for your home network. Titles, artwork and trailers come from
[TMDB](https://www.themoviedb.org/); video comes from torrents that are streamed while they download through
[TorrServer](https://github.com/YouROK/TorrServer). Pressing **Watch** searches your Jackett indexers, picks the
best playable release and starts playing within seconds.

Nextflix was originally based on [azad25/homeflix-wifi](https://github.com/azad25/homeflix-wifi). It has since been
restructured into a small modular monolith: one Go server, one Next.js web app, and a few off-the-shelf containers.

## Features

- **Profiles** – Netflix-style "Who's watching?" picker, up to 5 profiles, no passwords.
- **Browse** – Home billboard and rows, Movies, TV Shows, New & Popular, genre filters, title detail pages with
  seasons and episodes, search with suggestions, trailers.
- **Watch** – one click: the server queries Jackett (FlareSolverr handles Cloudflare-protected indexers), ranks the
  results, hands the magnet to TorrServer and remuxes the stream to fragmented MP4 with ffmpeg so it plays in the
  browser. Seeking and audio-track selection are supported.
- **Continue Watching** – per-profile progress, per-episode progress bars, and "Play" jumps to the next episode
  once the current one is finished.
- **My List** – per profile.
- **Streaming providers** – browse what is on Netflix, Disney+, Prime Video and others (TMDB data).
- **Notifications** – a feed of new and upcoming releases, backed by Redis.
- **Settings** – Jackett indexer URL and API key with a connection test, profile management.

## Architecture in one paragraph

| Container | What it does | Port |
|---|---|---|
| `server` | Go/gin API (`server/`), SQLite in the `db` volume, ffmpeg for remuxing | 8252 |
| `web` | Next.js 15 app (`web/`), proxies `/api/*` to the server and `/tmdb-img/*` to TMDB | 3008 |
| `redis` | Cache for providers and the notification feed | internal |
| `jackett` | Torrent indexer aggregator | 127.0.0.1:9117 |
| `flaresolverr` | Solves Cloudflare challenges for Jackett | internal |
| `torrserver` | Streams torrents over HTTP while they download | 127.0.0.1:8090 |

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the module map and dependency rules and
[docs/API.md](docs/API.md) for the endpoint list.

## Quick start

Requirements: Docker with Compose v2.24 or newer (on Windows: Docker inside WSL, e.g. Ubuntu 24.04).
Everything builds and runs in containers; nothing is installed on the host.

```bash
# 1. Configuration (the real *.env files are gitignored)
cp deploy/config/server.env.example deploy/config/server.env
cp deploy/config/web.env.example    deploy/config/web.env
#    Put your TMDB v4 "API Read Access Token" into TMDB_API_KEY in server.env.

# 2. Build and start
docker compose -f deploy/docker-compose.yml up -d --build

# 3. Set up Jackett: open http://127.0.0.1:9117, add indexers, copy the API key
#    into Settings -> Indexer in Nextflix (or JACKETT_API_KEY in server.env).
```

Open `http://<your-machine>:3008`. Details, updating and the migration from an old HomeFlix install are in
[docs/DEPLOY.md](docs/DEPLOY.md); indexer setup is in [docs/jackett.md](docs/jackett.md).

## DNS note (TMDB blocked by the ISP)

Some ISPs hijack DNS for TMDB and torrent sites, even when you point the system at a public resolver such as
1.1.1.1 over plain DNS. If TMDB requests fail or titles show no artwork:

- Turn on **DNS over HTTPS** for the host (Windows: Settings -> Network -> DNS server assignment -> 1.1.1.1 /
  1.0.0.1 with "Encrypted only (DNS over HTTPS)"). Docker in WSL then resolves `api.themoviedb.org` correctly.
- Browsers on the LAN may still use the blocked DNS, so the web app never loads images from `image.tmdb.org`
  directly. All artwork goes through the `/tmdb-img/*` proxy of the `web` container
  (`/tmdb-img/t/p/w500/<file>.jpg`). Keep it that way when adding new UI.

## Development checks

Never run `go`, `npm` or `node` on the host. The check scripts compile inside throwaway containers with the source
mounted read-only (from Windows, call them through WSL as shown):

```bash
wsl -d Ubuntu-24.04 -- bash /mnt/c/<path>/scripts/check-backend.sh    # prints GO_BUILD=ok|failed
wsl -d Ubuntu-24.04 -- bash /mnt/c/<path>/scripts/check-frontend.sh   # prints tsc output and TSC_EXIT=<n>
wsl -d Ubuntu-24.04 -- bash /mnt/c/<path>/scripts/tidy-backend.sh     # go mod tidy in a container
```

`check-backend.sh` takes an optional package list and `check-frontend.sh` an optional regex that counts the type
errors in matching files (`OWN_ERRORS=<n>`).

## Security

Nextflix has no login. Keep it on your home network: only the web app (3008) and the API (8252) are published on
the LAN, Jackett and TorrServer are bound to 127.0.0.1, Redis and FlareSolverr are not published at all. Never
mount the Docker socket into any container.
