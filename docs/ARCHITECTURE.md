# Architecture

Nextflix is a modular monolith: one Go binary (`server/`) and one Next.js app (`web/`). Each business domain lives
in its own module on both sides; shared infrastructure lives in `platform/` (server) and `shared/` (web).

```
server/                     Go 1.24, gin, gorm + SQLite, go-redis v8   (module name: nextflix)
  cmd/nextflix/main.go      wiring only
  internal/platform/        config, database, httpserver, cache, tmdb
  internal/modules/         profiles, progress, mylist, catalog, providers, notifications, streaming
web/                        Next.js 15 (App Router), React 19, TypeScript
  src/app/                  thin routes: export { default } from '@/features/...'
  src/features/<domain>/    screens, components, api/store files of one domain
  src/shared/               lib (api, profile, image), navigation, hooks, ui, types
deploy/                     docker-compose.yml, server.Dockerfile, web.Dockerfile, config/*.env.example
scripts/                    check-backend.sh, check-frontend.sh, tidy-backend.sh, migrate-from-homeflix.sh
docs/                       this folder
```

## Server

### Platform packages (`server/internal/platform/…`)

| Package | Responsibility |
|---|---|
| `config` | Reads env: `PORT` (8252), `DATABASE_URL`, `REDIS_URL`, `TMDB_API_KEY`, `TORRSERVER_URL`, `JACKETT_URL`, `JACKETT_API_KEY`, `ALLOWED_ORIGINS` |
| `database` | SQLite open with pragmas and pool settings, `Migrate(db, models...)` |
| `httpserver` | gin engine, logging/recovery, timeout middleware (skips `/api/torrent-stream/`), CORS with LAN origin check, `ProfileID(c)` from `X-User-ID`, `GET /api/health`, `http.Server` with graceful shutdown |
| `cache` | go-redis v8 wrapper; a nil `*Cache` is valid and behaves as "no cache" |
| `tmdb` | TMDB HTTP client (v4 token as Bearer), types, 24 h in-memory cache shared by catalog and notifications |

### Modules (`server/internal/modules/…`)

| Module | Responsibility | Tables |
|---|---|---|
| `profiles` | Profile CRUD, max 5, seeds "Home" on first start, delete cascades through cleaners | `profiles` |
| `progress` | Continue watching, per-episode progress | `stream_progress` |
| `mylist` | Per-profile My List (`media_id` = `"9"` + TMDB id), scoped by `X-User-ID` | `my_list_items` |
| `catalog` | TMDB lists, details, search, related titles, seasons | – |
| `providers` | Streaming-provider showcase from TMDB, cached in Redis | – |
| `notifications` | Redis-backed feed filled by TMDB-only generators | Redis |
| `streaming` | TorrServer files/probe/play (ffmpeg fMP4), auto-resolve through Jackett, indexer settings | `torrent_configs` |

A module usually has `model.go`, `repository.go`, `service.go`, `handler.go` and `routes.go` with
`func Register(api *gin.RouterGroup, s *Service)`. `catalog` and `providers` have no model or repository.

### Dependency rules

- `cmd/nextflix` imports `platform/*` and `modules/*`. It only wires things together.
- Modules import only `platform/*` (and third-party packages). **Modules never import each other.**
- `platform/*` never imports `modules/*`.
- When a module needs another module, it declares a small interface itself (the consumer owns the interface) and
  `main.go` passes the implementation in. Example: `profiles.ProfileDataCleaner` is implemented by
  `progress.Service` and `mylist.Service`, so deleting a profile also deletes its progress and list entries
  without `profiles` knowing about either module.
- Table names (`profiles`, `stream_progress`, `my_list_items`, `torrent_configs`) are stable; old tables are never
  dropped.

### Startup order (`cmd/nextflix/main.go`)

1. Load `.env` if present (optional), `config.Load()`.
2. `database.Open`, then `database.Migrate` with the models of profiles, progress, mylist and streaming.
3. `cache.New` (on error the server logs it and runs without Redis), `tmdb.New`.
4. Context cancelled by SIGINT/SIGTERM.
5. `progress`, `mylist`, then `profiles` with both as cleaners, plus `EnsureDefault`.
6. `catalog`, `providers`, `notifications` (`go Start(ctx)`, `defer Close()`), `streaming` plus `EnsureSettings`.
7. `httpserver.New`, `api := r.Group("/api")`, every module's `Register`, `httpserver.Run`.

## Web

| Feature (`web/src/features/…`) | Routes |
|---|---|
| `home` | `/` |
| `catalog` | `/movies`, `/tv-shows`, `/new-popular`, `/tmdb-movie/[id]` |
| `search` | `/search` |
| `my-list` | `/my-list` |
| `notifications` | `/notifications` |
| `profiles` | `/profiles` (and `ProfileGate`, which forces a profile pick on every page) |
| `providers` | `/providers`, `/providers/[id]` |
| `trailers` | `/trailers` |
| `settings` | `/settings` |
| `watch` | player, Watch button, seasons/episodes, Continue Watching row (used by other features) |
| `shell` | navbar and page shell |

### Rules

- Files in `src/app/` are thin: a route re-exports a screen. Screens take no props and read
  `useParams()` / `useSearchParams()`.
- Features may import `shared/*`. Imports between features are kept to the public pieces listed above
  (for example `watch` components, `shell/PageShell`, `my-list/store`); `shared/*` never imports a feature.
- All imports use the `@/` alias.
- Images always go through `/tmdb-img/t/p/<size><path>` (`shared/lib/image.ts`), never `image.tmdb.org`.
- The active profile lives in `localStorage` under `hfx-profile`; changes fire the `hfx-profile-change` event.
  API calls that are per profile send `profileHeaders()` (`X-User-ID`).

## Containers

See [DEPLOY.md](DEPLOY.md). `server` and `web` are built from `deploy/server.Dockerfile` and `deploy/web.Dockerfile`
with `server/` and `web/` as build contexts.
