# Architecture

Nextflix is a modular monolith: one Go binary (`server/`) and one Next.js app (`web/`). Each business domain lives
in its own module on both sides; shared infrastructure lives in `platform/` (server) and `shared/` (web).

```
server/                     Go 1.27, gin, gorm + SQLite, go-redis v9   (module name: nextflix)
  cmd/nextflix/main.go      wiring only
  internal/platform/        config, database, httpserver, cache, tmdb, upstream, torbox
  internal/modules/         profiles, progress, ratings, recommendations, top10, catalog, providers,
                            notifications, streaming, subtitles, segments, images, playbacklog
web/                        Next.js 16 (App Router), React 19, TypeScript
  src/app/                  thin routes: export { default } from '@/features/...'
  src/features/<domain>/    screens, components, api/store files of one domain
  src/shared/               lib (api, profile, image), i18n, navigation, hooks, ui, types
tv/webos/                   LG webOS app (Vite + React + TypeScript), see tv/webos/README.md
tv/android/                 Android TV / Google TV app (Kotlin, Compose for TV, Media3), see tv/android/README.md
deploy/                     docker-compose.yml, server.Dockerfile, web.Dockerfile, config/*.env.example
scripts/                    check-backend.sh, check-frontend.sh, tidy-backend.sh, playback-log.sh, migrate-from-homeflix.sh
docs/                       this folder
```

## Server

### Platform packages (`server/internal/platform/…`)

| Package | Responsibility |
|---|---|
| `config` | Reads env: `PORT` (8252), `DATABASE_URL`, `REDIS_URL` (empty: no cache, no notifications), `TMDB_API_KEY`, `TORRSERVER_URL`, `JACKETT_URL`, `JACKETT_API_KEY`, `ALLOWED_ORIGINS`, `ALLOWED_HOSTS`, `TV_APP_KEY` |
| `database` | SQLite open with pragmas and pool settings, `Migrate(db, models...)` |
| `httpserver` | gin engine, logging/recovery, Host check against DNS rebinding (LAN names and IPs, hosts of `ALLOWED_ORIGINS` and `ALLOWED_HOSTS`; for Host `server`, i.e. the web container, the browser's Host from `X-Forwarded-Host`, none for SSR fetches), `Sec-Fetch-Site: cross-site` block (except LAN CORS origins, `Origin: null` with `?app_key=<TV_APP_KEY>` (the packaged webOS app), GET/HEAD without Origin from a webOS user agent (its images and video) and page navigations outside `/api/`), 1 MB body cap, timeout middleware (skips `/api/torrent-stream/`), CORS with LAN origin check (`Origin: null` only with the app key), gzip for JSON answers under `/api/` (`gzip.go`, registered last so CORS preflights are untouched; skips HEAD, `Range`, `/api/torrent-stream/` and anything that is not `application/json`, decided when the handler starts writing; pooled `compress/gzip` writers at `BestSpeed`), `Lang(c)` from `X-Lang` then `?x_lang=`, `ProfileID(c)` from `X-User-ID` then `?profile_id=` (header-less GETs skip the CORS preflight), `GET /api/health`, `http.Server` with graceful shutdown |
| `cache` | go-redis v8 wrapper; a nil `*Cache` is valid and behaves as "no cache" |
| `tmdb` | TMDB HTTP client (v4 token as Bearer, on `upstream`), types, 24 h in-memory cache shared by catalog, providers and notifications; expired entries are kept 7 days and served when TMDB fails (see [DPI resilience](#dpi-resilience)) |
| `upstream` | Resilient HTTP client for TMDB API, image CDN, the TMDB export files (search typo index) and subtitle providers: short timeouts, retries on a new connection, hedging (GETs), per-request budget, `Post` for repeatable POSTs (see [DPI resilience](#dpi-resilience)) |
| `torbox` | TorBox debrid API client (`checkcached` in batches of 100, `createtorrent`, `mylist`, `requestdl`, `user/me`), Bearer key plus a `Nextflix/1.0` User-Agent (Cloudflare refuses default ones), HTTP/1.1 with short dial / TLS / header timeouts; the key never reaches an error message (see [Debrid](#debrid-torbox)) |

### Modules (`server/internal/modules/…`)

| Module | Responsibility | Tables |
|---|---|---|
| `profiles` | Profile CRUD, max 5, per-profile UI `language` (`en`/`tr`, default `en`), seeds "Home" on first start, delete cascades through cleaners | `profiles` |
| `progress` | Continue watching, per-episode progress | `stream_progress` |
| `ratings` | Per-profile ratings (`dislike`/`like`/`love`) and Favorites (replaces My List), one-time My List migration, change hook; see [Ratings and Favorites](#ratings-and-favorites) | `title_ratings`, `rating_migrations` (`my_list_items` is kept, read-only) |
| `recommendations` | Per-profile rows built from ratings + TMDB recommendations/similar, and the "Surprise me" pick (ratings + watch history); see [Recommendations](#recommendations), [Surprise me](#surprise-me) | – (memory cache) |
| `top10` | Netflix weekly Top 10 (Turkey and global) mapped to TMDB; see [Top 10](#top-10) | `top10_lists`, `top10_titles` |
| `catalog` | TMDB lists, details, ranked search with typo correction and suggest (see [Search ranking](#search-ranking)), "More Like This" (see [More Like This](#more-like-this)), seasons, search filters and ranked browsing (`/api/catalog/discover`, see [Search filters](#search-filters), [Browse ranking](#browse-ranking)) | – (typo index file `/app/data/search-index.tsv.gz`) |
| `providers` | TV shows per streaming provider (TMDB discover across regions) for the TV Shows page rows | – |
| `notifications` | Redis-backed feed filled by TMDB-only generators | Redis |
| `streaming` | TorrServer files/probe/play (ffmpeg fMP4), auto-resolve through Jackett (plus Bitsearch and IMDb id search) with swarm warm-up and 4K upgrades, indexer settings, TorBox debrid and its instant-play hints; see [Streaming](#streaming) | `torrent_configs`, `stream_sources`, `debrid_settings` |
| `subtitles` | Subtitles from the torrent itself (separate files, small embedded text tracks), OpenSubtitles (moviehash sync), SubDL, SubSource, turkcealtyazi.org, subf2m.co (Subscene archive) and YIFY Subtitles (movies); SRT/ASS/VTT → UTF-8 WebVTT with Turkish charset repair, ranking, automatic timing to the playing file (see [Automatic subtitle timing](#automatic-subtitle-timing-modulessubtitlessyncgo)), disk cache, settings, optional AI translation of missing languages with Claude Haiku 5.5: the whole source in the background, nearest to the player first, every answered line checked against its own line by id and echo (`ai.go`, `ai_job.go`, `ai_prompt.go`; see [API.md](API.md#ai-translation)), holes (stretches a reference of the file has dialogue in and the subtitle has none) found per served subtitle, ranked down and filled by that translation (`holes*.go`, [API.md](API.md#holes)) | `subtitle_settings`, `ai_translation_usage`, files in `/app/data/subtitles` and `/app/data/ai-subtitles` |
| `segments` | Intro and end credits of an episode for the Skip intro / Next episode prompts: file chapters, then TheIntroDB / IntroDB; see [Skip intro and Next episode](#skip-intro-and-next-episode) | `skip_segments` |
| `images` | Disk-cached proxy for TMDB images (`GET /tmdb-img/t/p/:size/*file`) plus a home feed warmer; see [Images](#images) | files in `/app/data/img-cache` |
| `playbacklog` | `POST /api/playback-log`: playback diagnostics from the TV player (waits, freezes, dropped frames, seeks) written as `playback: ...` log lines and daily files, rate limited per IP; read them with `scripts/playback-log.sh` (see [API.md](API.md#playback-log-modulesplaybacklog)) | daily files in `PLAYBACK_LOG_DIR`, 14 days |

A module usually has `model.go`, `repository.go`, `service.go`, `handler.go` and `routes.go` with
`func Register(api *gin.RouterGroup, s *Service)`. `catalog`, `providers` and `images` have no model or repository; `images.Register` takes the engine itself because its route lives outside `/api`.

### Dependency rules

- `cmd/nextflix` imports `platform/*` and `modules/*`. It only wires things together.
- Modules import only `platform/*` (and third-party packages). **Modules never import each other.**
- `platform/*` never imports `modules/*`.
- When a module needs another module, it declares a small interface itself (the consumer owns the interface) and
  `main.go` passes the implementation in. Example: `profiles.ProfileDataCleaner` is implemented by
  `progress.Service` and `ratings.Service`, so deleting a profile also deletes its progress, ratings and favorites
  without `profiles` knowing about either module. Likewise `recommendations.RatingsSource` is fed from
  `ratings.Service.List` by a small adapter in `main.go` (`ratingSignals`), and `ratings.Service.OnChange` calls
  `recommendations.Service.Invalidate`.
- Table names (`profiles`, `stream_progress`, `my_list_items`, `torrent_configs`) are stable; old tables are never
  dropped.

### Startup order (`cmd/nextflix/main.go`)

1. Load `.env` if present (optional), `config.Load()`.
2. `database.Open`, then `database.Migrate` with the models of profiles, progress, ratings, top10, streaming,
   subtitles and segments.
3. `cache.New` (on error the server logs it and runs without Redis), `tmdb.New`.
4. Context cancelled by SIGINT/SIGTERM.
5. `progress`, `ratings`, then `profiles` with both as cleaners, plus `EnsureDefault`; `ratings.MigrateMyList()`
   (one time, logged) and `go ratings.Backfill(ctx)`; `recommendations` (ratings adapter + TMDB catalog, hooked to
   `ratings.OnChange`), plus the watch history (`SetHistory(watchHistory(progress))`); `top10` (`Start(ctx)` loads stored lists and refreshes in the background).
6. `catalog` with `StartSearchIndex(ctx, SEARCH_INDEX_DIR or /app/data)` (loads / refreshes the typo index in the background), `SetRatings(searchRatings(ratings))` + `ratings.OnChange(InvalidateTaste)` (genre taste), `SetLocalSignals(localSignals(profiles, progress, ratings))` (browse ranking) and `SetTop10(top10.GetIn)` (the Top 10 sections of `/api/home`) and `recommendations.SetSimilar(similarTitles(catalog))` (More Like This lists for the "Because you ..." rows); `providers` (on the shared TMDB client), `images` (`Start(ctx)` launches cleanup and the warmer; `Top10: top10Art(top10)` gives it the Top 10 artwork), `notifications` (`go Start(ctx)`, `defer Close()`), `streaming` (also on the shared TMDB client, for the runtimes resolve and probe check files against) plus `EnsureSettings` and `StartInstant(ctx, instantFeed(top10, tmdb))` (instant-play workers and prewarm).
7. `segments` (chapters from streaming via `fileChapters`, show IMDb ids from TMDB), `httpserver.New`, `images.Register(r, …)` on the root, `api := r.Group("/api")`, every other module's `Register`, `httpserver.Run`.

## Web

| Feature (`web/src/features/…`) | Routes |
|---|---|
| `home` | `/` |
| `catalog` | `/movies`, `/tv-shows`, `/new-popular`, `/tmdb-movie/[id]` |
| `search` | `/search` |
| `surprise` | – ("Surprise me" button and modal, used by `home` and `search`) |
| `favorites` | `/favorites` (`/my-list` redirects here) |
| `notifications` | `/notifications` |
| `profiles` | `/profiles` (and `ProfileGate`, which forces a profile pick on every page) |
| `settings` | `/settings` |
| `watch` | player, Watch button, seasons/episodes, Continue Watching row (used by other features) |
| `shell` | navbar, phone tab bar and page shell |

### Rules

- Files in `src/app/` are thin: a route re-exports a screen. Screens take no props and read
  `useParams()` / `useSearchParams()`.
- Features may import `shared/*`. Imports between features are kept to the public pieces listed above
  (for example `watch` components, `shell/PageShell`, `favorites/store`); `shared/*` never imports a feature.
- All imports use the `@/` alias.
- `src/proxy.ts` (Next.js proxy, formerly middleware) answers 403 when the Host is not a LAN name or listed in
  `ALLOWED_HOSTS` (`deploy/config/web.env`), same rules as the server (`shared/lib/lanHost.ts`): a DNS rebinding
  page could otherwise call `/api` through the rewrites as same-origin.
- Images always go through `/tmdb-img/t/p/<size><path>` (`shared/lib/image.ts`), never `image.tmdb.org`. Card,
  billboard and hero artwork is rendered with `shared/ui/TmdbImage` (see [Images](#images)).
- The active profile lives in `localStorage` under `hfx-profile`; changes fire the `hfx-profile-change` event.
  API calls that are per profile send `profileHeaders()` (`X-User-ID`).

### Phones and tablets

Desktop (≥ `md`, 768 px) keeps its layout; smaller screens and touch input get Netflix-app tweaks:

- Shell: below `md` the top bar is compact (logo, search icon → `/search`, notifications, profile; 44 px touch
  boxes) and `shell/BottomTabBar` (Home, TV Shows, Movies, New, Favorites) replaces the Browse menu. The page gets
  bottom padding through `body:has(.nf-tabbar)`; the bar hides while an `aria-modal` dialog is open. Bars pad with
  `env(safe-area-inset-*)` (`viewport-fit=cover`).
- Use `max-md:` / `md:` for layout and `pointer-coarse:` for touch sizes (≥ 44 px targets, larger ⚡ chip). Inputs
  are at least 16 px on phones and touch screens (iOS zooms otherwise; `globals.css`). Hover previews and row
  arrows are mouse-only; elements that only appear on hover are shown or hidden on touch (`pointer-coarse:`).
- Rows (`.nf-card-row`) snap to cards on touch and keep horizontal swipes inside the row. Phone cards are sized for
  about 2.5 backdrops / 3 posters per screen.
- Billboards and the detail hero are taller on phones and portrait tablets (`sm:portrait:`), with full-width Play
  buttons; pop-overs become bottom sheets on phones (genre menu, Surprise me).
- PWA: `public/site.webmanifest` (standalone, `#141414`) and the Apple web-app metas in `app/layout.tsx`.

## Localization

The UI is available in English and Turkish (Türkçe), chosen per profile like on Netflix. There is no i18n
library; `web/src/shared/i18n/` is a small typed layer:

| File | Content |
|---|---|
| `en.ts` | English messages; defines the key set (`MessageKey`) |
| `tr.ts` | Turkish messages, typed as `Messages`: a missing or unknown key is a TypeScript error |
| `core.ts` | `translate`, locale detection, Intl format helpers (`en-US` / `tr-TR`) |
| `I18nProvider.tsx` | `I18nProvider` (in `app/layout.tsx`), `useT()`, `useLocale()`, `useFormat()` |

Language order: the active profile's `language` (server field, edited in the profile editor) -> localStorage
`hfx-lang` (the last profile's language, used on the picker) -> `navigator.language` (`tr*` -> `tr`) -> `en`. The
provider listens to `hfx-profile-change`, so switching or editing a profile re-renders at once and updates
`<html lang>`. The server and the hydration pass render English, then React switches to the client locale (no
hydration mismatch); a head script sets `<html lang>` early.

Adding a string:

1. Add a namespaced key to `en.ts` (`'<feature>.<name>'`, e.g. `'detail.moreLikeThis'`), then the same key to
   `tr.ts` (the type check fails until you do). Use Netflix Türkiye wording; never translate brand names or
   title metadata.
2. Placeholders are `{name}`. Plurals are `{ one: '{count} Season', other: '{count} Seasons' }`, picked by
   `vars.count` with `Intl.PluralRules`. Numbers in vars are formatted for the locale.
3. In a client component:

```tsx
const t = useT();
t('home.continueWatching', { name: profile.name });
t.rich('profiles.deleteConfirm', { name: <b>{profile.name}</b> }); // placeholders as React nodes
const f = useFormat();
f.duration(96); // "1h 36m" / "1 sa 36 dk"; also f.relativeTime, f.date, f.shortDate, f.number
```

Plain functions outside React take the locale: `translate(locale, key, vars)`, `formatRelativeTime(locale, date)`.
Keys for the watch player, detail, home, catalog, My List / Favorites and ratings (`player.*`, `detail.*`,
`home.*`, `catalog.*`, `myList.*`, `favorites.*`, `rating.*`, `top10.*`) already exist. Server error messages
are still English.

### Content language (TMDB data)

Titles, overviews, taglines, genre names, episode names and localized posters / logos follow the profile language
too:

1. Web: `installLangFetch()` (`shared/lib/api.ts`, called once at module load of `I18nProvider.tsx`) wraps
   `window.fetch` and adds `X-Lang: <tr|en>` (from `detectLocale()`, the same source as the UI language) to every
   request to `getApiUrl()/api/...` or `/api/...` that has no `X-Lang` yet (`sendBeacon` and `keepalive` requests
   on unload send none; they need no TMDB text).
   Client caches of backend answers are keyed by language: `apiCall` (localStorage), `getJson` in
   `features/catalog/titles.ts`, the recommendation rows (`useRecommendations`) and the notification store (its owner
   is profile + language). Logos are picked by `pickLogo` (`shared/lib/image.ts`): profile language, then English,
   then language-less.
2. Server: `httpserver.Lang(c)` reads `X-Lang`, or `?x_lang=` when the header is missing or empty (`tr`, `tr-TR` ->
   `tr`; anything else -> `en`) and sets `Vary: X-Lang`. The query form lets the packaged webOS app send header-less
   GETs without a CORS preflight; it is not `lang` because `/api/torrent-stream/subalt` uses `?lang=` for the
   subtitle language. Handlers call `tm.WithLang(lang)`, a view of the shared `tmdb.Client` that sends `language=tr-TR`
   (or `en-US`) and keeps its own entries in the 24 h in-memory cache (keys end in `|tr` / `|en`). For `tr`, images
   use `include_image_language=tr,en,null` and videos `include_video_language=tr,en,null` (TMDB lists no trailer for
   most titles in Turkish otherwise).
3. Fallback: TMDB's tr-TR answers often have an empty overview or tagline, and an untranslated title comes back as
   the original title (e.g. Japanese). For details, seasons, episodes and lists the client then fetches the en-US
   answer (lazily, cached) and fills empty overviews / taglines / episode overviews, empty or placeholder episode
   names ("3. Bölüm") and titles that equal the original title of a title whose original language is neither
   Turkish nor English. Turkish originals keep their title. Helpers: `tmdb.MissingText`, `tmdb.FillText`.
4. Modules: catalog and providers answer in the request language; recommendations are cached per profile and
   language; Top 10 lists are stored in English (ranks from Netflix's English titles) and localized per request
   from the Turkish details; notifications are generated in English and their TMDB titles / overviews / genres are
   localized when listed (3 s budget, the rest stays English until the next poll). Generated notification text is
   built by the web from these fields.
5. Language-agnostic lookups stay English: streaming (runtime, title names), ratings backfill, subtitle external
   ids, availability. Resolve, prefetch, upgrade, sidegrade and release replace the client's `title` with TMDB's
   English title of `tmdb_id`, so a Turkish title still finds releases and every endpoint uses the same cache keys.
   Stored titles (ratings, Continue Watching) keep the language they were saved in; the Favorites page shows the
   title from the localized details.

## Home endpoint

`GET /api/home` (`modules/catalog/home.go`, contract in [API.md](API.md#home)) answers the profile-independent home
rows in one request instead of about ten: upcoming movies and TV, popular movies, the Turkish Top 10 (movies and
shows) and the genre rows. Clients open many requests on HTTP/1.1 (6 connections per host, shared with images),
so one gzipped answer arrives much sooner.

- Every section runs in its own goroutine on the cached TMDB lists (`tm.WithLang(lang)`), the Top 10 through the
  `Top10Source` main.go wires (`top10.GetIn`). After 8 s (`homeDeadline`) unfinished sections are `null`; their
  fetches go on and fill the cache for the next call. The answer is always 200 (`null` sections without a TMDB key
  or before the first Top 10 download) with `Cache-Control: private, max-age=60`, or `no-store` when a section is
  `null`.
- Each section is byte for byte the body of its old endpoint, so clients map both paths into one `HomeData`. The
  genre rows come from `tmdb.HomeGenreRows` (`platform/tmdb/home.go`, fixed ids and order); `row.Query()` adds the
  defaults (`sort_by=popularity.desc`, `include_adult=false`) and equals what `discoverParams` makes of the old
  `/api/tmdb/discover/movie` query, so both share one TMDB cache entry. The image warmer reads the same list.
- Clients call `/api/home` first, without custom headers (`?x_lang=`). A 404 or 405 means an older server: they load
  the old endpoints in parallel, remember "unsupported" for that server until the next launch and try again then.
  Network errors and 5xx keep the data on screen and retry on the next refresh. A `null` section keeps that section of
  the previous answer or snapshot.

## Images

Browsers resolve `image.tmdb.org` with the ISP's hijacked DNS, so every image is served from our own origin:

```
browser -> web:3008 /tmdb-img/t/p/<size>/<file>  (Next.js rewrite)
        -> server:8252 /tmdb-img/t/p/<size>/<file>  (modules/images)
        -> disk cache /app/data/img-cache/<size>/<file>, on a miss image.tmdb.org
```

Server (`server/internal/modules/images`):

- Only the sizes `w92 w154 w185 w300 w342 w500 w780 w1280 original` and bare file names
  (`[A-Za-z0-9_-]+.(jpg|jpeg|png|webp|svg)`) are accepted; anything else is a 404 without an upstream call.
- The cache lives in the `db` volume (`/app/data/img-cache`, override with `IMAGE_CACHE_DIR`). Downloads go to a
  temp file and are renamed into place, so a half-written image is never served. One download per file at a
  time (concurrent requests wait for it), at most 12 upstream downloads overall (waiting for one of these slots
  is capped at 10 s), each through `platform/upstream` with a 4 s budget (see [DPI resilience](#dpi-resilience)).
  TMDB 404s are remembered for 15 minutes; other failures for 30 s and answered `504` (`502` for a bad upstream
  status or content type), always with `Cache-Control: no-store`.
- Responses carry `Cache-Control: public, max-age=31536000, immutable`, an `ETag`, `Last-Modified` and
  `X-Cache: HIT|MISS`; `If-None-Match`, `HEAD` and ranges work through `http.ServeContent`. SVG logos get a
  sandboxing CSP.
- The cache is capped at 2 GB. Every 30 minutes (and right after the cap is crossed) a cleanup deletes the least
  recently used files (by mtime, which hits refresh at most once an hour) down to 90% of the cap.
- Warmer: 20 s after start and then every 6 hours it reads the home page lists through `platform/tmdb`
  (trending, the Turkish Top 10, popular TV, new episodes, popular movies, coming soon, the genre rows of
  `tmdb.HomeGenreRows`) and downloads the sizes the UI uses, in on-screen order: the six billboard candidates
  `w300 w780 w1280 original` plus their `w500` logos, backdrop cards `w92 w300 w500 w780`, Top 10 posters
  `w92 w342 w500` (web and TV ranked rows use `w342`) plus their backdrops at `w780` (the TV hero of a focused card),
  Coming Soon posters `w92 w342 w500` plus their backdrops at `w500` (TV cards) and `w780` (TV hero). 3 at a time
  (user requests keep most download slots and the connection bursts stay small), for English and Turkish lists
  (Turkish posters and logos differ). The Top 10 comes from `Config.Top10` (wired in `main.go`, `nil` skips it).

### DPI resilience

The ISP (Kablonet) resets or stalls some TLS connections to TMDB with DPI. On Windows GoodbyeDPI hides most of
it, but Docker traffic through mirrored WSL networking still sees it: a reset fails every request on that
connection, and a stalled one used to hang until the old 15 s header timeout (images answered `502` after 15 s).
Resets come in bursts: in the measurements all 12 in-flight connections to the CDN were reset at once.

`platform/upstream.Client.Get` (TMDB API and images) reads the whole body and:

- dials with 3 s connect, 3 s TLS handshake and 5 s response header timeouts, and aborts a body that makes no
  progress for 2 s (1.5 s for images);
- retries resets, EOFs, timeouts, truncated or stalled bodies, `429` and `5xx` up to 3 tries in total, after a
  100-300 ms jittered pause (or the response's `Retry-After` when it is at most 5 s and fits the budget, else the
  response is returned at once), each retry on a new connection (a keep-alive-less transport);
- hedges: when no try has response headers after 1.2 s (images) / 1.5 s (API), it starts one parallel try on a new
  connection; the first good answer wins and the other is cancelled;
- stops at a per-request budget (images 4 s, API 8 s) with an error wrapping `context.DeadlineExceeded`; when the
  tries or the budget run out after a `429` / `5xx`, that response is returned (with its `Retry-After`) instead;
- speaks HTTP/1.1 with a per-host pool (16, images 12). Measured from a container on `nextflix_default` (1,200
  poster fetches per variant, 12 at a time): HTTP/2 multiplexes everything on one connection, so one reset failed
  12 requests at once (24 of 1,200 with a plain client); with retries both HTTP/2 and HTTP/1.1 had 0 failures, and
  HTTP/1.1 had the lower p95 (about 250 ms vs 330-420 ms, h2 suffers head-of-line blocking on one connection);
- dials IPv4 only when the host has no IPv6 route (Docker's bridge has none; checked once with a UDP "dial").

`platform/tmdb.Cached` keeps entries 7 days past their 24 h TTL. An expired entry is answered at once while one
background call refreshes it (stale-while-revalidate: `flightGroup.start`, one per key, at most 4 at a time so a
wave of entries expiring together does not burst TMDB; a background call joins the shared calls only once it has a
slot, so a miss never waits behind the queue). Only a miss waits for TMDB (concurrent misses share one fetch).
Movie and TV details use `CachedFresh` instead: an expired entry waits for the refresh, so a new season or release
date shows at once (details pages, next episode, playback's season counts). When a
refresh fails the stale value stays and is re-checked after a minute. Combined lists (`GetUpcomingMovies`,
`GetUpcomingTVSeries`) fetch their four TMDB lists at once (a localized one also starts the English combined list
right away, which its gap fallbacks read); combined lists whose parts failed (also lists with videos) prefer stale
complete data and are otherwise cached for only a minute instead of 24 h. Re-measure with
`go test -tags live -run Live -v ./internal/platform/upstream/` (needs `TMDB_API_KEY`, prints p50/p95/max).

Every network failure that is left after the tries wraps `upstream.ErrTransport`. `Client.Post` sends a body with
the same retries on a new connection but never hedges; it is only for endpoints that are safe to repeat.
`Options.RetryStatus` narrows the retried statuses and `Options.CheckRedirect` limits redirects (a refused redirect
is final, not retried).

Subtitle providers (`modules/subtitles/providers_http.go`) use it too, since the same DPI resets their TLS
connections from Docker (logs showed `connection reset by peer` on turkcealtyazi.org and a cut OpenSubtitles answer,
`unexpected end of JSON input`):

- OpenSubtitles, SubDL and SubSource share one client (3 tries, hedge after 4 s for GETs, 15 s header timeout, 5 s stall
  timeout, 25 s budget, retries 500/502/503/504); their search GETs, file downloads (SubDL and SubSource zips, OpenSubtitles
  links) and the OpenSubtitles `login` / `download` POSTs retry resets on a new connection;
- turkcealtyazi.org has its own client: no hedging (one request every 2 s is the polite rate), only 502/504 retried
  (its 503 is a Cloudflare challenge), redirects only on the site (`/tt<id>/` → `/mov/<id>/<name>.html`); its
  download form POST retries resets the same way. When FlareSolverr reports a network error of its browser
  (`net::ERR_CONNECTION_*`, `ERR_EMPTY_RESPONSE`, `ERR_SSL*`, ...) the page is loaded through it once more;
- subf2m.co and yifysubtitles.ch (`providers_scrape.go`) get one client each: no hedging, one request per second per
  site, retries 500/502/503/504, redirects only on the site (subf2m also its download CDN `isubcdn.com`), pages kept
  24 h on disk. Neither needed FlareSolverr when checked (plain HTTP from Docker answered 200). subf2m's search is
  disallowed in its robots.txt, so title pages are found by Subscene style paths built from the TMDB title and checked
  by the IMDb id they link (main.go passes a `TitleLookup` next to the IMDb lookup);
- checked and not used: AnimeKalesi (its robots.txt disallows the download paths `/s-*`, `/sa-*`, and titles carry
  no IMDb / TMDB id), OpenSubtitles.org (the site sits behind an Anubis proof-of-work wall and the legacy XML-RPC /
  REST API needs VIP or a registered user agent; it is the same database as OpenSubtitles.com, which we use);
- a provider call that failed on the network (`ErrTransport`, a timeout, 5xx, a cut JSON answer, a FlareSolverr
  network error) is not cached: the next `/api/subtitles` call (the players ask again after 8 s and 20 s) tries
  again. Real "no results" stay cached 30 min, other failures (bad key, quota, title not found) 3 min.
- OpenSubtitles download quota (free key without login: 5 a day): once a download answers 406 (or a 429 about the
  quota, or `remaining: 0`) the service remembers it until `reset_time_utc` (1 h without it). Until then no download is
  sent: `/api/subtitles/file` answers 429 `{quota: true, reset_at}` at once, and the list marks OpenSubtitles items
  `quota: true` and moves them behind the other items of their language. Only the file endpoint downloads, once per
  id (shared by concurrent requests, then served from the disk cache, also after `refresh=1` lists).

Web:

- `shared/lib/image.ts` builds proxy URLs, `srcset` candidates per kind (poster `w185/w342/w500`, backdrop
  `w300/w500/w780`, billboard `w780/w1280/original`) and the blur placeholder (`w92`, billboard `w300`).
  `BILLBOARD_SIZES` caps the billboard at 1280 CSS px, so `original` is only picked on HiDPI screens.
- `shared/ui/TmdbImage` renders a `#2f2f2f` skeleton with a shimmer, then the blurred placeholder, then the real
  image, which fades in (220 ms) after `img.decode()`. It needs a positioned parent with the image's aspect ratio.
  `priority="high"` is for billboards and the detail hero (`fetchpriority=high`); the default waits until the
  image is within 1.5 viewports.
- Rows use `useImageRowGate` + `<ImageRowGate>`: a row starts loading when it is within 1.5 viewports
  vertically, and each card when it is within 1.5 row widths of the visible part of the scroller (a plain
  viewport observer would never see cards clipped by the scroller).
- `shared/ui/ImageGuard` and the head script in `app/layout.tsx` rewrite stray `image.tmdb.org` URLs (`src`,
  `srcset`, inline styles) to `/tmdb-img/` and stop `onError` retry loops after two failures.

## Streaming

`server/internal/modules/streaming` plays torrents through TorrServer. The Watch button resolves a title to one torrent
file (`resolve.go`, shared per title by prefetch and play in `resolvejob.go`, cached for 7 days in `stream_sources`):

1. Jackett search per indexer (`indexers.go`): the configured indexers come from Jackett's torznab list (cached 10 min)
   and only those carrying the media type are asked (YTS only for movies, EZTV only for TV), each with both queries
   in parallel. Each source's answer time is tracked as a moving average in memory (Bitsearch included); one at or
   below 1 s (or not measured yet) counts as fast. The title's queries start at once; the TMDB / IMDb lookups (other
   names, IMDb id, runtime) run next to them and the queries of the other names join the same search when they
   arrive (after the cut they count as late results). Every deadline counts from the start of the resolve (indexer
   list fetch and lookups included; their times are logged). Resolve ranks as soon as one of these holds
   (`tsGatherJobs`, the reason is logged with the result count): every source it waits for answered (and the names
   lookup's queries did too); the fast ones answered (or 1.2 s passed) and the results already hold 3 usable,
   title-matched candidates of the top wanted resolution (2160p for HEVC clients without a cap, else 1080p or the cap;
   a 4K needs 3 seeders; with a known home line, see Line below, only releases within it count, and the top is
   1080p when even a typical 4K, 14 Mbit/s, is over it); the fast ones answered and 1.2 s passed, or 1.5 s passed, and there are 3 of 1080p or more
   (any mix of 1080p and 4K: 4K is preferred, not required, and a late one still becomes an upgrade); 3.5 s passed
   (longer only while nothing at all arrived). Ranking waits for the other names only when the title alone found
   fewer than 5 usable results. Slow indexers (1337x through FlareSolverr, ~15 s; any indexer whose
   average answer time is above 3.5 s; `internetarchive` always) keep running for up to 25 s and `late.go` merges
   their results, as do the ones the early cut left behind: up to 2
   candidates that rank above the pick are checked; a higher resolution feeds the upgrade path, one of the same
   resolution becomes the cached source (next play) and a spare. Without an indexer list (old Jackett) the aggregate
   endpoint is used; if every request fails (and Bitsearch found nothing) the resolve answers 502. Upgrade searches
   wait for all indexers.
   - Bitsearch (`bitsearch.go`, toggle `bitsearch_enabled` in the indexer settings, on by default): the search page
     of the DHT index bitsearch.to is scraped as one more source of the same gather (same cut-off, same answer-time
     average under `native:bitsearch`; later answers join the late results), finding torrents no Jackett indexer
     lists. When every Jackett request fails, the next Jackett URL is tried and then what Bitsearch found is the
     answer. One folded query per name (movies:
     `<name> <year>`; TV: the `SxxEyy` ones first, then the absolute and bare ones, a season query only when its bare
     name is not searched), ≤ 4 per search round, ≤ 1 request/s, 30 min cache per query, video categories only, magnet built from
     the info hash plus open trackers. FlareSolverr only on a Cloudflare challenge; any other failure pauses it for
     2 min and resolve goes on with Jackett.
   - IMDb (`imdb.go`): with a `tmdb_id`, the IMDb id (TMDB external ids) and IMDb's English title (suggestion API,
     cached 7 days) are looked up next to the other names (`titles.go`); the title is matched and searched like an
     alternative title. Movies are also searched by `t=movie&imdbid=` on every indexer whose torznab caps support it;
     those results count as the title whatever their name.
   - TV queries (`indexers.go`): `<name> SxxEyy` and `<name> Sxx` per name. Foreign shows often use other notations
     ("Cukur 94 S4 E1", "Cukur.BL.90", "5. Bölüm", whole-show packs), so for a non-English original the names lookup
     also adds the bare original name (folded, at most 2) and, from season 2 on, `<name> <absolute>` (`tsBroadQueries`);
     without other names the bare title. An English original only gets the bare title (with its alternative titles)
     when the first round found fewer than 5 usable results.
   - Episode numbers (`episodes.go`): TMDB's season list (`seasons[].episode_count`, specials left out, cached 24 h,
     waited for up to 2.5 s from the start of the resolve) gives the absolute number of the episode (Çukur S4E1 =
     33 + 34 + 25 + 1 = 93). Without counts, names that carry both numbers ("Cukur 94 S4 E1": absolute minus episode
     is the season's offset) decide; with counts TMDB wins and a disagreement is only logged (one uploader counts a
     special as S3E26, while TMDB and the season packs "Season: 4 / Episodes: 093-131" start season 4 at 93).
2. Ranking (`tsScore`, a sum of logged parts, `tsScoreParts`): match (year / episode), resolution, source tier
   (REMUX 18 > BluRay 15 > WEB-DL 12 > WEBRip 6 > HDTV 3 > HDRip/unknown 0, cinema leaks -150), video codec fit, then
   the format parts of `rankformat.go` for what the client can use, and seeders; top 4 checked in parallel. The
   breakdown of the checked candidates is logged (`resolve "<title>"|<caps>: #1 206.1 [match +30.0 res +95.0 ...]
   {eac3 5.1 atmos DV+HDR webdl} "<name>"`).
   - Tags (`tags.go`, `tsParseTags`): audio codec (TrueHD, DTS:X, DTS-HD, DDP/E-AC-3, DTS, DD/AC-3, FLAC, PCM, Opus,
     AAC, MP3; every codec listed, best first), channels (7.1 / 5.1 / 2.0, `6CH`), Atmos, DV / HDR10 / HDR10+ / HLG,
     source, Hi10P / 4:4:4 H.264 (undecodable, -40) and whether the name was cut off (ends in `...`/`…`, or has no
     `-GROUP` and ends in a format word or the start of one: `... DDP5 1 At`, `... WEB-DL DD`).
   - Audio (`tsAudioScore`): a native-audio client (`atmos=1` with `eac3` in `acodecs`) gets Atmos / DTS:X 14 >
     7.1 10 > 5.1 7 > stereo 0 for codecs it declared (TrueHD / DTS only with `truehd` / `dts`; an undeclared
     multichannel codec scores 5, it is re-encoded); every other client (browsers: the server re-encodes to Opus /
     AAC 5.1) gets multichannel 7 > stereo 0, Atmos is just 5.1 there. Unknown audio 2; a name cut in its audio tag
     (`... DD`) counts as unknown.
   - HDR (`tsHDRScore`, HEVC clients): with `dv=1` any DV 8, HDR10+ 6, HDR10 5. Without it HDR10 / HDR10+ / DV with an
     HDR10 layer 5 on an HDR display, -5 on an SDR one (tone-mapped); DV-only names (profile 5, no HDR tag) -90; a DV
     tag at the end of a cut-off name may hide its HDR10 layer (2). Non-HEVC clients: any HDR -8 (re-encoded).
   - Audio language (`tsLangScore`, `lang.go`): Russian trackers often release a dub alone (the name then carries no
     language tag). Dub markers (`MVO`, `DVO`, `AVO`, `Dub`, `Дубляж`, `многоголосый`) and foreign-language tags
     that are not the original language (`Rus`, `Ukr`, `ITA`, `Latino`, `Castellano`, `TRUEFRENCH`, `VFF`, `VFQ`,
     `German`, `Hindi`, ...) give -8 unless the name also says `MULTi` / `MULTiLANG` / `Dual Audio` / `Original` /
     `ENG` (for an English or unknown original), which give +4. The original language comes from the names lookup
     (TMDB `original_language`). `MULTi-SUB` names subtitles, not audio, and does not count here.
   - Subtitles (`tsSubNameScore`, `subs.go`): new releases often carry the profile language's subtitle only inside
     some torrents (platform subtitles muxed into WEB-DL MKVs) long before subtitle sites have it. For the profile
     language (`X-Lang`; not when it is the original language; English only for a known non-English original) a
     name that names its subtitle (`TR Sub`, `TRSub`, `Türkçe Altyazı`, `Subs: ENG, TUR`; for Turkish also `TUR`
     / `Turkish` alone) gives +6, `MULTi-SUB` / `MULTISUB` / `Multi Subs` +4. The check decides
     (`applyProbedSubs`): a text subtitle track (`subrip`, `ass`, `ssa`, `mov_text`, `webvtt`; PGS / VobSub images
     and forced-only tracks do not count) or a subtitle file next to the video in the torrent (`.srt`, `.ass`,
     `.ssa`, `.vtt` named `tr` / `tur` / `Turkish` ...; in a pack only the ones whose path names the video file)
     in that language makes the part +8, a probe without it 0. Like the format parts it moves the rank
     (`rankAdj`), so it only reorders good checks of one resolution (`tsProbeReorder`), never beats swarm health
     or the duration check. The languages found are the answer's `format.subs`.
   - Bitrate (`tsRateScore`): 6·log2 of size / runtime against the tier's typical one (2160p 14, 1080p 5, 720p 2.5,
     480p 1.2 Mbit/s), from -12 to +6; above 3× it falls again; season packs 0.
   - Line (`tsLinePart`, `linerate.go`): swarm health is judged per torrent, so a TV (hevc, no cap) on a 28 Mbit
     line (≈ 3.2 MB/s, test stack, no TorBox) got a 23.9 GB 2160p needing 3.5 MB/s: it played 10-22 s of preloaded
     data, stalled, stepped down and resolved again (first stable picture after 42-69 s). Now a release whose need
     (size / runtime) is over 0.75 of the known line gets -100 - 60·log2(need / that share): it ranks after the ones
     that fit, and when none fits the lower need ranks first (logged `"<name>" needs 3.50 MB/s, the line does ~3.20
     MB/s: ranked after the ones that fit`). Unknown line: 0. The line is what TorrServer downloads in all
     (`download_speed` summed over its torrent list), polled every 2 s while a measurement reads TorrServer or a
     stream plays. A poll counts while a measurement pulls without limit and two torrents download or one from ≥ 8
     active peers, or while a playing torrent downloads ≥ 1.25× its file's bitrate (TorrServer fills its read-ahead
     at line speed; once it is full the stream pulls just its bitrate: 0.6 MB/s from 10 peers says nothing about
     the line). The median of 5 such polls in a row is an observation (an average of 3 rose to 3.71 MB/s on a line
     shaped to 28 Mbit: TorrServer's speed counts bursts), and TorBox measurement reads are observations too (same
     line, one per file). The highest observation is the estimate, but it is only used (and stored in
     `line_rate`, so the first resolve after a restart knows it) once an observation of another torrent set (or
     TorBox file) reached 0.8 of it, or one set counted for 30 s in a row (a first playback teaches it). It halves over 4 days without newer observations as high (a slower line is
     learned). A TorBox-cached release or upgrade whose need fits what TorBox delivered in the last 10 minutes gets
     no line part and is never refused for the line.
   - Seeders (`tsSeedScore`): 6·log2(1+s) up to 30, then only 2·log2 of the multiple; under 5 seeders -20. A big swarm
     no longer outweighs a better format of a well-seeded release, and a format-preferred one with almost no seeders
     does not block a healthy one. The format parts together (≤ 28) stay below a resolution step (45), so they only
     order releases within a tier, and the measured swarm health still decides among the checked ones.
   A 2160p needs only 3 Jackett seeders for its full rank bonus; seeder counts are often stale, so the measured swarm
   decides. Copies of one torrent listed by several indexers are merged first (`tsDedupe`: same info hash, or same
   name stem without site tags and sizes within 2 %, unless both carry different hashes); before scoring, each result
   takes the fullest name of its torrent (`tsFullerTitles`): the name with the most format facts among the copies
   with its info hash, and for a name still cut off, a fuller name of the same size (within 2 %) that continues it
   word by word (`tsContinues`; hashes must not differ), so hashless cut-off copies ("... WEB-DL DD" with 1867
   seeders) are scored with their real tags and merge into their torrent. A check that finds a hash another check
   already owns stops; the hash a check finds behind a hashless listing is remembered, so once the player avoids
   that torrent the listing ranks last too (Detour: "[YTS AM]" took a check slot after its YTS twin was avoided). A movie name whose first word after the title is a year more than 1 off the title's year is
   another film (remake, namesake: "Night.Of.The.Living.Dead.2026...", the 1990 remake for the 1968 film) and is
   rejected; names without a year pass. Every path ranks through `tsScore` (resolve, TorBox boost, late results,
   upgrade / sidegrade searches, spares, `subalt`), so the year must be right: with a `tmdb_id` it is TMDB's release
   year (`englishTitle`, cached with the English title), whatever year the client sent (none on resumes and some
   upgrade polls, a wrong one from tests). Episode requests only accept the episode, ranges covering it, season packs of its season and
   complete-series packs containing it. Bonus material is rejected for movies and episodes: a word of it anywhere in
   the name ("Greatest Moments", "Inside the Episode", "Making of" / "Making-of", "Behind the Scenes",
   "Featurette", "Extras", "Bonus", "Trailer", "Sample", "Interview", "Documentary", "Prologue", "Promo", "Recap",
   ...; in Cyrillic "Трейлер", "Тизер", "Фильм о фильме", "Бонус", "Интервью", ...) that the title or one of its
   names does not contain itself; the whole name counts, so an IMDb id match scored by its own name
   ("The Odyssey: The Making of an Epic 2026 1080p web YTS") is caught too. So is a namesake that says so
   ("The.Odyssey.2026.720p.TUBI.WEB-DL.AAC2.0-NOtLAN (NOT The Chris Nolan FILM)"): a name with "not ... film" /
   "not ... movie" is rejected unless those words name someone and the title's directors (TMDB credits, read with
   the English title) are known and not among them (then the title is that namesake).
   The episode info after the title (`tsParseEpisode`, after an optional year / country and the repeated title of
   "Çukur / Cukur (Season: 4 ...") must come first, else the title goes on (another show).
   Notations: `S01E05`, `S01E05-E07`, `S1 E5`, `1x05`, `S01`, `S01-S03`, `Season 1 Episode 5`, `Seasons 1-3`,
   `Sezon 1 Bölüm 5`, `2. Sezon 5. Bölüm`, `01.sez` (season); `E05`, `B05`, `BL.90`, `BLM 12`, `Bölüm 5`, `5. Bölüm`,
   `ep.1`, `Episode 1`, `Episodes 01-107` (episode, of the named season when there is one); `Series 2` is a season
   (British), `Season 2 / Series: 108-138` episodes (Russian trackers); a bare number right after the title
   (`Cukur 94 S4 E1`, `Show 05 1080p`) is an absolute number, the first of a range when another higher one follows.
   Bare numbers above 1899 (years), 480 / 576 / 720 / 1080 and 264 / 265 are never episodes, nor are numbers after
   format tags (`DDP5 1`, `H 264`); a year followed by a month and day (`Show 2024 10 15`) is a date. Matching
   (`tsEpisodeKind`): season and episode decide when both are there (a name with both numbers matches by them); a
   number without a season is absolute (for season 1 equal to the episode, later seasons need the absolute number);
   episode numbers past the length of their season that fit its absolute numbers are absolute ("Season: 1 /
   Episodes: 01-107" of a show released as one season: a pack spanning seasons, kind 3 up to 300 GB; "S01E94";
   "Season 2 Episodes 34-67"), while a stale count of a running season does not make "S05E10" absolute.
3. Each candidate is added to TorrServer and its file is picked; in a pack
   (`tsPickEpisodeFile`) each video file is read like a release name (its title and year dropped; without the title,
   "[Grp] Show - 05 [1080p]" style names give their first bare number), with the season of its folders ("Season 2/",
   "S02/", "2. Sezon/") or the pack's as context. First the episode by season and number; a pack whose numbers run past
   the season's length is numbered absolutely and skips that step. Then the absolute number (also "Cukur.093.ser.01.sez"
   in a whole-show pack labelled season 1, and "S01E094"). No match: a single-episode release takes its largest video,
   a pack is rejected ("no matching video file"). Then two checks run in parallel:
   - Duration (`runtime.go`): ffprobe of the header against the runtime from the shared TMDB client (`platform/tmdb`,
     cached 24 h in memory; the client's `runtime` is the fallback). A probe still running after 12 s goes on (up to
     25 s more) once the warm-up shows peers or data, instead of being killed and started over; one that failed early is
     retried once on a warm swarm. There is one ffprobe per file at a time: later callers (the player's probe, play)
     wait for the running one and only start their own when it fails. Background work passes the quiet gate before
     it registers its probe, so a player never joins a probe that is put off; once resolve answered, a running probe
     of a candidate that was not picked stops while a stream starts (see 6). A verified file beats any file of unknown
     duration. Without a runtime the header is
     still probed in the background, so the player's probe is a cache hit. The probe also gives the real resolution:
     the candidate's quality follows the video size (height ≥ 1600 or width ≥ 3200 → 2160p, ≥ 900 / ≥ 1800 → 1080p,
     ≥ 600 / ≥ 1200 → 720p, else 480p) instead of its name, before the pick and in the cache (`cachedSource`
     corrects older entries whose file was probed since), so upgrades compare real resolutions. A file smaller than
     its name said gives way to a good candidate of a higher real resolution. In names, an explicit 2160p / 4K /
     1080p / 720p / 480p wins over UHD-ish words ("Criterion 1080p UHD BluRay" is 1080p). The probe also corrects the
     format (`applyProbedFormat`): a cut-off or silent name gets the best audio track (Atmos from the profile or the
     track title, channels, codec), Dolby Vision is read from the DOVI side data (profile 5, or 8 with compatibility
     id 0, has no HDR10 layer) and Hi10P / 4:4:4 video is found; the answer's `format` and `hdr` follow. The rank
     moves by the changed format parts; a finished good candidate of the same resolution whose corrected rank is
     more than 3 above the pick's takes its place (`tsProbeReorder`). A file the client cannot show right (DV
     profile 5 without `dv=1`, Hi10P / 4:4:4) is unfit: `tsChoose` takes it only when no other check is left.
     So is a file whose audio is a dub only (`tsWrongAudio`, `applyProbedLanguage`): every audio track carries a
     language tag and none is the title's original language or the profile's (`X-Lang`, so a Turkish dub is fine
     for a Turkish profile), e.g. an English film with a lone Russian track. An untagged / `und` track, or one whose
     title says "original" or names the original language, keeps the file; without a known original language
     nothing is judged. Unfit files are never spares or upgrades, but the only playable source still plays
     (logged as `has the wrong audio (audio only in ru, original en)`).
   - Client caps on the probed file (`caps.go`): names can say nothing about the file (Big Buck Bunny on TorBox,
     `big-buck-bunny-2008-4-k-60-fps-blender-animated-short-x-265`, is a 7 GB 2160p HEVC 10-bit file), so after the
     probe a file taller than the client's `maxq`, or whose video play has to re-encode for it (HEVC without
     `hevc=1`, AV1, VP9, MPEG-4, Hi10P / 4:4:4: whatever `videoPlan` does not copy), is unfit too (`applyProbedCaps`,
     logged `unfit: 2160p above the 720p cap, hevc yuv420p10le video is re-encoded`). An unfit result never starts the
     short grace (only the starving one, so fitting checks still running get their time) and never stops the other
     checks behind the TorBox gate. When the checks end with an unfit pick (or none), `capsFallback` checks up to 4
     more candidates: unchecked ones whose name may fit first, then those whose name breaks the caps (names above
     `maxq` included, which ranking otherwise rejects), cheapest first. A fitting extra pick replaces the first one
     when it is good, or when neither is good and it is the more playable one (a dead swarm never replaces one that
     delivers). Only when nothing fits does an unfit file play, then the cheapest (`tsPlayCost`: a copy above the cap
     before any re-encode, then the lowest resolution; a good one before a starving one); the answer carries
     `caps_fallback` with the reason and the log says `nothing that fits the client was found, falling back to …`.
     Without any name within `maxq` the names above it are checked the same way. For a capped client (`maxq`, or no
     `hevc=1`) a name without a resolution tag ranks after every labeled one (`tsUnknownRes`, res part -40) until
     probed. `x-265` / `h_264` count as codec tags.
   - Caps elsewhere: the source cache key holds the caps, and `cachedFor` drops a cached source that breaks them
     (its probe, else its stored `quality` and `format.video`) and resolves again; a source stored as a caps fallback
     is reused for these caps for 6 h only. Late results after a fallback pick may replace it with any fitting one
     (any rank). `sidegrade` clamps the playing resolution to `maxq`; spares, upgrades (`upgrade` offers nothing with
     `maxq`) and late candidates are never unfit.
   - Swarm health (`health.go`): a warm-up read from the middle of the file while TorrServer's stat is polled every 1 s,
     up to 15 s for the highest resolution on offer, 8 s for the others. It starts once the probe has read the header,
     at the latest 2.5 s after the file was picked (`tsProbeHeadStart`): the probe needs the start and often the tail
     (MP4 `moov` at the end, Matroska tags and cues), and a full-speed mid-file read on the same swarm made it 2-3x
     slower. A swarm that already delivers ≥ 3.9× the file's bitrate (and ≥ 2 MB/s, TorrServer's speed polled every
     0.5 s while the probe reads) has room for both: the measurement starts at once (`awaitHeader`, logged
     `warm: <hash> delivers … while its header is read`). Verdict (`window`): the rate delivered after the first
     1.5 s (the start burst) over the last 8 s: bytes read / time, or TorrServer's average speed when higher (its
     single highest poll left out once there are 4, so one speed spike never decides). It keeps up at ≥ 1.3×
     size/runtime once that covers 3 s (2 s at ≥ 3× that), and only when at least two polls delivered the file's
     bitrate (one piece is a burst, not a swarm); dead after 8 s without peers or data. BitTorrent delivers in bursts
     (whole pieces, peers unchoking in turns): the median of per-second rates used before read 0 in most seconds of
     swarms that delivered several times the need (Plan 9 from Outer Space YTS: "0.00 MB/s (peak 5.09)" for
     0.29 MB/s needed). The Little Shop of Horrors case (best 3 s said 1.05 MB/s, playback got 0.22) stays starving:
     its burst falls into the first 1.5 s. `peak` (best 3 s window) is logged and returned for comparison; the health
     log line also lists the median and every poll (`[median …; read/speed …]`, MB/s).
   - TorrServer's RAM cache (2 GB per torrent) serves bytes it read before (playback minutes ago, its read-ahead, an
     earlier check) without the swarm: a reuse check of The Mentalist S02E10 read "1157 MB/s … keeps up" from a
     swarm of one peer that delivered 0.04 MB/s hours later. So every measurement of a TorrServer read compares the
     bytes read with what the torrent downloaded meanwhile (`bytes_read_useful_data` of its stat, taken once before
     the read and with every poll): a read of ≥ 4 MB that got at least 4× what was downloaded came from the cache
     (`fromCache`) and is judged on the downloaded bytes and TorrServer's speed only (logged `(read from
     TorrServer's cache)`), so it never keeps up by cached bytes. A swarm verdict of a TorrServer read that is not
     cached is remembered per file for 3 minutes (`noteSwarm`, also the pick of a resolve) for the cached-source
     check below. A TorBox proxy read has no swarm and is not judged this way; a TorBox file read from TorrServer
     (TorBox paused) is judged on TorrServer's own stat.
4. `tsPickBestKeep` returns the best good candidate (keeps up, duration verified or runtime unknown) as soon as it is
   there, also from a running check's snapshot. It waits only while a better-ranked check of at least its resolution
   is still running and already delivers ≥ 0.8 of its need (`tsPromising`): a 3 s grace (15 s for a higher resolution
   without an upgrade path), cut short as soon as that check falls below 0.8 or finishes. Unmeasured or slow 4K checks
   are not waited for, they keep running for the upgrade path; checks of the picked resolution keep running too
   (possible spares). 12 s after the checks started (`tsResolveAnswerCap`, a
   hard wall that also cuts a grace short) it answers with a good one if there is one, else with the most playable
   source (`tsChoosePlayable`) among the finished checks and the snapshots of running ones (each check reports one as
   soon as its file is picked, then after every poll): alive (active peers or data, not dead) over a dead or silent
   swarm, fit for the client (not unfit, nor unprobed with a name it gets re-encoded by: an x265 for a browser
   only wins when no H.264 delivers, and a good one only answers once nothing else is left), within the known line (need ≤ 0.75 of it, the line as it stands now:
   checks that download fast raise it) over one that needs more, measured (a verdict, or ≥ 2 s
   of samples) over unmeasured (a 4K below 0.3 never beats a ≤ 1080p with a higher ratio), a verified duration over
   an unknown one (a failed probe, or one still running that the player would wait for) unless the verified one
   delivers < 0.5 of its need and the other clearly more, of two over the line the lower need (checks share the
   line, so their ratios say little), a clearly better rate / need ratio (≥ 0.1 higher and
   ≥ 1.5×), the lower need (bitrate) when the ratios are close,
   then active peers, connected seeders and Jackett seeders. When nothing is measured yet at the cap (or only ones
   below 0.1 while other checks still lack a measurement) it waits up to 4 s more (`tsResolveCapExtra`) and answers as
   soon as a measured one delivers ≥ 0.1 of its need. Only when no candidate even has its metadata does the first one
   to get it answer (within the 20 s check timeout). While the pick is not good every other check keeps measuring
   (any resolution); the pick's own swarm measurement stops once it is the source, while its duration check goes on, so
   its probe (the header the player reads next) is cached instead of thrown away. While the pick starves, an alternative
   that keeps up or is clearly faster (≥ 1.5× its rate and ≥ 0.5× its own need) becomes a spare and the cached
   source; one of a lower resolution only when it keeps up (`tsLowerSpare`), and `sidegrade` may then offer it.
5. `afterPick` (`upgrade.go`) sorts out the rest. Higher resolutions keep warming for up to 90 s: up to 4 distinct
   torrents, 2 at a time, a starving one gives way to the next after 2 rounds (each round re-measures a fresh part of
   the file; at most 2 background measurements and 3 titles at once). The first that keeps up with a verified duration
   replaces the cached source; `GET /api/torrent-stream/upgrade` offers it. Without a watcher (cached or saved source)
   that endpoint starts a search restricted to higher resolutions, at most every 10 minutes per title. A higher
   resolution over the known line is not kept running, warmed, offered or cached (logged `upgrade "<name>" needs …,
   the line does ~…: not warmed` / `not offered`): it would only stall later. Late results, spares, caps fallbacks and
   sidegrade / upgrade searches rank and pick the same way (`findSource`, `runChecks`). Up to 2 live
   alternatives of the picked resolution stay as spares; every other candidate is dropped from TorrServer at once.
6. A starting stream gets the line (`quiet.go`): once resolve has answered, its background work (checks that keep
   running, upgrade warm-ups and upgrade searches, spare measurements, late candidate checks, sidegrade searches, their
   probes) waits while another torrent starts: the player's `probe` of it runs, or its play stream is not past its
   start. A fixed 30 s was too short on a 28 Mbit line (a borderline 4K start stalled ~20 s after the background
   reads came back), so the start now ends once the stream is healthy: `play.go` counts the bytes it delivers, and
   it is past its start when it is 30 s of media ahead of real time (video copied; bytes at the rate of what the
   stream carries: the video plus the copied audio track it plays, from ffprobe or Matroska's `BPS` tag, else the
   file's bitrate), or has flowed 60 s without a 5 s gap and (rate known) kept up. A torrent's restarts and seeks
   before that are one start (`tsPlayRun`), capped at 120 s from its first stream; after that cap its restarts and
   seeks (the stall watchdog's too) join it without a new start, so a stream that keeps stalling never holds rescue
   work back for good. Only a seek after a healthy stream is a new start. Logged as `warm: deferring … while … starts` /
   `resuming … after …` and `warm: <hash> is past its start after 34s: 31 s ahead` (or `flows steadily`, `the start
   took too long`). Candidates that were not picked stop their duration probe while a stream starts and probe again
   (25 s) afterwards (on a 28 Mbit line a non-picked 1080p probe took 2.1-2.9 of 3.3 MB/s from the pick). While a
   player waits for a new resolve of the title (a step down with `maxq`, a slow-start handover with `avoid`, any
   capability variant), the earlier resolve's background work waits too (`while a player resolves the title again`),
   so the new checks get the line. A measurement interrupted before its verdict starts over
   afterwards; one that already has a verdict ends with it. Work on a TorBox file does not wait for a starting TorBox
   stream (CDN reads take nothing from a swarm; GoT S01E01: the 4K upgrade warm-up waited 48 s behind the 1080p TorBox
   stream it was to replace), only for a starting torrent. Checks before the answer (Play, prefetch on the detail
   page) never wait for a starting stream; another title's playback is a different rule (playback priority below).

The chosen source is saved in TorrServer's DB (`save_to_db`, category `nextflix`), so after a drop or a restart it opens
from its stored metadata (≈ 20 ms instead of seconds of metadata exchange). Closed entries older than 7 days are
removed at start and every 6 hours.

A resolve the source cache answers is checked before it answers (`verifyCached`, `reuse.go`): a source that played
fine hours ago may have lost its swarm (The Mentalist S02E10, TorBox paused: the cached 52d6688e opened with no data
for 11.6 s, then the player searched again). A cached source TorBox plays (TorBox on and not paused) answers at once,
and so does one whose swarm verdict is less than 3 minutes old (`tsCachedFresh`: the pick of a prefetch, a reuse or
an earlier check; logged `the cached … was measured … ago`). Any other one, a TorBox source while TorBox is paused
included, is added to TorrServer and measured like a saved source (below): up to 8 s where playback starts (`?t=`
of the TV, else mid-file), next to the probe the player reads next. Keeps up or unmeasured: it answers with the new
health (`the cached … was measured again: …, playing it`). Starving, dead or without metadata: the cached source is
dropped and the resolve runs fresh with its hash in `avoid` (`the cached … is not played, …: resolving fresh
without it`). The extra start delay: none for TorBox or a fresh verdict, about 3-5 s for a swarm that keeps up
(the verdict needs 1.5 s + 3 s of samples; the probe runs meanwhile), 8 s plus the search for one that starves.

### Playback timeline (`play.go`, `seekpoint.go`)

Every time the players show, save and look subtitles up with is file time: pts minus the file's `start_time`, as
VLC / mpv show it (also the time of embedded subtitle tracks, `-copyts -start_at_zero`, and of the speech tap). A
cue at T must show while the frame at T is on screen:

- Split streams (`track=video|audio`) carry file times (`-output_ts_offset t`, absolute `tfdt`), so position =
  the element's time.
- A muxed stream starts at 0 (`make_zero`), and position = seekpoint answer + the element's time. Its time 0 is the
  earliest first *decode* time of its packets: for video with B-frames the keyframe is shown two frames later.
  `seekpoint` therefore answers the file time of that time 0, not the keyframe: it copies the first video and audio
  packets with the same input options as play (`streamInput`, also `-probesize`, `-noaccurate_seek`) into ffmpeg's
  `framecrc` output (`firstPackets`, reads from the keyframe to t + 0.5 s) and takes the earliest decode time; a
  re-encoded video starts at t, shown `tsEncoderDelay` (2) frames after its first decode time. The players also
  ask it at t = 0 (the answer is then slightly negative, e.g. -0.083).
- The seek has to be play's own: ffmpeg seeks Matroska / MPEG-TS with B-frames to t - 0.13 s (no
  `AVFMT_SEEK_TO_PTS`), so a t up to 0.13 s after a keyframe starts on the keyframe before it. ffprobe's
  `-read_intervals`, used before, answered the later keyframe: the stream's positions and subtitles ran a whole GOP
  ahead (2-12 s) until the next seek. TV splices request their stream at the seekpoint's t, so a muxed splice was
  placed a GOP off the same way.
- No edit lists in muxed streams (`-use_editlist 0`): Chromium ignores an empty first edit (a track's start delay)
  while Firefox applies it; without them the muxer moves a later track's first sample to 0 in both.
- A re-encode keeps the source's frame times (`-fps_mode vfr`, `-enc_time_base 1/90000`); the default constant
  rate with a 1/fps time base put frames on a grid from t, up to 1.5 frames late. With copied audio
  (`-noaccurate_seek`) the frames between the keyframe and t are squashed into the first frame, which then shows
  until t while the audio plays from the keyframe (cosmetic; positions stay right).

Measured with `TestPlayTimeline` (`timeline_test.go`: generated files with the frame number drawn into each frame
and tone bursts at known times; H.264 with B-frames and keyframes 2-12 s apart, MKV / MP4 / MPEG-TS / MKV remuxed
from TS with `start_time` 11.4 s, audio or video delayed, 23.976 / 25 fps, MPEG-4 re-encoded; played through the real
`seekpoint` and `play` handlers for t on, just before and just after keyframes; every output frame's position
computed like the players, `tfdt` + composition offset) and in headless Chrome 155 / Firefox 155 (frame numbers read
from `requestVideoFrameCallback` frames, plain `src` and MSE). Before: muxed positions +62-88 ms in every stream
(Chrome, Firefox +83-126 ms), +3.0 / +5.7 s after seeks near keyframes. Now: copied video exact (0-1 ms), re-encoded
within one frame, split exact, Chrome ≤ 38 ms and Firefox ≤ 49 ms over 140 plays (Firefox's frame callbacks lag by up
to one frame); audio and the speech tap within 21 ms of file time (the fixtures' own AAC priming). Run it with the
Go test template in a container with ffmpeg (`TL_FULL=1` for all start times, `TL_KEEP=<dir>` keeps the outputs).

Left open: a split stream from t = 0 shows video with B-frames two frames late until the first seek (its first
decode time is before 0 and the muxer moves the track, ~83 ms; the split seekpoint answers the file time). MPEG-TS
sources: ffmpeg's `-ss` on TS lands on the keyframe *after* t (the seekpoint then answers `exact: false`) and
shifts split streams by 0.13-0.17 s (the seek's 0.13 s heuristic leaks into the timestamps; `-copyts` would avoid
it but needs another speech tap start); torrents rarely carry `.ts` files.

### Debrid (TorBox)

With a TorBox key (`/api/settings/debrid`, advanced settings), a release TorBox has cached plays from its CDN at line
speed instead of from the swarm (`debrid.go`, `debrid_resolve.go`, client in `platform/torbox`).

- Resolve: after ranking (before any TorrServer check) the hashes of the best 40 candidates are asked in one
  `checkcached` call (≤ 2.5 s; answers kept 30 min, cached and not cached alike). A failed, partial or malformed answer
  leaves a hash unknown, never "not cached" (asked again next time). With "prefer TorBox" (default on) every cached
  candidate is raised by the same amount within its resolution, so the best cached one ranks 5 above the best release
  of that resolution and the cached ones keep their order; other resolutions are untouched (a cached 1080p does not
  beat a 4K). Then the usual top 4 are checked.
- A cached candidate is checked without TorrServer: its file is picked from TorBox's file list with the usual
  movie / episode / pack logic (`tsPickFile`; the file's index is its place in that list + 1), the torrent is found in
  the account (`mylist`) or added (`createtorrent` with `add_only_if_cached`; nothing is downloaded), `requestdl` gives
  the HTTPS link, the duration probe reads it (the duration check still applies) and a short read stands in for the
  swarm health (`debrid_rate.go`): 3 bounded ranges of 16 MB from the middle of the file are read through the proxy in
  parallel for 2.5 s after the first byte; the rate leaves out the first 0.5 s (connection ramp), `peak` is the best
  1 s. One sequential 1.5 s read before read one cold CDN connection and underrated TorBox (Game of Thrones S01E01
  4K: 2.28 MB/s "peak 0.00" at resolve, 14.74 MB/s a minute later), so a cached 4K never won and the 1080p started.
  The rates TorBox delivered in the last 10 minutes are kept (these measurements and the proxy's CDN bytes per 2 s); a
  measurement below 1.3× the need whose first bytes came within 2 s keeps up when that best recent rate is ≥ 1.3× the
  need (logged as `TorBox line … MB/s`). Upgrade warm-ups of a TorBox file measure the same way. The answer carries `source: "torbox"` and `debrid: true`.
  When TorBox cannot give a link the candidate is checked through its swarm instead.
- The best ranked cached check goes first: checks ranked below it wait until it finished (≤ 5 s); once it is a good
  pick the ones of its resolution or lower stop (`a TorBox source of this resolution was picked`), so no swarm competes
  with it for the line. Higher resolutions still run and feed `upgrade`.
- Playback: hash / index keep naming the file on every endpoint (`probe`, `play`, `seekpoint`, `thumb`, subtitles).
  `fileURL` maps a debrid source to a loopback proxy (`http://127.0.0.1:<random>/f/<hash>/<index>`, not reachable from
  the LAN) that reads the TorBox link with `Range`: the link is cached 2 h (TorBox opens it for 3 h) and requested
  again on a 401 / 403 / 404 / 410. The subtitle module reads through the same proxy (moviehash range reads, embedded
  and live tracks, subtitle files of the torrent from TorBox's list), wired in `main.go`.
- Proxy chunks (`debrid_proxy.go`): one CDN connection gives ~18 MB/s and every new range costs 0.2-0.9 s before its
  first byte, and ffmpeg reads the header, the end and the cues of an MKV on separate connections before every seek
  (4 sequential requests, 2.2 s of first-byte waits for a 4K file). So the file is read in bounded chunks: a reader's
  position starts a 4 MB chunk, a reader that read 1 MB gets the next 8 MB chunk fetched ahead, from 4 MB on 3 of
  them in parallel (keep-alive pool, ≤ 8 idle connections, 90 s). Chunks are shared by every reader of the file and
  stay cached (LRU over all files, 256 MB, chunks in use never evicted; chunks fetched ahead of a reader count as just
  used, so health checks of other releases cannot evict them before the reader gets there), so the header / index / cue reads before a
  seek, the data around a paused position and a restart at the same spot never go out again. Readers get bytes as
  they arrive (no wait for the whole chunk). A chunk fetch without headers after 4 s or without bytes for 5 s is
  tried again on a new connection (DPI stalls) and resumed at its next byte, up to 6 failures / 2 link refreshes;
  a reader whose chunk gave up tries one new chunk, then ends (ffmpeg reconnects). Measured on Big Buck Bunny 4K
  x265 MKV (7.1 GB, 96 Mbit/s, `copy` to fMP4): first fMP4 bytes at t=0 1.75 s → 0.77 s, at t=300 4.2 s → 1.0 s
  (0.07 s again at the same spot), t=100 4.8 s → 1.2 s; 10 s of media at t=300 10.6 s → 5.9 s (line bound:
  ~25 MB/s over 4 connections); Big Buck Bunny 1080p MP4: t=300 1.4 s → 0.14 s.
- A file whose CDN reads gave up (after those retries) is marked broken for 2 min: `fileURL` then gives TorrServer's
  URL for it (the same file by path and size), so the player's silent restart plays the same release from the swarm
  (logged `torbox: <hash>:<i> could not be read, playing it from the swarm`). The TorBox transport remembers the
  address each host last connected to and dials it when the name does not resolve (Docker's DNS answers
  `server misbehaving` / times out now and then); TLS still checks the host name.
- TorrServer never loads a debrid source: its stat (pings, release names) and `files` (a player re-adding a saved
  or offered source) are answered from what is known, it is not
  saved to TorrServer's DB, and a cached debrid source is re-warmed by fetching its link and header. After a restart
  a cached source with `debrid: true` is registered again when it is read from `stream_sources`.
- Fallback: TorBox unreachable, rate limited (429), unauthorized or with an expired plan pauses it (30 s, 1 min,
  10 min) and everything takes the torrent path silently: resolve skips the cache check, and a debrid source plays
  from TorrServer (the same file found there by path and size). Switching TorBox off does the same. Uncached releases
  are never added, unless "add uncached to TorBox" (default off) sends the picked torrent source to TorBox afterwards
  (≤ 20 an hour) so a later play is cached.
- Players: the web player shows a small "TorBox" badge with an SVG bolt (neutral #e5e5e5, no emoji) next to the quality badge for a debrid source.
- Live check: `go test -tags live -run TestLiveTorBox -v ./internal/modules/streaming/` with `TORBOX_API_KEY` (and
  ffmpeg) checks the account read-only with the public-domain Big Buck Bunny torrent.

### Saved sources (`reuse.go`)

Continue Watching and a resumed episode play the torrent saved with the progress (`stream_progress.hash`,
`file_index`) without a search, but not without the debrid decision. Before, the players opened it straight away,
and since debrid sources are only known in memory (registered by a resolve), a resume after a server restart read a
TorBox-cached release from its swarm (Game of Thrones S01E01 2160p, 2026-10-09: 0.71 MB/s for 3.2 MB/s needed, no
TorBox log line, the player restarted the same swarm for 3 minutes; TorBox had the file cached). Now both players ask
`GET /torrent-stream/reuse` first (old servers: 404, the saved source plays as before):

- TorBox on and the hash cached (`checkcached`, the shared 30 min answer cache): the file is found in TorBox's list
  (the title's cached TorBox source, else the movie / episode by name like resolve, else the saved index when it is a
  video), registered, its link fetched, and it plays from TorBox (`debrid: true`, TorBox index). The header is read
  ahead (`debridRewarm`).
- Not cached (or TorBox off, paused, unknown): the torrent is added (stat, ≤ 10 s for metadata) and its swarm is
  measured for up to 8 s where playback resumes (the pieces the player reads next; the probe runs next to it). A
  read TorrServer served from its cache (see swarm health) is measured once more, half the file away
  (`swarmHealthAt`, logged `came from TorrServer's cache …, measuring at … MB`); when that is cached too the swarm
  counts as unmeasured. Keeps up (≥ 1.3× size / runtime) or unmeasured: it plays with that health (the start buffer
  uses it). Starving, dead or without metadata: `resolve` (the player resolves again with that hash in `avoid`,
  which picks a TorBox or faster release; the torrent is dropped). The resolve that follows checks a cached source
  the same way before it answers with it (see above).
- A saved source that a step down picked (its hash is a cached source of a `|max…` key of this title) gives way to
  the title's uncapped cached source for these caps: after a slow swarm stepped down to 1080p, progress saved the
  1080p and every resume played it.
- Without the endpoint (an old client), `files`, `probe` and the subtitle reads adopt a hash that a cached source
  (`stream_sources`) names as TorBox (`adoptSaved`, looked up at most every 10 minutes per hash), so it plays from
  TorBox too. The subtitle reads (moviehash range reads, embedded tracks) go through `fileURL`, so they read the
  proxy whenever the file is a TorBox source; an embedded-track ffprobe killed at its time limit on a slow swarm
  counts as temporary (listed again on the next call, not cached as failed). Subtitle lists never hold up the play
  stream (they answer after their wait with what is there).
- Measured (web, headless Chrome, test server on `nextflix_default` after a restart, Night of the Living Dead 1968
  resumed at 20:00 from Continue Watching): a saved TorBox-cached 1080p OFT (a strong swarm too) played from the swarm
  with the first frame after 5.8 s, now from TorBox after 6.5 s (2.4 s of it the reuse check: `checkcached`, the
  TorBox torrent and its link). A saved torrent without a swarm: with the old server (the player's 12 s hand-over
  already on) the first frame came after 29.5 s; now `reuse` answers `resolve` after 10.4 s and the first frame comes
  after 12.3 s at 20:02 (without the hand-over the old player restarted the same swarm after 30, 60 and 90 s). GoT S01E01 4K took 3+ minutes from its swarm; TorBox has that file cached (`checkcached`), so the
  resume now plays it from TorBox.

### Films in cinemas with a home release (`homerelease.go`)

TMDB's digital dates lag the real ones: a film it lists as only in cinemas can already have WEB-DL releases (The
Odyssey 2026 on 2026-10-09: TMDB said "digital 2026-11-17", the indexers had AMZN WEB-DL and WEBRip releases next to
TELESYNC / HDTS recordings). So for a movie whose TMDB verdict is `in_theaters`, or `not_released` with a release
date that has passed (or none) or a digital date ahead, the catalog asks streaming (`catalog.HomeReleaseFinder`,
wired in `main.go`) before answering:

- `HomeReleaseOut` runs the resolve search without TorrServer (`searchRanked`: TMDB's English title, year, other
  names and directors, the same early cut; when nothing usable came early it waits for the slower indexers, 15 s in
  all) and ranks it with `tsRankAll`. A home release is a candidate that names a home source (WEB-DL, WEBRip, web,
  BluRay, HDTV, ...; never a cinema tier: CAM / TS / TELESYNC / HDTS / TC / SCR are rejected by `tsScore`, R5 / LTE
  / DCPRip / hardcoded subs are the cinema tier), passes the title, year, bonus-material and namesake rules, starts
  with the title (after a `www.site.org - ` prefix: "George A. Romero's Resident Evil (2025)" is another film) and has
  10 seeders. Fake WEB-DLs of films in cinemas are common, so these must come from 2 release groups (the last word
  of the name): Resident Evil (2026) three weeks into its cinema run had one uploader's "WEB-DL" (4581 seeders) and a
  0.9 GB "AMZN WEB-DL" with 5 seeders, and stays `in_theaters`; The Odyssey had Kitsune, QxR, LAMA, YTS, ...
- The verdict is kept per film in memory: found 6 h, cinema recordings only (or nothing) 1 h, a failed search 5 min.
  Concurrent callers share one search; at most 3 searches run at once. A caller waits up to 4 s; a slower search
  answers "not found" now and caches its verdict for the next call.
- Found: `/api/availability` answers `available` with `digital_release_found: true` (the TMDB dates stay), so the web
  and TV detail pages, billboards and hover previews show a normal Play instead of "In theaters · Streaming from
  ..." / "Sinemalarda · Dijital çıkış: ...". Resolve, prefetch, the instant-play check and `resolve?dry=1` get the
  same verdict through `notOut` (its wait covers the 4 s) and search normally; the file check (duration against TMDB's
  runtime) is unchanged. Not found: the 425 / `not_released` answers as before.
- Wrong lengths (`homedur.go`): the "WEB-DL"s can be another video under the film's name (The Odyssey on 2026-10-09:
  the Kitsune, QxR, LAMA and YTS "WEB-DL"s were all 86 min for a 173 min film, the making-of, and every resolve
  ended in 404). When no probed home release has the title's length and at least 2 have another one
  (`tsHomeMislabeled`), the verdict turns to not found for 6 h and their hashes never count as a home release again
  (7 days): the film is `in_theaters` again (theater banner, 425). Two checks feed it: resolve's own candidate checks
  (`noteHomeDurations`, while the film counts as out only through its home releases; the resolve then answers 425
  `in_theaters` instead of 404), and the home check itself: after a found verdict it probes the headers of up to 3
  TorBox-cached home releases through the debrid proxy in the background (one film at a time; nothing is streamed),
  so the banner is back seconds after the first look (live: 3 files of 86 min, `in_theaters` 4 s after the search).

### Cinema recordings on request (`cinema.go`)

A film that is only in cinemas (`in_theaters`) can still be watched as a cinema recording when the viewer asks for it:
the web and TV detail pages show a secondary "Sinema kaydını yine de izle" / "Watch the cinema recording anyway" button
under the theater banner (movies only). The first press shows a one-line warning (low picture and sound quality), the
second plays with `cinema=1` (web: `CinemaRecordingButton`; TV: the button in the detail actions, reachable with the
D-pad, its warning in the action caption). Every call of that playback carries the flag (`StreamMeta.cinema` →
`paramsOf`: resolve, prefetch, sidegrade, upgrade, subalt, release), and the players show a neutral "Sinema kaydı" /
"Cinema recording" badge (lucide clapperboard, #e5e5e5) like the TorBox one.

- Server: `cinema=1` skips only the `in_theaters` 425 gate (`notOut`; `not_released` stays blocked). Recordings
  (TS, TELESYNC, HDTS, HDTC, CAM, screeners, R5, DCP rips, hardcoded-sub leaks), rejected otherwise, are ranked: the
  source part is `tsCinemaBase` (45, one resolution step above the home sources, which are fakes while a film is in
  cinemas; a real one passes the duration check) plus the capture: DCP rip 34 > HDTS / TELESYNC / HDTC 30 > screener
  26 > R5 24 > TS / TC 20 > other 10 > CAM -20, "HQ" +5, burned-in subtitles -15 (Korean ones fine for a Korean
  profile); a name that tags the profile's audio language (`TR`, `Türkçe Dublaj`, `ENG`) gets the multi-audio part.
  Bonus material, namesakes ("NOT the … film"), other years and the duration check apply as always. The source cache
  key carries `|cinema`, so a normal resolve never reuses a recording, and normal resolves, upgrades, sidegrades and
  late results never rank one. The answer carries `cinema: true` for a recording; upgrade searches of a cinema
  playback rank recordings the same way, so they only leave the tier for a home release that passes the duration
  check. Live (The Odyssey, 2026-10-09): `dry=1&cinema=1` lists "1080p HQ HDTS" and "1080p TELESYNC" releases first.

### Instant play hints

Title cards show a small "⚡" (tooltip "Anında açılır" / "Instant") when the title has a release TorBox has cached,
so Play starts from the CDN at once (`instant.go`, `POST /api/torrent-stream/instant`).

- Check per title: the resolve search without TorrServer (`searchTitle`: Jackett and Bitsearch, TMDB's English title,
  year and other names, the same early cut; slower indexers may add results for up to 12 s more), ranked by `tsRankAll`
  for a browser client. The best 20 hashes, leaving out cinema recordings, other years (ranking rejects them) and
  releases below 720p, go to one `checkcached` call (the shared 30 min answer cache, so a Play right after reuses it).
  Any cached → true; none → false; nothing usable found → false; a hash TorBox did not answer (failure, pause) or a
  failed search → unknown. TV shows are checked by S01E01 (the episode a Continue Watching card names, when given);
  season packs count. A title that is not out yet (a film in cinemas without a home release, see above) is false
  without a search.
- Answers are kept per title (`movie:<id>` / `tv:<id>`) in memory: true 6 h, false 1 h; an expired one is still
  served (up to 24 h) while it is checked again. Unknown answers are retried after 10 min at the earliest.
- The endpoint never waits: it answers what is known and queues the rest (a client's titles before prewarm ones,
  ≤ 300 queued). Two workers check one title each. Before a check and every second during it the gate is asked:
  a stream plays (or ended < 20 s ago), a stream starts, a resolve job runs (Play, a detail-page prefetch) or TorBox
  is paused → the check waits, and a running one is cancelled and queued again at the front.
- Prewarm (`StartInstant`, wired in `main.go` with the Top 10 of Turkey and global and TMDB's top 10 trending movies
  and shows of the week): 2 min after start and then every 10 min, only while nothing is queued or running and the
  gate is open, titles without a fresh answer are queued, at most 60 an hour.
- TorBox off (no key or disabled): every answer is null and nothing is searched.
- Web (`shared/lib/instant.ts`, `shared/ui/InstantChip.tsx`): cards ask once their row is near the viewport; the asks
  of one render are batched into one POST (150 ms debounce, ≤ 100 titles); unknown titles still on screen are asked
  again after 20 s, twice at most; known answers are reused for 10 min. Home and catalog cards and Continue Watching
  show the chip in the image corner, the hover preview "⚡ Instant" next to the quality badges, the detail page
  "⚡ Anında" next to Play. The TV app (`tv/webos/src/components/instant.ts`, `Row.tsx`) does the same for the cards
  that load their image (an SVG bolt, the TV has no emoji font).
- Measured from a test server on `nextflix_default` (27 titles: Top 10 Turkey movies and shows plus 7 others):
  one check took 0.8-4 s (13 s for a title without a cached release, which waits for the slower indexers); all 27
  were known 40 s after the first request (2 at a time); 26 of 27 had a cached release (10-20 of the best 20 for
  popular titles, 1 of 2 for small Turkish shows). The endpoint answers in 1-2 ms, first call or cached. A first
  visit of the web home page (fresh server) asked 145 titles in 5 POSTs (rows as they came near) and re-asked them
  in 2 POSTs after 20 s, when 24 chips showed; the server checked 146 titles in about 4 minutes (127 cached,
  19 not, none unknown), and a second visit showed 171 chips at once from 3 POSTs. A detail-page prefetch paused the checks (`waiting while a resolve runs`, one
  running check stopped and queued again).

### Trick play thumbnails

`GET /thumb` (`thumbs.go`) gives the frame a seek lands near, for the TV's scrub strip and the web seek bar:

- One ffmpeg per frame: `-noaccurate_seek -ss <t>` on TorrServer's stream URL, one decoder thread, `-fps_mode
  passthrough` (else the keyframe before `t`, whose time is negative after the seek, is dropped and ffmpeg decodes on
  to the next one), one MJPEG frame scaled to the width (display aspect kept), HDR tone-mapped like the re-encoded
  play stream. No `-skip_frame nokey`: H.264 counts only IDR frames as key, and on Night of the Living Dead (Matroska,
  I-frame keyframes) that read 9-34 MB per frame instead of about 0.5 MB.
- Times snap down to a 10 s grid (clients ask coarser steps as multiples); widths to 160 / 240 / 320 / 480 / 640.
  Frames are cached on disk in `/app/data/thumbs/<hash>/<index>/<t>_<w>.jpg` (`THUMB_CACHE_DIR`), written through a
  temp file, capped at 500 MB: the least recently used (mtime, refreshed at most hourly on a hit) go first, down to
  90 %. A frame ffmpeg could not make is remembered for 1 minute (404).
- Limits: concurrent requests for one frame share one job; one ffmpeg per torrent and 2 server-wide. A request waits
  up to 6 s (then 504); a job whose request timed out finishes anyway (up to 15 s) so the retry is a hit. A job
  whose viewers all aborted their requests is cancelled only when the viewer's latest request is more than 2 grid
  cells away (the scrub moved on); one next to it finishes and is cached.
- Never at the expense of a stream: a cached frame is always served; otherwise the pieces are only read for the
  playing torrent while its stream flows (a seek's new stream that has not sent bytes yet, or a running player probe,
  answers 204), or for any torrent while nothing else plays or starts (204 otherwise). A running frame counts as a
  seekpoint for the stream guard, so its torrent is never dropped under it.
- Prefetch (`POST /thumb/prefetch`): the TV asks every 20 s for the grid points from 30 s behind the position to 30 s
  ahead within its buffer (pieces TorrServer already holds). They are made one at a time, only while the torrent's
  stream flows and is older than 30 s, no other torrent starts and no viewer waits for a frame; the queue keeps 24,
  each at most a minute old.

Measured from a test container on `nextflix_default` (Night of the Living Dead 1968, 1080p H.264 Matroska, w=320/480):
a disk hit 2-5 ms; a fresh frame whose pieces TorrServer holds 120-250 ms; during playback at a new position
0.1-1.5 s (one 3 s); on an idle torrent 0.4-0.8 s (before the fix above 4-6 s); while a held key fires a request per
step, the queued ones arrive after 0.6-1.6 s.

Clients: the TV (`tv/webos/src/player/TrickPlay.tsx`, `thumbs.ts`) shows a strip while Left / Right scrub (target
frame large with a white border, ±1 and ±2 scrub steps smaller and dimmer, widths following the video's aspect
ratio, the target time below; placeholders with their time until a frame arrives). It loads two frames at a time,
the target first, then the next one in the scrub direction; requests are not aborted (their frames are cached either
way), 204 / 504 are asked again after `Retry-After`, and frames are kept as object URLs (150). Seeking never waits for
them. The web player (`components/SeekThumbnail.tsx`) shows one frame above the seek bar on hover and drag:
requests are snapped to the 10 s grid (moving inside a cell asks nothing), debounced 80 ms and never aborted (an
aborted request made the server stop the frame, so nothing was ever cached and no frame showed); at most 3 are in
flight and only the newest waiting one is kept, older ones still finish and fill the memory cache (120 frames).
Until the hovered frame arrives the nearest loaded one (within 60 s) shows dimmed with a spinner; 204 / 504 are
asked again after `Retry-After` while that cell is still hovered. While playing it asks `thumb/prefetch` every 20 s
for the grid points 30 s around the playhead.

### Subtitle alternatives (`subalt.go`)

Every finished candidate check records its result with the subtitle languages it found (per title, kept 2 hours,
with the client caps it was checked for), and each resolve remembers its best 12 ranked candidates. `GET /subalt`
(asked by a player whose file has no subtitle in the profile language, see below) offers a recorded release that
has it: checked for the same caps, not the playing or an avoided torrent, fit for the client, of a fitting length,
kept up when it was measured (within 30 min), of the playing resolution, a higher one, or one step lower (never below
720p unless the playing one is; no 4K for non-HEVC clients; the `maxq` cap holds); the playing resolution first, then
the most headroom. Without one it starts a background scan (one server-wide, at most every 10 min per title; a title
played from the cache is searched first, no checks): up to 3 untried candidates of an acceptable resolution, the
ones whose name hints at the subtitle first, are added to TorrServer and only their header is probed (ffprobe, the
quiet gate and playback priority apply); the first one with the subtitle is then checked like a resolve candidate
(swarm health in a background measurement slot, length), which records it for the next poll. Probed torrents are
dropped again; a hit stays in TorrServer's DB so switching to it opens fast. The cached source is not changed.

### Warm-up policy (`warm.go`)

A title (resolve key) is only warmed on intent, and every torrent it adds is recorded in its `tsWarm` so it can be
dropped again. A torrent one another live title holds (a season pack shared by two episodes) is not dropped.

Stream guard (`guard.go`): every drop goes through `dropTorrent`, which refuses a torrent a player uses: a running
`play` stream, one that ended less than 60 s ago (seek, restart, switch), or a running `probe` / `seekpoint`. It logs
`warm: keeping <hash>: playing (<why>)` and the janitor retries the drop once the torrent is free (and no live title
holds it by then). This covers playback priority, release (immediate, delayed, `hash=`), hold expiry, resolve / upgrade /
sidegrade / spare cleanup and the TorrServer DB prune. A torrent is linked to every title (resolve key without the
capability suffix) that tracked, picked, spared or cached it, also after that warm was released; a stream of a saved
source is linked through the source cache when it starts.

- Intent: `prefetch` (detail page opened, next episode), `resolve` (Play; a cached answer re-holds that source),
  `upgrade` and `sidegrade` polls. Card and episode hovers only ask `resolve?cached=1`, which never holds anything. A
  prefetch of a cached source re-adds it (from the DB) and reads its header (peers connect, probe cached).
- Held torrents without readers are pinged every 20 s so TorrServer keeps them open; a title held 15 minutes without
  playback releases itself.
- `POST /release` with the title: while a player reads one of its torrents (any capability variant) the release is
  checked again after 5 s and ignored if that still holds (`ignoring the release of …`), so a stray release never ends
  the playing title. Otherwise it cancels its background work (resolve job unless a player waits for it, upgrade
  warm-up, late indexers, spare measurements) and drops its torrents after 2 s; with `delay=60` (detail page left
  without Play) it waits first and any new intent cancels it. With `hash=`: the torrents a closing player used.
- Spares are dropped 60 s after the pick without playback, or 3 minutes into playback (counted from the spare's
  arrival or the player's last `draining=1` poll when those are later). `GET /sidegrade` offers only a spare that
  keeps up (`tsSidegradeWorthy`), measured within 45 s (else it measures them in the background), and, when the
  playing source's last measurement kept up too, has ≥ 1.5× its rate / need (against an unmeasured, starving or dead
  source keeping up is enough); at most one per title every 2 minutes, never one in `avoid=` (such spares are
  dropped). A spare that does not keep up is never offered, whatever the player reports: a switch reloads or
  restarts the player (the TV once left a 2160p pack measured at 17.83 MB/s for a 1080p at 0.51 MB/s). For a
  starving source a spare of a lower resolution counts too when it keeps up (the playing resolution or higher is
  preferred).
- A draining player (`draining=1`, polled every 5-10 s while its buffer drains) keeps the spares alive;
  `critical=1` (under 6 s of buffer) also allows a lower resolution. When no spare is left to measure, it starts a
  background search (`searchSidegrade`, at most every 3 minutes per title): the title is resolved again capped at the
  playing resolution, without the playing torrent and the avoided ones; its checks are background work (quiet gate).
  It never starts before the playing torrent's stream sent its first bytes a minute ago (a `critical=1` during the
  start buffer once ran a whole resolve next to the player's own step down), while a player waits for a resolve of
  the title (step down, `avoid`), or without a known playing resolution (it would search uncapped); logged as
  `warm: deferring the sidegrade search of <key>: <why>` (at most every 30 s). A source measured below 0.5 of its
  need waives the minute (first bytes still count). While the title's source starves (measured below 0.5 and its
  stream not healthy yet, or a `draining=1` / `critical=1` within 30 s) the search's checks and the spare measurements do not wait for a
  starting stream (`rescueCtx`; logged `… does not wait for a starting stream: its source starves`): that start may
  be the failing one (Night of the Living Dead, 28 Mbit: the pick at x0.11 held the check of a YTS that kept up for
  55 s, until the start cap). Upgrades, late checks, other titles and subtitle reads still wait, and the search
  ends once a player resolves the title. Only a pick that finished good (keeps up, fitting length) becomes a spare, never the search's cap answer ("best so
  far"); a later poll offers it under the rules above. `upgrade` likewise offers only a finished, measured
  higher resolution that keeps up with a fitting length.
- TorrServer's `TorrentDisconnectTimeout` is 45 s: only the safety net for torrents Nextflix lost track of.

Playback priority (`priority.go`): while a `play` stream runs (or ended less than 20 s ago: a seek, the next
episode), every other title gives way so the playing file gets the whole connection. Exempt are the titles of the
playing torrents (any torrent the title resolved, prefetched, cached or holds: its pick, spares, candidates, upgrade /
sidegrade offers, the hidden seamless-switch stream, also when another capability variant or a released warm found
it; and any other episode or capability variant of the same movie / show: same type, title and year), a next-episode prefetch (`next_pos`), and a title a player asked for within
2 minutes (`resolve`, `upgrade`, `sidegrade`: a second client pressing Play). A title that gives way:

- Its work waits in the quiet gate (`waitQuiet`, also the running reads of `measureOnce`), before its resolve answered
  too, logged as `warm: deferring … while <hash> plays`. A measurement waits before it takes one of the background
  measurement slots (`warmSlot`), so a paused title never blocks the playing one's spares or upgrades. A resolve that
  ran out of time while paused does not count as failed.
- The janitor (every 5 s) pauses it (`warm: playback priority: pausing <key> (n torrents) while <hash> plays`), saves
  its torrents in TorrServer's DB (metadata only, category `nextflix`) and drops them on the next tick (`dropped …
  (kept in TorrServer's DB)`); held torrents of a paused title are not pinged. A torrent the stream guard protects or
  that an unpaused title holds stays.
- A detail-page `prefetch` answers `deferred` (holds the title, loads nothing).
- When the last stream ended (+20 s) it resumes (`resuming <key> (n torrents back)`): its dropped torrents are added
  again from the DB (a stat), its waiting work goes on and a deferred prefetch starts.

The player (`web/src/features/watch/components/TorrentStreamPlayer.tsx`) polls `upgrade` 5 s after the start, then every
10 s during the first 5 minutes while a source below 2160p plays on an HEVC client (a poll that has to wait logs why
in the console: quality unknown, no position yet, another switch loads, retry pause). At most one upgrade per session
(a dropped preparation is tried once more after 60 s, 15 s for a TorBox source, see below). After a step down only a
TorBox offer is taken (it plays at line speed, and the step down came from a slow swarm); taking it lifts the cap
again (GoT S01E01: the TorBox 4K comes back after the step down to 1080p).
A finished upgrade is logged (`[nextflix] upgraded to <hash> (TorBox) 26 s after the start (offered at 5 s; ready
in 4.1 s, lead 20 s)`) and the release of the replaced torrent carries it as `why=` (`player: dropping …` in the
server log), so a slow switch shows its cause (late offer, slow preparation, overtaken / keyframe / seek restarts).
When playback stalls (two stalls within 60 s or one over 8 s) it first asks `sidegrade` for a faster source of the
same quality (at most every 2 minutes; a source resolve measured as starving (`keeps_up: false` with a measured rate below need, not a measurement
still running) is checked 8 s after it is ready, then every 15 s during the first 3 minutes, before the first frame
too, sending `draining` / `critical` only when the buffer verdict says so; a switch before the first frame starts at the
resume position), else it re-resolves with `maxq=1080`, then `720` (before the first frame the slow start below
steps down instead). The cap is set only when the lower source is switched to: an answer that does not keep up
leaves it (later sidegrades still see spares of the playing resolution) and the step down may run again after 60 s.
A probe that reports `duration_mismatch` re-resolves with the file avoided, at most twice. While a sidegrade loads
in the hidden slot (below), a stall does not step down: the step down waits for that preparation and runs only if it
fails. A sidegrade that keeps up but could not be lined up seamlessly (TorBox, or no keyframe far enough ahead after
3 tries) is switched to with a restart (loading screen) instead of stepping down; a keyframe failure never marks the
source as slow.

Slow start (web and TV): a torrent source (not TorBox) that shows no frame 12 s after it opened (`SLOW_START_MS`, the
probe included) hands over at once: a sidegrade that keeps up, else the title is resolved again with that torrent in
`avoid` (no `refresh`, so a TorBox source in the source cache is kept; a cached source that is in `avoid` is not
answered, resolve searches again). A handover never asks above the starving source (`handoverPlan`): a source
above 1080p steps down to `maxq=1080` (the sidegrade too); a 1080p / 720p swarm is replaced at most at its own
resolution, so a TV without `maxq` never lands on a 4K. A swarm slow to wake is no proof the line can't carry the
file (Detour 1945: a starving YTS 1080p needing 0.28 MB/s was handed to a 720p needing 1.84 MB/s from 2-3
seeders), and the stall step down still runs once it plays. A 4K's lowered cap is kept only when its answer plays
well (keeps up, unmeasured, or measuring at its need); a starving answer still plays, uncapped. A 1080p ask for a
4K that finds nothing resolves at the item's original cap (1080p is not asked again for the item). Up to 3
handovers per item (`MAX_AUTO_REFRESH`); the last one resolves without the torrents earlier handovers left, still
avoiding the one it replaces (as does any handover whose resolution has nothing new), so the server's fresh checks can pick an earlier swarm that
woke up meanwhile (Detour: the first YTS answered its probe 13 s after it was left). Its source gets 30 s for a
first frame (`LAST_START_MS` after the slow start), then the error with Retry. Worst case: 3 × 12 s + 30 s = 66 s
plus 3 resolves; it used to be 3 handovers and then 30 + 60 + 90 s restarts of the same swarm (4-5 minutes).
Live (28 Mbit, 4K first): handovers at +20.8, +63.4, +88.7 s and the first frame at 122.7 s before this.

Recovery chain (web `TorrentStreamPlayer.tsx`; the TV engine has the same restarts, chain and pause keep-alive,
without the kept frame): a torrent without a first frame goes through the slow start above (its stall watchdog
waits during the last try); a stream that breaks before its first frame (an
element error: the server dropped or cut it) is restarted silently at the same position once per torrent; a stream
that broke after playing (element error, ended well before the file end, stall watchdog, a paused element left without
media) restarts silently up to 3 times. After that the source is replaced: the title is resolved again (`refresh`, a
fresh torrent in `avoid`), up to 3 times per item. A TorBox file whose CDN reads gave up is meanwhile served from
TorrServer by the server (see Debrid), so the silent restart already plays the same release from the swarm. An error
shows only when the whole chain failed, with Retry; for a TorBox source it is neutral ("Playback could not start" /
"stopped and could not be resumed"), never the "not enough peers" wording. While the chain runs the screen keeps one
state: the last frame stays on screen, dimmed, under the empty video (a canvas in the parent, so it survives the
new PlayerCore of a replaced source) with a small spinner and "Switching source…" / "Kaynak değiştiriliyor…", or the
plain loading screen when no frame was shown yet; no toasts per attempt, no error screens between attempts. Every
restart keeps the last frame the same way (seeks, audio track changes). The loading overlay shows only after 400 ms
without frames and goes once frames ran 300 ms (`useSteady`), and the kept frame goes once the new stream played
0.3 s. Restarts log `[nextflix] restart: <reason>` to the console. Browser tests: with localStorage `nfx-debug=1`,
`window.__nfxFailPlays = n` makes the next n play streams fail.

Pause: while the viewer pauses, the player sends `POST /keepalive` every 15 s (the server keeps the torrent guarded
and prioritized, the title held, and a TorBox file's link and CDN connection warm). A restart while paused (the
stream died) only gets the new stream ready (`preload=auto`, no autoplay), so Play starts at once. After a pause of
10 s or more a dead element restarts on Play right away; a stall within 5 minutes after such a resume restarts at
once when nothing arrived since the resume (the browser only played what it had cached: Chrome keeps ~25 s of a
progressive stream beyond `buffered`), else after 2.5 s without buffer growth (also for a stream that delivered nothing
for 10 s) instead of the usual 12 s. Measured with a stream frozen during a 150 s pause (ffmpeg stopped): resume
plays at once, the stall when the cache runs dry lasts 2.8 s (torrent, Night of the Living Dead) / 3.9 s (TorBox
4K re-encoded) before frames run again; a healthy stream resumes at once.

Adaptive start buffer (`web/src/features/watch/startBuffer.ts`, ported to `tv/webos/src/player/startBuffer.ts`):
a source whose resolve health (`rate / need`) is below 1.3× starts paused (`preload=auto`, still downloading) until
8 s are buffered ahead, at most 4 s after the first frames were playable (`readyState` ≥ HAVE_FUTURE_DATA); a
source at 1.3× or more, an unmeasured one and a TorBox source (line speed) start at once. Only the first stream of a
source holds; seeks and restarts play at once. Once playback started, a `waiting` is only a stall when it lasts
800 ms (`STALL_CONFIRM_MS`: still below HAVE_FUTURE_DATA or the position did not move; `playing` cancels it):
only then the player counts as buffering (overlay, `stalled()`, no buffer health samples) and, when not seeking,
holds. Shorter waits (a few frames' gap, a decoder hiccup) used to pause the element for the hold and flash
"Buffering..." for under a second, a few times per episode; now they play on unseen (web and TV alike; the web
overlay keeps its 400 ms / 300 ms hysteresis on top). After a stall
any source is held paused until 4 s are buffered, at most 6 s. Either hold also ends
when the buffer reaches the end of the file or the browser stopped fetching with ≥ 2 s buffered. The normal loading
screen shows with a small "Buffering for smooth playback…" line. A hold counts as a stall for the watchdog (only a
buffer that stopped growing restarts the stream), the long-stall step down and the seamless switch (`stalled()`);
its pause is not the viewer's (no `playing: false`, no progress save). Play / Pause during a hold: Play during the
start hold starts now, Pause during a stall hold stays paused. A seamless switch ends any hold.

Proactive sidegrade (`web/src/features/watch/bufferHealth.ts`): once a second while the active stream plays (not
paused, buffering or seeking) the player samples the position, the end of the buffered range that holds it and
whether the browser is still downloading (`networkState`). Over a sliding 10 s window (at least 6 s of playback; a
seek, restart, switch or a pause resets it) the growth of the buffered end per second of playback gives the verdict:

| State | Rule |
|---|---|
| healthy | growth ≥ 1, or ≥ 0.9 with ≥ 25 s buffered; the buffer reaches the end of the file; the browser stopped downloading on its own (`networkState` IDLE or a `suspend` event since the last sample, whatever is buffered: Chrome keeps only ~2.5 s of a progressive stream ahead and fetches it in bursts) and for 15 s after that unless < 1 s is buffered while it fetches (critical); real stalls go through `waiting` |
| draining | growth < 0.9, or growth < 1 (shrinking) with < 25 s buffered |
| critical | growth < 1 with < 6 s buffered; < 1 s buffered while fetching within 15 s after the browser idled |

While draining or critical it asks `sidegrade` at once, then every 5 s backing off to 10 s (a 204 just means the
server is still measuring its spares), never while another preparation runs, at most every 2 minutes after an offer
(the server's own limit) and at most one source switch (any kind) per 60 s. An offer is prepared in the hidden slot
like any switch, but its lead stays inside the visible buffer (`min(8 s, max(3 s, buffered ahead − 2 s))`; a retry
after a keyframe came out too close adds the lead used so far on top, past the buffer if need be), and with
less than 13 s buffered 2 s from `t0` count as ready, so it takes over at a frame boundary before the visible stream
runs dry; no toast, no overlay. A sidegrade still loading is dropped (its torrent released) once the visible stream
was healthy again for 15 s. Torrents the player left for being slow (any sidegrade or step down away from them) and
sidegrades whose preparation failed are never switched to again in the session (an offer of one is released). The
TV app (`tv/webos/src/player/engine.ts`, one `<video>`) uses the same verdict, sized to its MSE byte budget (a 4K
stream fits ~30 s in all, so the draining / critical levels shrink with it and a full buffer is healthy; a download
the player paused on purpose never counts as draining). Only an offer measured clearly faster (rate / bitrate ≥ 1.3
and above the playing source) is held; while the buffer drains it is spliced in without a restart (see TV splicing
below); a restart onto it only follows a real stall (waiting with under 1 s buffered for 3 s, not the start hold).
The offer is dropped after 15 s of healthy playback. Every restart logs `[restart] <reason>` to the console
(`window.__nxPlayer` is the engine for DevTools).

TV splicing (`tv/webos/src/player/mseStream.ts`): upgrades, sidegrades and audio track changes go into the playing
MediaSource like a DASH quality switch. The new stream is requested about 10 s ahead (3 s for audio) inside what is
buffered, at a `seekpoint` keyframe; it is held in memory until it covers 4 s (audio 2 s) past its start, then each
SourceBuffer drops its data from that start on, switches codecs with `changeType` and takes the new stream with a
`timestampOffset` (muxed: seek point − player offset; split: 0). Data that cannot be ready 1 s before the cut, a
seek or a close drops the preparation and nothing changes. Measured on an LG TV (Chrome 120): H.264 → H.264 of
another release plays across the cut with no waiting and no frozen frame (the old stream loses a few B-frames just
before the cut; a stop there would jump to the cut). Another codec family (H.264 → HEVC) is refused: after
`changeType` the TV never shows a frame of it (the clock stops at the cut and seeks on either side never finish), so
such an upgrade restarts on the new source at once when it arrives within the first 90 s of playback (a short
loading screen beats the lower quality for the whole title), later it waits for the next seek, which restarts anyway.
The TV polls `upgrade` 5 s after the start, then every 10 s. A source of another length (outside max(30 s, 1 %), see
`switchLength.ts` below) is not taken; another cut more than 10 s off is not spliced (a restart onto it maps the
position by the length ratio),
and a switch with a restart never starts at 0 from a position past 30 s: a file that does not reach the position
(a pack's wrong file) sends the player back to the source it came from.

TV split mode: when the server's probe says `split` and the selected audio track is one the TV plays only on its
own (E-AC-3 / AC-3, `split_direct`), the player requests `track=video` and `track=audio` (`docs/API.md` "Split
streams") into two SourceBuffers (each with its own byte budget: ~79 MB video, ~18 MB audio measured), plays on the
file's timeline (offset 0, no `seekpoint`) and keeps Atmos. A server without `split` gets the muxed request with
converted audio, as before; a TV that refuses a split SourceBuffer falls back to muxed for the session.

Switching sources (`web/src/features/watch/seamlessSwitch.ts`): the player has two `<video>` slots, the visible one
and a hidden, muted one; every handler, the controls, the progress saver and the subtitle overlay follow the active
slot. An upgrade or sidegrade offered while the current stream plays (or is paused) is prepared in the hidden slot,
one preparation at a time:

1. `files` (re-adds the torrent; not for a TorBox source, which never goes through TorrServer), then the same
   `probe` the player runs before playing. A failed probe, `duration_mismatch`, or a length more than max(30 s, 1 %)
   off the playing file ends it silently (`switchLength.ts`, ported to the TV). Releases of one title differ a little
   (Game of Thrones S01E01: WEB-DL 3695.1 s, BluRay 3702.1 s; a 3 s limit dropped the TorBox 4K sidegrade and the
   player stepped down to 1080p). Up to 10 s apart the same time is the same moment; further apart every position of
   the preparation is mapped by the length ratio. `switchLength.test.mjs` checks the rule (`node
   --experimental-strip-types --test` in a node:22 container).
2. The new stream is requested about 8 s ahead of the visible position (20 s for a TorBox source: its `seekpoint`
   takes ~4 s and its play stream ~4.5 s to the first bytes, since every ffmpeg seek is a new CDN request; with 8 s
   the visible video always got there first), with the audio track of the current track's language (same channel
   count first, else the usual default rules) and the same `hevc`/`acodecs` caps. `seekpoint` runs next to the play
   request and gives its real start `t0` (the keyframe before the request; a keyframe too close to the visible
   position starts it again further ahead, twice at most). It is ready once `readyState` ≥ HAVE_FUTURE_DATA with 5 s
   buffered from `t0` (2 s once the browser stopped fetching on its own).
3. When the visible video presents the frame at `t0` (within 50 ms, `requestVideoFrameCallback`; a 40 ms poll where it
   is missing) the hidden one starts playing, still muted and invisible, next to it. Only once its time advances (within
   3 s; else the preparation is dropped and the visible stream is never touched) the visible one is paused, the hidden one gets the current volume, fades in over 150 ms, and becomes the active stream (hash, file, duration, audio tracks, offset, quality badge). 250 ms later the
   old element drops its stream and the old torrent is released. No toast and no loading screen; an upgrade shows a
   small "Now in 4K" note for 2 s while the controls are hidden. Subtitles are listed again for the new file.
4. If the visible stream stalls while the hidden one already holds the current moment (1.5 s of it buffered, also
   before it counts as ready), it takes over at once (after a seek inside its buffer). If the visible video got past the hidden start, the hidden one seeks forward inside its
   buffer, else it restarts further ahead (up to 3 times; the lead grows by 8 s, 20 s for TorBox, and at least to
   the time the last try ran plus 7 s). A seek that leaves the hidden start ahead
   keeps it; any other seek (or another audio track) restarts the preparation once the visible stream plays again
   (up to 4 times). Not ready within 60 s, an element error, or the end of the title near: the preparation is
   dropped and its torrent released. The reason (with the preparation's timings and restarts) is logged in the console
   (`[nextflix] upgrade to <hash> dropped: …`) and reaches the server log: a dropped upgrade is offered once more a
   minute later (15 s for TorBox) (that `upgrade` poll
   carries `failed=<reason>`, logged as `the player could not switch to the upgrade: …`), any other dropped switch
   releases its torrent with `why=<reason>` (`player: dropping <hash>: …`). After the second failure the upgrade
   is not tried again this session.

A stalled (or not yet started) player still switches the old way: PlayerCore remounts on the new source at the same
position with the loading screen and a toast ("Switched to 4K", "Switched to a faster source"); a step down always
does. PlayerCore is keyed by the resolved source (not the retry counter): keyed by the counter it mounted once with
the previous source and the session's first start position, and the new source started there (Frieren: an upgrade
offered while the visible stream was buffering went back to 0). Rewind guard (`rewindGuard.ts`): once the item
played past 30 s, a start at 0 the viewer did not ask for (hard switch, retry, recovery, a new PlayerCore) resumes
at the last position that played; the position the parent keeps is only written by a running stream, so a
PlayerCore still opening its file never reports 0. Dev builds expose `window.__nfxDebugSwitch({ hash, index,
debrid?, kind?, hard? })`, which switches like an offer would: seamless while the player plays, else (or with
`hard`) the hard switch (for browser tests).

The player has a single exit control, a Back arrow at the top left (shown with the other controls, which stay up while
paused). It does what closing always did (saves progress, releases the title and its torrents, cancels the next
episode's warm-up) and the screen the player was opened from shows again; the player is an overlay without a route
of its own, so no history entry is popped. Esc still closes it.

Phones and tablets (`features/watch/mobile.ts`, `components/TouchSeekBar.tsx`, `mobilePlayer.module.css`): on a
touch-first device (`(hover: none) and (pointer: coarse)`) the player switches to a touch layout; desktop is
unchanged. A tap shows / hides the controls (they hide after 3 s; hidden controls take no taps), Play / Pause and
±10 s are big center buttons, a double-tap on the left / right third seeks back / forward with a ripple and the
running total ("+20 s"); every further tap adds 10 s and the seek runs once 650 ms after the last tap (one stream
restart, not one per tap). The seek bar is a 44 px touch target that starts dragging only after a sideways move (a
vertical swipe never seeks); the drag shows the `SeekThumbnail` frame above the finger and the target time. Audio &
Subtitles opens as a bottom sheet (drag its handle down or tap outside to close). Subtitles are placed on the
picture and sized from its height (5.5 %, 14-40 px, times the Settings size), above the controls when they show.
No mute / volume (hardware buttons), no long-press 2× (it would read as a draining buffer to the buffer health
check). Opening the player requests fullscreen on the container (never the `<video>`: the overlay, the controls
and the hidden switch slot live next to it) and locks landscape where allowed (Chrome Android); a rotate button
toggles the lock. iPhone Safari has no element fullscreen and `webkitEnterFullscreen` would replace our player
(controls, skip intro, next episode) with the native one, so there the fixed full-viewport container is the
fullscreen (the fullscreen and rotate buttons are hidden; the address bar stays). Opened from the home screen
there is no bar, so the first player open in a Safari tab shows a one-time Add to Home Screen hint
(`hfx-home-screen-hint`). While open, the player adds `viewport-fit=cover` (safe-area insets pad the bars), stops
overscroll / pull-to-refresh (touchmove is cancelled outside `[data-scroll]` lists), disables double-tap zoom
(`touch-action`) and holds a screen Wake Lock while playing. The Wake Lock API needs a secure context, so over
plain http on the LAN only WebKit's own rule keeps the screen on: playing video that started with sound. An
element adopted from the hidden slot played muted, so on Safari it is paused and played again once unmuted. The
log notes `wakelock` on `device` and each `hidden` / `visible` change with the element's paused / muted state.

Safari (every iOS browser too) plays a URL only when the server answers byte ranges, which the live `/play` stream
cannot, so there `streamSource.ts` fetches the stream and feeds it through `ManagedMediaSource` (`MediaSource` on
macOS); other browsers get the URL as `src`. Measured on an iPhone (iOS 18.7): the element's clock and the frame on
screen agree within 0.02 s, so subtitles need nothing extra. What the feed handles:

- WebKit keeps downloading while reading waits (21 s of waiting handed over 227 MB in one read, which never fit:
  `QuotaExceededError` until the buffer ran dry). So the request carries `pace=1` and the server reads the input at
  playback speed after a 30 s burst; the feed still appends in 4 MB slices and holds at 45 s ahead (10 s while
  `ManagedMediaSource` is not `streaming`). A hold counts as the browser idling for the buffer health check
  (`fedFetching`), else the shrinking lead read as a slow source and sidegraded. Paced, growth stays near 1
  and a skip forward inside the buffer shortens the lead for good, so a fed stream only drains below 0.75
  growth, or below 1 with < 12 s buffered (critical as usual).
- iOS evicts buffered media under memory pressure, also ahead of playback. Watched data is removed first (10 s kept
  behind); if the data at the position still ends before what was appended, the stream fails at once (`network`
  error) and the player restarts at the position instead of stalling at the hole.
- A paced stream keeps coming during a pause: after 20 s paused the feed stops, and playing on restarts the stream
  at the position.
- iOS refuses `play()` on an element no tap started (the hidden slot, or a remounted player's new elements). So
  every switch (sidegrade, upgrade, step down, subtitles release) restarts the playing element on the new source
  at the position (`switchInPlace`: probe, then a restart with a short loading screen). Only a source failure still remounts the player; a refused start there shows the play
  button instead of the loading screen (`play-rejected` in the log).
- Playback log (client `web`): `device` (`fed=true`), `feed-hold`, `feed-quota`, `feed-stuck` (where the feed waits
  after 5 s without data), `feed-evicted`, `feed-paused-out`, `feed-end`, `feed-error`, and `timeline` samples for
  the first 3 minutes (element clock, frame time, stream offset, buffered range); every player also logs `switch`
  (seamless switch result and why it was dropped) and `play-rejected`.

Release from the web app: an open player marks its title / episode as playing (`holdPlaying` in `api.ts`) and
`releaseTitle` skips a playing title, so only that player releases it when it closes (or on Next). The detail page
sends `release?<title>&delay=60` when it is left (route change or `pagehide`); the player sends `release?<title>` plus `hash=<every torrent it played>` when it closes, drops a replaced
source at once (upgrade, sidegrade, step down) and releases the finished episode on Next. The next episode is
prefetched once 10 minutes are left (85 % for episodes under 25 minutes, `next_pos`/`next_dur` let the server check
it) and released when the player closes before it is reached.

Player preferences live per profile in localStorage `hfx-player:<profileId>` (written by Settings,
`features/settings/playerPrefs.ts`, and by the player for the last audio/subtitle pick): `autoplayNext: false` drops
the 5 s countdown at the end credits and the end of an episode (only the Next button stays, see
[Skip intro and Next episode](#skip-intro-and-next-episode)), `subSize` (`small`/`medium`/`large`) scales
the subtitle overlay by `SUBTITLE_SIZE_SCALE`, `audioLang: 'original'` picks the title's original language and skips
the "Turkish profile → Turkish audio" rule (picking the original track in the player keeps `'original'`),
`upscale: true` turns on [Anime upscaling](#anime-upscaling-web) for anime.

Default audio track (`features/watch/audioPick.ts`, a pure function; the TV app has the same rules in
`tv/webos/src/lib/subtitles.ts`), first match wins: the last picked language (not with `'original'`), Turkish for a
Turkish profile (idem), a track tagged with the title's original language, a track whose title says "Original" /
"orig" or names the original language ("English 5.1"), an untagged / `und` track (usually the original; one without
dub words such as "Dub" / "MVO" / "Дубляж" first, the default one first) over tracks tagged with another language,
the file's default track (`default` in the probe answer), the first track. Commentary tracks are skipped. Resumes
from Continue Watching carry no `original_language`, so both players look it up (`/api/tmdb-movie/:id`, cached) and
the probe step waits up to 1.5 s for it.

Both players draw subtitles themselves: the cue for position - `subDelay` (position = seekpoint offset + the
element's time, see [Playback timeline](#playback-timeline-playgo-seekpointgo)), the web overlay on every animation
frame, the TV overlay right after each cue boundary (`cueTiming.ts`, at least every 100 ms for seeks; a fixed 100 ms
poll showed cues 50 ms late on average). A new cue list (a poll of a partial answer) is looked up at once without
a blank frame; the delay is applied only there. The webOS app also subtracts the clock's lead (`clockLead.ts`):
webOS restarts `currentTime` at a seek's target and within ~30 ms of playing reports about 0.25 s more, then runs at
rate 1 (LG B5, webOS 25: 0.21-0.29 s over 15 seeks; pause and resume without a seek do not jump; Kodi uses -275 ms
on webOS), so cues showed that much early with picture and sound in sync. 1.5 s after each seek's `playing` the jump
is measured (a stall or pause in between drops the sample), the median of the last 7 is kept in localStorage
(`nfx-tv:clock-lead`; 0.25 s until the first) and logged as `clock` and as `clead` in `summary`. `video.
requestVideoFrameCallback` never fires on webOS and the screen capture service is denied to apps, so the displayed
frame itself cannot be checked.

Subtitles in the players (web `TorrentStreamPlayer.tsx`, TV `engine.ts`): the list of the playing file is asked
again 8 s and 20 s after the first answer while the profile's language is missing. When it is still missing after
those retries and the audio is in another language:

- The player asks `subalt` (again after its `Retry-After` while the server scans, at most 4 times; again after every
  late re-query). An offer shows as a row in the subtitle panel: "Türkçe (başka sürümde) – geçiş yap" / "English
  (another version) – switch". Nothing switches without the viewer. Choosing it switches like a sidegrade: the web
  player prepares it in the hidden slot (`seamlessSwitch.ts`, kind `subtitles`) and takes over seamlessly (a
  stalled player restarts with the toast "Switched to the version with … subtitles"); the TV restarts at the same
  position. The new file's list then picks the best subtitle of that language as the viewer's choice.
- Late uploads: every 10 minutes the list is asked again with `refresh=1` (the server skips its cached provider
  answers, at most every 5 min per title). New subtitles are added to the panel; when the profile language arrives
  and the viewer has not chosen a subtitle it is shown, with the toast "Türkçe altyazı bulundu".
- A subtitle file that fails (web player; `subtitles.ts`, `nextSubtitleAfterFailure`): after a 429 (OpenSubtitles'
  daily quota or another rate limit) the next subtitle of the same language from another provider is shown (SubDL, SubSource,
  Türkçe Altyazı, embedded, torrent; that provider is not tried again), after a 5xx another provider first, then the
  same one. A quota failure of the shown subtitle says in a toast either that another source took over or, without
  one, that no other source has the language and when the quota resets (once per kind). Failed files are remembered (a quota until its `reset_at`, a server or
  network error 5 s after the first one in a row, doubling up to 5 minutes, others 5 minutes) and not asked again on
  seeks before then; a shown subtitle that failed with such an error and has no other source is asked again when its
  wait ends (an AI translation's first answer often fails once; before, every seek logged `why=failed` for 5
  minutes). The TV does the same (`subtitleRetryMs`); they and the items the server marks `quota: true` are greyed in the panel with the hint
  "giriş yapınca hak artar" / "more after signing in", and the default pick, a remembered pick and late lists only
  take them when nothing else of that language is left. Only the chosen file is ever downloaded.

Embedded text tracks of big files are read live (`modules/subtitles/live.go`): extracting a track of an MKV / MP4
means reading the whole file, so before, such tracks were only offered for small or mostly cached files. Now they are
listed with `live: true`; the file request carries the player's position (`t`) and ffmpeg reads the track from 30 s
before it onward (`-ss … -copyts -start_at_zero`, so cues keep the file's times, counted from its start time like a
full extraction and the play stream; the pieces it reads are the ones playback needs
next, mostly from TorrServer's read-ahead cache). Cues collect in memory and each answer returns what is there (a
`NOTE nextflix-partial` line and `X-Subtitle-Partial: 1` while more follows); the players ask again every 15 s (the web player also
after a seek; a failed poll keeps the cues and asks again, it never switches to another subtitle). A position more than 5 minutes past the read's frontier (or before its start) starts a new read
there; a read stops at a range read before, after 3 minutes without requests, or when another track of the same file
is chosen (at most 3 reads server-wide). Once every second of the file was read the track is cached on disk like a
full extraction.

### Automatic subtitle timing (`modules/subtitles/sync*.go`)

Subtitles from OpenSubtitles (without a hash match), SubDL and turkcealtyazi.org are made for some release, not
necessarily the playing one: another frame rate, a title sequence or ad breaks cut differently. Seen on The Mentalist
S02E07 (Netflix WEB-DL): every Turkish subtitle was made for the HDTV / DVD releases; the one shown (turkcealtyazi
"S02 (pack) HDTV") was right in the teaser, 1.5 s early after the title sequence, 3.5 s after the first ad break and
4.2 s by the end (measured against the file's own English track). So the server times them to the playing file, like
ffsubsync (FFT cross-correlation of the subtitle with a reference signal, a few framerate ratios) and alass (offsets
that change at splits), simplified:

- Ids: `/api/subtitles` gives each such subtitle an id that also names the playing file (hash + index). Subtitles made
  for the file (torrent files, embedded tracks, OpenSubtitles hash matches) keep their ids and are never timed.
- Reference (`sync_media.go`), read once per file and kept on disk (`/app/data/subtitles/syncref`): a subtitle shipped
  in the torrent (or an embedded track / hash match already on disk) gives the whole timeline for free. Otherwise
  60 s windows, about one per 7 minutes from the opening to before the credits (4-10), are read with ffmpeg `-ss`
  (only those stretches, through TorrServer or the TorBox proxy): the cues of the file's own text subtitle track (any
  language, English first; `-copyts`, since after `-ss` subtitle times count from the keyframe before it), else speech
  from the dialogue channel (`pan=mono|c0=FC` for 5.1, else a downmix) at 8 kHz by a simple energy detector on 10 ms
  frames (`sync_vad.go`). Every window read goes through a local byte cap (1.25 × its share of the file + 16 MB):
  a subtitle read in a stretch without lines would otherwise continue to the end of the file. The reads start 20 s
  after the first request and one window at a time, and every window (also the early own-track windows) first waits
  while any stream starts, the file's own included (the quiet gate's start rule; subtitles owns the small
  `LineGate` interface, streaming implements it, `main.go` wires it; logged `warm: deferring subtitle timing read of
  …`): up to 12 windows of 60 s (~800 MB) took the line from a starting stream. The waits of one job share 2 minutes
  (after that its reads go on without waiting, so seeks cannot make it run out of time; the job limit grew by
  those 2 minutes), so timing just starts later; embedded-track and moviehash reads a player waits for never wait.
- Alignment (`sync_align.go`): the signals are line starts (a cue's start; speech after a pause), each reference start
  spread to ±0.6 s, not ffsubsync's speech on/off: many subtitles chain cues back to back, and fan subtitles start
  lines up to a second early or late. For each ratio (1, 25/23.976, 24/23.976, 25/24 and back) every offset within
  ±90 s is scored per window by FFT cross-correlation; a Viterbi pass then picks piecewise constant offsets within
  ±15 s of the best global one, each change costing 4 matched lines plus 0.5 per second it moves (a far jump needs
  more evidence) and moving at least 0.4 s (smaller steps between neighbouring windows are detector noise). Then every
  run of windows with one offset has to beat the offsets of its neighbouring runs by 4 matched lines on its own
  windows, else it takes the better neighbour's offset: the Viterbi used such runs as bridges. Seen on Avatar: Seven
  Havens S01E02 (WEB-DL subtitle on the WEB-DL file, one true offset): the played audio's speech starts 0.23 s after
  the lines of the file's own track, a step the 0.4 s rule forbids, so one live window went +11 to +14 s to get from
  one to the other and minutes of cues were served 10-14 s off. With windows of both kinds (own track and speech) the
  speech lag is fitted as well (0 to 0.4 s in 0.1 s steps), so they agree. That lag is the own track's style, not
  the detector's: on 23 audio windows of 9 files (read like the window reads, against their own tracks; stereo
  downmixes and centre channels) the detected onsets lie at the acoustic ones (the first frame 8 dB above the quiet
  before it) within 0.02 s, while the tracks' lines lead the speech by +0.03 to +0.12 s on most files and +0.35 s on
  Gilmore Girls MONOLITH and The Mentalist S02E09. So speech alone times lines to the speech; a band-pass
  (300-3400 Hz) and onset backtracking did not help (mean |lag| 0.14 s either way), nor did a constant correction
  (validated leaving each file out: 0.16 s), so neither is used. The own track's style comes in through the fitted
  lag wherever own-track windows exist: read windows, or the 2 early windows of a file above the byte budget (see
  `readEarly`; Gilmore MONOLITH replayed: 0.53 s late from the audio only, 0.12 s with 2 own windows). A change is
  placed in the subtitle pause that fits both neighbouring windows best (the longest among equals). Up to 2 rounds
  add windows between neighbours with different offsets more than 150 s apart (12 windows at most). A result fits
  when its score beats chance (the mean over all offsets) by 0.15 and the best offset away from the chosen ones by
  0.08 (matched lines per reference line), and the timed subtitle ends before the video does (+60 s) and covers 40 %
  of it.
- Live timing (`sync_live.go`, `streaming/speechtap.go`): the play ffmpeg of a stream with audio (muxed, or the
  split `track=audio` request) has a second output: the dialogue of the played audio track as 8 kHz mono PCM on fd 3
  (`pan=mono|c0=FC` for layouts with a centre, else `-ac 1`; `aresample=…:async=1:first_pts=0` trims or pads the start
  so sample 0 is the request's `t` on the file's timeline, also when a copied stream seeks to the keyframe before `t`;
  checked against tone bursts with ffmpeg 7.1 for copied / re-encoded, muxed / split streams: within 20 ms, also
  for files with `start_time` 11.4 s or delayed audio, `TestPlayTimeline`). The players show cues on the same
  timeline (see [Playback timeline](#playback-timeline-playgo-seekpointgo)). Streaming
  owns the small interface (`SpeechTap.Open(hash, index, start) SpeechSink`), subtitles implements it, `main.go`
  wires it. The tap never costs playback: a goroutine drains the pipe and hands 1 s pieces to the sink through a
  32 s queue, dropping what does not fit (the next piece's offset marks the gap); a sink that panics stops getting
  data while the pipe is still drained; a play ffmpeg that fails with the tap before writing anything runs again
  without it. A file with text subtitle tracks (never PGS / VobSub / DVB) gets a third output from the same run
  (`streaming/cuetap.go`, `CueTap.OpenCues(hash, index, start, tracks)` picks one track and returns a `CueSink`): the
  chosen track and the played audio copied into `framecrc` (packet times only, no text; `-avoid_negative_ts
  disabled`, `-flush_packets 1`). Its times count from the request's `-ss`, so `t` + time is the file time, the same
  as the own-track reads (`-copyts -start_at_zero`) and the speech tap, also for cues between the keyframe and `t`
  (exact to the ms in `TestPlayTimeline` for SRT / ASS / WebVTT in MKV and mov_text in MP4, copied / re-encoded,
  muxed / split, with `start_time` 11.4 s). The muxer writes the two streams in time order, so an audio packet at T
  means every cue before T came: the sink gets `Heard(T)` about every second (also through stretches without
  cues), and none after a dropped cue. mov_text gap samples (2 bytes) are no cues. Same safeguards as the speech
  tap (256-event queue, a panicking sink stops getting data, a failing run retries without both taps). Subtitles
  runs the same energy detector on 10 ms frames (energies kept per frame, 1 byte) and puts them
  on a 60 s grid of the file, so the stretches of several streams (a seek starts a new one; overlapping, in any
  order) merge. A grid window is finished once 90 % of it was heard and the stream moved on (speech kept as bits,
  750 bytes); an unfinished one counts from 20 s heard in one stretch. Every 30 s of new audio (and when a stream ends)
  re-fits the subtitles served for the file in the last 10 minutes (debounced 3 s, at most every 15 s per file, one
  re-fit at a time server-wide) with the read windows plus the live ones (a window of the file's own subtitle track
  beats a live one, a live one an audio window read for the same stretch). While a file's first timing still reads
  its windows, the re-fit times its subtitles from the live windows alone (one decide per subtitle at a time), so a
  file opened mid-way (another source after a stall) is not served untimed for the whole read (The Mentalist S03E01,
  2026-10-14: about 45 s with cues 7 s early). While a subtitle served in the first 5 minutes of a viewing is still
  untimed (no fit yet, or a pre-timed one not backed yet, see Serving), every 10 s of audio re-fits the file, at most
  every 5 s, and the viewing's first request re-fits at once when the file was heard before: the TV's split stream
  hears ~15× real time at first (server log: 153 s ahead after 11 s), so the 30 s / 15 s steps, not the audio, set
  when the first fit came (`sync_first_eval_test.go`, below). A decide costs 21 ms (median, p95 35 ms; with read
  windows 39 / 137 ms) on the snapshot's starts. The live windows of a file with a timed
  subtitle are kept on disk (`/app/data/subtitles/synclive`, one bit per frame). Once 85 % of the file's windows
  were heard live timing ends for the file (no more tap) and the result is final. Fits with live windows are held to
  stricter rules: on generated episodes, few or contiguous windows let another episode's subtitle pass through the
  other frame rates and the offset changes (5-15 % with 3-20 windows, also with the plain rules above), so a fit needs
  120 s of reference (180 s before: the first 1.5 minutes on screen were untimed, since the tap hears about 90 s
  ahead of the picture; with 120 s no other episode's subtitle fitted in the first minutes, 0 of 78 real pairs and 0
  of 300 generated ones, while with 60-90 s this episode's and other episodes' peaks overlapped, even with the offset
  known within 1 s from a sibling episode, so that prior is not used) and 30 reference line starts, below 8 windows
  one offset at the file's rate is fitted (from 4 windows also one offset
  change, such as a title sequence cut differently, at a peak bar of 0.20, else the single offset at 0.12), other rates
  need windows spread over 25 minutes, or contiguous ones over which they drift by 10 s (25 ↔ 24 fps: 8 minutes; a
  PAL subtitle of a 23-minute episode never fitted before), and the peak bar is 0.10 instead of 0.08 (another
  episode's subtitle with 3-41 contiguous windows: 1 of 480 cases fits, at 20 windows, with the plain rules 46 of 480;
  with 4-7 windows and the one change: 0 of 632, while 73 % of the true changes are found; with 8-20 contiguous
  windows and the other rates: 0 of 60). An offset change costs as much as with read windows: the cost used to grow
  with the windows (4 × windows / 12 lines), which dropped a real teaser offset once most of an episode was heard
  (Mentalist S02E10 KONTRAST and a Gilmore Girls file, 2 s off before the title sequence). Shaped like S02E10
  (+0.66 s in the teaser, +3.00 s after the title sequence at ~214 s, a minute of audio at a time), the change is found
  at the 5th window. A later re-fit of a fitting subtitle keeps its frame rate (fewer FFTs).
  Hysteresis: once a provisional fit was accepted, a re-fit that does not fit keeps it served (no alternatives tried;
  logged `kept the last accepted fit (new fit poor: …)`) until a new fit is accepted or the file is covered. On
  S02E10 the fit from 3 windows (one offset) turned poor at 6 windows as they passed the title sequence, so the
  subtitle was served untimed for two minutes before two offsets fitted at 8. A re-fit that fits replaces the served
  fit only when its offsets match 4 lines more on the current windows than the served ones do (`keepStable`), else
  the cues moved back and forth by fractions of a second between re-fits (the players logged `moved=` on most polls).
- Own-track cue tap (`sync_owncues.go`, streaming side wired in `main.go`): the play ffmpeg also passes the cue times
  of one text track of the played file (it demuxes them anyway; no extra reads). Subtitles picks the track like the
  window reads (the probed one, else English, not forced, not SDH) through `OpenOwnCues(hash, index, start, tracks)`
  (no tap when live timing is off, the file's reference is a whole subtitle, or the file is covered); `Cue(start,
  end)` marks a cue, `Heard(t)` says every cue before `t` was passed. Frames are kept like the speech (1 byte per
  10 ms frame while unfinished: read, cue shows, cue starts) on the same 60 s grid, so overlapping streams in any
  order merge; a window is finished once 98 % of it was read (bits for "shows" plus the start frames, in
  `synclive` next to the speech windows), an unfinished one counts from 20 s read in one stretch, and only windows
  with 4 line starts are used. They are `subs` windows: they beat a live speech window of the same stretch, count
  towards covering the file, and 30 s of newly read track trigger the same debounced re-fit. While a tap runs,
  `readEarly` reads nothing.
- Line refinement (`sync_refine.go`): fan subtitles start single lines up to a second early or late (The Mentalist
  S02E18's HDTV pack around 12:00: four lines 1.0 s early, then 0.5 s early, then 0.6-0.75 s late), and another
  cut's subtitle has stretches of a minute 2-3 s off (S02E17's HDTV pack: +2 s around 2:00, −3 s around 5:30) that
  the fit's segments do not follow. Wherever own-track windows (read and live, also overlapping ones) show a cue's
  surroundings, the cue is moved onto the own lines in two steps. Local offset: on the subtitle's own clock (before
  the fit's offsets, so lines the fit put on the wrong side of an offset change move with their stretch) the 12
  neighbours on each side (within 60 s) are shifted to where most of them start within 0.3 s of an own start (±1 s
  around the fit, else ±4 s), only when that beats every shift 0.5 s away by 2 lines and 60 % of them then lie
  within 0.4 s of one; both sides agreeing (0.2 s) give the offset, else the side that puts the cue itself nearer
  to an own start (within 0.7 s; two large offsets that disagree: none). Then the cue moves onto the own start
  nearest to it within 0.7 s when the next one is 0.4 s further, the own track shows that stretch and no
  neighbour is nearer to it. Moves under 0.15 s are not made; a move that would swap two cues or leave one
  shorter than 0.7 s is undone; a cue chained to the next one (gap under 0.1 s) ends where that one now starts,
  others do not reach into the next one. A fit that changes nothing is served refined when a cue moved. Lines
  outside own-track windows stay as the fit serves them.
- Serving (`sync.go`): the file request answers the subtitle as it is at once (marked partial, `NOTE
  nextflix-partial`, so the players ask again every 15 s, every 5 s while it is `pending` in the first 3 minutes after
  they put it on; a timing ready within 2.5 s is answered directly) and
  times it in the background (5 min at most, 2 files read at a time). The result is cached per (subtitle, file) on
  disk (`/app/data/subtitles/synced`); later requests and the players' next poll get the timed cues. While live
  windows can still change it (live timing on, file not covered) a result is provisional: `X-Subtitle-Sync: live`
  (`pending` while nothing fits), partial while the file plays (a tap open or closed within 90 s, or the first 30 s
  of a viewing session, since the tap may open after the first subtitle request), `no-store` and unmarked otherwise.
  Cues past the covered range take the offset of the nearest covered segment. Final results answer `synced` (or
  `unchanged`). One that does not fit is replaced by the best fitting other subtitle of its language listed for the
  file (up to 3 tried, OpenSubtitles skipped while its quota is used up; `replaced`, re-checked on every re-fit while
  provisional), else served as it is (`poor`). While provisional, a subtitle that missed only the peak bar but
  already sits at its best offset (one offset within 0.5 s, gain over chance met) is not replaced, only served as it
  is: a few minutes cannot tell whether another release's offset holds for the whole file. Seen on The Mentalist
  S02E16 (a stall moved playback to a ViSUM AMZN WEB-DL 37 minutes in): the Amazon subtitle (within 0.3 s by the
  file's own English track) was replaced after 3 live windows by an HDTV pack subtitle timed -5.3 s from them,
  which was 2-5 s off before minute 25 and swapped the lines mid-scene. A failed read is not tried again for 10 minutes (`failed`), but live
  windows still time it (the answer is `pending` while the file plays). Every answer carries `X-Subtitle-Sync`
  (`original` for subtitles that are never timed); see [API.md](API.md#subtitles-modulessubtitles).
- Next episode (`pretime`, `?pre=1`): the players warm the next episode 10 minutes before the end; once that resolve
  has a source (`resolve?cached=1`, asked every 20 s for 3 minutes) they list its subtitles and ask the one their
  default pick would show with `pre=1`. The server starts its timing like a first request (reference read, decide),
  but notes nothing as served (no live re-fits, no first-answer grace); the reads wait in the LineGate like every
  timing read, and a file that plays from a swarm is read only while the line is known and the playing streams need
  at most half of it (`ReadRoom`, streaming's `RoomToRead`; TorBox files always). A fit made before any of the file's
  audio was heard is marked pre-timed (`pre`): when the episode opens it is answered `pending` (the subtitle as it
  is) until the first re-fit with played audio keeps or replaces it, a few seconds into the episode. Read windows
  alone can miss a teaser's own offset: on the snapshot's starts the read-only fit put 20 of 31 subtitles' first 5
  minutes partly off (the subf2m English S02 pack: −3.3 s where the teaser needs −1.2 s), while with the played
  audio of the first minutes most were right (median from 140 s heard). Viewing model (`TestSnapshotFirstFit`, 35
  real starts from 0 on the TV, split stream on TorBox): cues of the first 5 minutes within 0.5 s 96 % today, 98 %
  with the faster untimed re-fits and polls, 98 % pre-timed (96 % if a pre-timed fit were shown at once); the time
  until the TV shows a timing right for 90 % of them, p90 58 s → 50 s → 28 s. Pre-timing helps most where the played
  audio alone fits late: the Turkish subtitles of The Mentalist S03E01 / S03E02 need 440 s of audio for their first
  live fit (~80 s on the TV), so their first minutes went from 84-89 % to 98-100 % within 0.5 s.
- Same cut (`sameCut`): a subtitle first asked for a file that it was served for on another file within the last
  minutes (a step down or sidegrade during playback) takes that file's fit at once (provisional, `same_cut` in the
  result, logged `the fit on <hash8>:<index> (same cut: …)`) when both probed lengths agree within 0.05 s and the
  frame rates match; the new file's own windows take over through the re-fits (hysteresis as above). Gilmore Girls
  S01E07 (2676.266 s both) was two minutes 2.7 s off after a step down. Equal lengths alone do not make one cut (The
  Mentalist S02E11 and S02E15 NF are both 2551.616 s, Gilmore Girls S01E06 / S01E08 0.043 s apart), but one subtitle
  names one episode, and such pairs in the snapshot match at lag 0-30 ms on every common live window (3 pairs, 14
  windows). The live windows themselves are not shared: one wrong match would make a wrong result final.
- Cost: the windows of one file are planned within 800 MB (each counted as its share of the file + 28 MB for the TorBox
  proxy's first chunk and read-ahead) and need at least 8 windows, so files above about 10 Mbit/s (1080p WEB-DLs of
  The Mentalist S02E10 at 13.6 Mbit/s, every 4K file) read none and are timed from the played audio (the first
  provisional timing after 2 minutes of audio with 30 line starts, i.e. about 30 s into playback). One exception:
  when the first play stream of such a file opens (`readEarly`, once per file), the cues of its own text track (if it
  has one) in the 2 minutes from the play position are read, after the same 20 s delay and through the same byte
  cap: the player fetches those bytes anyway, so they come from TorrServer's cache, and exact lines fit sooner and
  better than the speech heard so far. Such a reference is fitted by the live rules (they are contiguous). The S02E07 file
  (7 Mbit/s, 2.26 GB) gets 10 windows: about 550-600 MB read once per file, shared by every subtitle of it; the CPU part
  (decoding 60 s of audio, the FFTs) takes well under a second per window. The server logs the bytes read per
  reference. The tap reads nothing extra; it decodes the played audio track once more when it is copied (estimated
  1-2 % of a core for E-AC-3 5.1, not measured on the server), resamples it to 8 kHz mono (16 KB/s through the pipe)
  and the energy step costs well under a millisecond of Go per second of audio. Memory: 6 KB per unfinished window,
  750 bytes per finished one, 16 files kept. A re-fit takes about 0.1 s per subtitle and 25 windows at one frame rate
  (measured in the tests; all 7 rates and a 2 h film: a few seconds).
- Logs: `subtitles: live audio of <hash8>:<index> from <t>s: <s> heard, <s> missing; <s> of the file covered, <n>
  windows finished` when a play stream ends (`own track of …: <n> cues, <s> read; <n> own windows finished` for
  the cue tap), and `subtitles: timing of … (provisional) (… offsets …, score …; live
  reference, <n> windows (<n> live, <s> played) …)` whenever a re-fit changes what is served (state, offsets by
  0.05 s, splits by 1 s); `timing reference of …: … no read, timed from the played audio` for files above the budget.
  When what is served changes and the player sent its position (`t`, within 2 minutes), `timing of … moved <n> of
  <m> cues within 300 s of the viewer at <t>s: max ±…s, median ±…s` says how the cues just ahead of the viewer
  moved (matched by text, nearest start; lines of another subtitle counted apart), so slips near the viewer show
  without the players' logs. Pre-timing logs `pre-timing … (next episode)` or `… skipped: the line has no room …`.
- Ranking: later lists show a timed fitting subtitle as `synced` (after the ones made for the file) and move ones
  that do not fit down; the file's probed frame rate is the ranking's fps hint. A subtitle made for the playing
  release (same group, or same streaming service and source family; "WEBRip.Amazon" counts as AMZN) goes ahead of
  the timed ones unless its own timing found it does not fit: on The Mentalist S02E10 (AMZN WEB-DL) the HDTV pack
  was 3 s late after the title sequence, while subf2m's "S02.WEBRip.Amazon" pack is named for that release. It is
  still timed like every provider subtitle (only hash matches are served as they are), and needed it: +1.24 s, then
  +3.26 s after the title sequence.
- The viewer's manual offset (`subDelay`) still applies on top.
- Holes (`holes.go`, `holes_fill.go`, see [API.md](API.md#holes)): once timed, a served subtitle is compared on the
  file's timeline with a reference of the file in another language (its own text track or a hash match on disk, else
  a subtitle timed to fit). A pause of 15 s or more holding 4 or more reference dialogue lines is a hole, unless the
  own-track windows or silent heard audio contradict it. Holes demote the subtitle behind complete ones of its
  language; with the AI translation on, the reference's lines in them are translated (ai_job.go, the subtitle's lines
  around as context) and merged in the reference's timing without touching the subtitle's cues. Measured on every
  two subtitles of one episode in the snapshot and the server's cache (one timed onto the other by the aligner, 49
  pairs both ways, Avatar, The Mentalist, Gilmore Girls, Rurouni Kenshin, a film): 11 holes, all real (Avatar
  S01E02's two scenes, 25 and 15 lines, against GRACE and the SDH subtitle; a 4-line exchange; an anime ending song
  and next-episode preview left out; 5 shouted lines), none in a pause of both (Avatar's 175-216, 1071-1110 and
  1139-1171 s are music and action in both). Speech detection is weak evidence: 68-79 % of the frames of those
  pauses count as speech, so it only vetoes a hole in silent audio.

Measured on S02E07 (text-matched against the file's English track): the HDTV subtitle had a median error of 3.3 s
(10 % of lines within 1 s); timed from 10 windows of the file's own track: 0.41 s (83 %; 89 % is the best any timing
of this subtitle reaches, its own line timing is loose), from speech detection alone: 0.37 s (83 %). The S02E06
subtitle (another episode) is rejected.

Evaluation on real data (`sync_eval_test.go`, run with `NFX_TESTDATA` pointing at a copy of `/app/data`; skipped
otherwise): the truth is the file's own text track (per read window, the subtitle's offset by its line starts and
ends, independent of the aligner); the played audio (synclive) is replayed a minute at a time through the real
re-fit path (`decide`), and what the viewer watches (the minute 90-150 s behind the last one heard) is scored. On a
snapshot of 21 files (The Mentalist, Avatar: Seven Havens, Gilmore Girls), 13 real pairs with read and live windows:
cues within 0.3 s while watching 85 % → 100 %, re-fits serving a segment more than 1 s off 2.9 % → 0 %, served
changes 58 → 16; Avatar S01E02 alone: false segments 8 → 0 of 104 re-fits. 28 distortions of the Avatar subtitles with
known truth (offsets up to ±60 s, PAL and 24 fps rates, title-sequence and ad-break cuts, jitter, early lines, 30 %
dropped, merged and extra cues) on its real windows: false segments 17 % → 2.5 % of re-fits, within 0.3 s while
watching 68 % → 95 %; from the audio alone a PAL subtitle now fits after 8 minutes (never before). Other episodes'
subtitles timed with the read windows: 32 → 6 of 190 fitted (5 of the 6 are this episode's after all: another
release, which the own-track check missed), live replays 1 → 0 of 43 (18 → 0 re-fits). On The Mentalist S02E10 AMZN
the subf2m "S02.WEBRip.Amazon" subtitle still needs +1.24 s, +3.26 s after the title sequence (from the audio).
The first minutes (`sync_early_eval_test.go`: the picture 90 s behind the audio, a re-fit every 30 s): The Mentalist
S02E10 AMZN (no read, truth from its converged fit) first timed after 120 s of audio instead of 180 s, cues of
minutes 0-5 within 0.3 s 71 % → 92 %; Avatar S01E02 and its 30 distortions without a read: with the 2 early
own-track windows 66 % → 81 % (median error 0.29 → 0.00 s), first right minute 2.3 → 1.4.
The start of a viewing (`sync_first_eval_test.go`, `NFX_PAIRS`: the 83 (subtitle, file) pairs the server served;
the 35 played from 0 with a fit over the whole file as truth): re-fits every 10 s of audio instead of 30 s accept
the same first fits about 10 s of audio earlier and no other episode's subtitle (0 of 27 pairs in 1620 re-fits up to
10 minutes); the viewing model with the read, the TV's tap rate and the poll and re-fit steps gives the numbers under
Next episode above. Before these changes 37 of 66 TV viewings opened untimed, 24 for 20 s or more (median 65 s, on
the code before the live fit during the first read); the first timing then moved the cues by 1.6 s (median).
Line refinement (`sync_refine_eval_test.go`, signed per-line errors against the own track, early and late apart):
with the read own-track windows, on 24 loose pairs (Mentalist, Gilmore Girls, others) lines in those windows within
0.15 s 56 % → 76 % (early beyond 0.15 s 21 % → 11 %, late 24 % → 13 %, p10/p90 −0.33/+0.42 → −0.19/+0.26 s, none
moved 0.15 s further from its line); already right subtitles 94 % → 97 %, none worse. With the whole own track
from the cue tap (`TestSnapshotRefineFull`: S02E17 and S02E18 own tracks read once for the test, times only,
every line of the episode): S02E18's HDTV pack 44 % within 0.15 s as served today → 77 % (|median| 0.18 →
0.07 s, early 19 % → 12 %, late 36 % → 11 %), S02E17's 26 % → 51 % (0.34 → 0.14 s), the two subf2m English
subtitles 27 % → 49 % and 27 % → 47 %; all four 32 % → 57 %, within 0.5 s 66 % → 72 %. Against the fit with the
tap's windows the refinement moves 24 % of the lines, 23 % closer, 1 % further; 8 % of the lines end further off
than served today, from the fit itself (with own windows everywhere its segments change: 10 % better, 10 %
worse). Lines without clear evidence keep the fit's place (S02E17: p10/p90 −0.94/+0.88 s remain, stretches
whose lines are a second apart from each other). No other episode's subtitle fitted with the tap's reference (87
tried). Lines outside own-track windows (an own window held out in turn, 3574 lines) stay as they were: 41 %
within 0.15 s, p10/p90 −0.73/+0.87 s. Tried and rejected: snapping lines onto speech onsets of the played audio
(only 30-40 % of own line starts have an onset within 0.3 s; held-out lines 1 % better, 3 % worse, already right
subtitles 4 % worse); carrying the windows' offsets to the stretches between them (held out: 2 % better, 1 %
worse, S02E17 5 % worse); one offset per 60 s window (±1 s; the two HDTV packs with the whole track
36 % → 53 % instead of 65 %: the 2-3 s stretches and the lines the fit put on the wrong side of an offset change
stay); a ±4 s search alone (read windows 72 % instead of 76 %: shifts a line spacing away compete, so ±4 s is
tried only when ±1 s finds nothing); a 0.5 s gate (1 % worse against the fit). Other episodes' subtitles
fitted at the file's rate to the files with own-track windows (692 pairs, mostly not accepted, which are never
refined): 1.6 % of their cues move, against 2.9 % of this episode's subtitles' at that rate.
`sync_robust_test.go` keeps the Avatar case as a compact fixture (`testdata/sync/avatar_s01e02.json`: cue times and
speech bits, no text).

`torrserver_tuning.go` raises TorrServer's settings once per start, only writing when a value changes (a write drops
TorrServer's torrents): `CacheSize` ≥ 2 GB RAM (read-ahead 95 % ≈ 1.9 GB, minutes of a 4K stream),
`ReaderReadAHead` ≥ 95, `ConnectionsLimit` ≥ 200, `TorrentDisconnectTimeout` = 45 s, `PeersListenPort` = 41413,
and DHT / PEX / UPnP / upload on and `ResponsiveMode` on (only switches this TorrServer knows are written).
`UseDisk` and `PreloadCache` (only used by TorrServer's own preload) are left alone. How far ahead TorrServer
(MatriX.145) downloads: a reader's range is `CacheSize / readers × ReaderReadAHead %` (≈ 1.9 GB) ahead of its
position, and it prioritizes up to `ConnectionsLimit / readers` missing pieces of that range at a time (Now / Next /
16 MB Readahead / High / Normal, re-armed every 5 s), so it keeps downloading until the whole range is in, then at
the reader's pace. There is no per-torrent priority API; the playing file gets the connection by dropping the others
(playback priority). Measured on Night of the Living Dead 1968 (1080p, 10.6 GB, 2 MB pieces, 1.83 MB/s needed), a
realtime-paced read (1.9 MB/s) from 70 % of the file with `ConnectionsLimit` 100: 25-30 MB/s with 28-33 active
peers; buffered ahead 22 s of media after 10 s, 51 s after 20 s, 140 s after 30 s, 6.5 min after 40 s, the whole
1.9 GB range (≈ 18 min) after 90 s, then at the reader's pace. With a second torrent read at full speed next to it
the playing one still got 20-24 MB/s (19 s ahead after 11 s, 72 s after 21 s), because that swarm was strong; with a
weak playing swarm (3-5 active peers) the other torrents take the line, hence the playback priority.

## Anime upscaling (web)

The web player can run [Anime4K](https://github.com/bloc97/Anime4K) on anime: a switch under "Picture" in the Audio &
Subtitles panel, shown only for anime and donghua (TMDB Animation genre 16 and original language `ja`, `zh` or `cn`,
`isAnimeTitle` in `features/watch/api.ts`; wider than the catalog's Japanese-only Anime filter). It is off by default and remembered per profile
(`upscale` in the player prefs). The TV app has nothing like it: webOS plays video on a hardware plane the page can't
read, and the TV upscales on its own.

- `features/watch/upscale.ts` draws the active `<video>` slot through the shaders into a canvas over the slots (under
  the subtitle overlay) on every new frame (`requestVideoFrameCallback`). It uses `anime4k.js` (WebGL port of the
  v4.0.1 GLSL shaders, pinned). WebGPU ports exist but WebGPU needs a secure context, and the LAN app runs on plain
  http.
- The mode is picked from the video on its first frame and again when the source changes (`pickMode`, Anime4K's
  advice): lines = max(height, width x 9/16); 900+ lines Mode A (most 1080p), 600-899 Mode B (most 720p, softer
  restore), below 600 Mode C (a modern SD release is a downscaled HD master) or Mode A for titles before 2009 (real
  SD masters). The panel hint shows it ("auto mode B for this 720p video"), the wipe label too ("Anime4K B").
- Chains (`anime4kShaders.ts`, all after Clamp_Highlights), in CNN size M (S after a step down): A = Restore_CNN +
  Upscale_CNN_x2, B = Restore_CNN_Soft + Upscale_CNN_x2, C = Upscale_Denoise_CNN_x2. Their AutoDownscalePre and
  second upscale are left out: the output is the picture's on-screen size in device pixels, at most 2x the video,
  so they could never run. The x2 pass only runs when the output is over 1.2x the video (720p on a 1080p screen,
  1080p on 4K); 1080p on a 1080p screen only gets the restore pass.
- "Extra enhancement" (a second switch, shown while upscaling is on; `upscaleExtra` in the prefs) runs the
  secondary mode in HQ sizes: A+A = Restore_CNN_VL, Upscale_CNN_x2_VL, Restore_CNN_M, AutoDownscalePre_x2/x4,
  Upscale_CNN_x2_M; B+B = Restore_CNN_Soft_VL, Upscale_CNN_x2_VL, AutoDownscalePre, Restore_CNN_Soft_M,
  Upscale_CNN_x2_M; C+A = Upscale_Denoise_CNN_x2_VL, AutoDownscalePre, Restore_CNN_M, Upscale_CNN_x2_M. Anime4K
  says secondary modes are for x2 or more, so they only run when the screen (not the window, so fullscreen
  doesn't rebuild) is at least 1.9x the video; otherwise the primary mode runs and the switch says why. Toggling it
  restarts the upscaler (and the wipe below).
- Watchdog: if more than 25 % of 240 presented frames were skipped by the loop, it steps down extra -> M -> S
  (leaving extra shows "Extra enhancement turned off" on that switch), then it stops with a notice ("the graphics
  card could not keep up"). Frames are not judged for 2.5 s after the first
  frame of freshly built shaders (anime4k.js compiles them synchronously and ANGLE finishes the big ones on the
  first draws), nor across a gap over 500 ms, a hidden tab, a pause or a seek. It also stops on missing WebGL
  extensions, a lost context, a cross-origin frame or an empty first frame, and the plain video shows again.
- When the first upscaled frame is up, the canvas wipes in from the left behind a glowing line
  (`components/upscale.module.css`, 4.5 s): it reaches the middle in 1.5 s and holds there until 3 s with
  "Anime4K <mode>" on its left and "Original" on its right, so both pictures can be compared, then moves out to the
  right.
- Diagnostics go to the playback log (`features/watch/upscaleLog.ts`, client `web-a4k`): `a4k-gpu` (GPU name,
  backend, dpr), `a4k-build` (`mode`, `tier` X/M/S, `lines`, `extra_idle`, build ms), `a4k-first` (`chain` such as
  `B:M`, first-frame ms, sizes), `a4k-window` (every 240 frames: `skipped`, `draw_avg` / `draw_max` ms),
  `a4k-level`, `a4k-stop` (`why`, `at`, `err`).
- The `<video>` slots load with `crossOrigin="anonymous"` so WebGL can read their frames; the API's CORS already
  allows every origin `getApiUrl()` talks to directly.
- The shaders are their own lazy chunk (`features/watch/anime4kShaders.ts`, kept out of the `vendors`
  chunk in `next.config.js`), loaded only when upscaling starts.
- Drawing through a canvas bypasses the browser's own video upscalers (NVIDIA RTX Video Super Resolution, Edge
  Video Super Resolution) and HDR output, one reason it stays opt-in.

## Skip intro and Next episode

`server/internal/modules/segments` (`GET /api/skip-segments`, see [API.md](API.md#skip-segments-modulessegments))
tells both players where an episode's intro and end credits are. Sources, first match per segment:

1. Named chapters of the playing file: the probe runs ffprobe with `-show_chapters` (free: chapters are in the
   header), `streaming.Service.Chapters` hands them over through the consumer-owned `segments.ChapterSource`
   (`fileChapters` in `main.go`). Only whole names count (`Intro`, `Opening`, `OP - <song>`, `Opening Credits`,
   `Credits`, `End Credits`, `Ending`, `ED`, `Outro`, a few Turkish / German / French / Spanish / Italian ones);
   generic chapters (`Chapter 3`, `Opening Scene`) are ignored.
2. Community timestamps: [TheIntroDB](https://theintrodb.org/docs) (`/v3/media?tmdb_id=&season=&episode=&duration_ms=`,
   TMDB id of the show, the file length picks the release version; 500 reads a day per IP without a key), then
   [IntroDB](https://introdb.app/docs) (`/segments?imdb_id=&season=&episode=`, IMDb id via TMDB external ids) for
   what the first one lacks. The answer is stored per episode and file length (±5 s) in `skip_segments`: found 30
   days, not found (or a segment missing because a database failed) 3 days; failures that found nothing are not
   stored and pause lookups for 10 min (an hour after a 429).

Audio fingerprinting (Jellyfin Intro Skipper style: chromaprint of several episodes of a season, black frames for
credits) was left out: it needs the decoded audio of many episodes, i.e. gigabytes read from swarms while one plays.

Rules (`rules.go`) keep wrong data (another release version, a bad submission) from putting a prompt in the middle
of an episode: an intro lasts 5 s to 4 min and ends in the first half; credits start in the second half, at most
15 min before the end and not in the last 5 s, and only with a known file length.

Players (`web/src/features/watch/skipSegments.ts`, `tv/webos/src/player/skipSegments.ts`, series only, fetched once
per episode and file):

- Intro: a "Skip intro" button while the position is inside it; it seeks to its end. On the TV it takes the focus
  when it appears (OK skips, Left / Right still seek) and gives it back to the timeline when it goes.
- Credits: while they play (and a next episode exists), a corner card counts down 5 s ("Next episode" fills up,
  "Watch credits" cancels) while the video goes on; the countdown waits while paused. The TV focuses "Next episode".
  With `autoplayNext: false`, or after "Watch credits", only a Next episode button shows there.
- Without credits data nothing appears before the end; the old "last 40-120 s" Next button is gone. The `ended`
  event (the real end) always starts the full-screen 5 s countdown, also after "Watch credits".

## Pause screen

Netflix's "You're watching" screen in the web, phone and TV players (`web/src/features/watch/components/PauseScreen.tsx`,
`tv/webos/src/player/PauseScreen.tsx`). When the viewer paused and gives no input for 6 s, the controls fade out and
the picture dims under the title, `S1:E3 "Episode name"` (series) or year and runtime (films), the overview, and
"Paused" in the corner. It does not come while buffering, during a start hold, the countdown, the Audio & Subtitles
panel, a seek preview or a Skip intro / Next episode button.

- Text: the episode's overview from the season endpoint (the show's overview when TMDB has none), the film's from
  `/api/tmdb-movie/:id`, in the profile language; cached per title / episode and language (`getPauseInfo` in
  `watch/api.ts`, `pauseInfo` in `tv/webos/src/lib/catalog.ts`).
- Web: mouse movement, a key, a wheel or a tap brings the controls back; the screen takes no clicks, so a click on it
  plays like a click on the video. TV: the first key only brings the controls back, except the media keys (Play,
  Pause, Stop, Fast forward, Rewind), which act at once.

## Ratings and Favorites

`server/internal/modules/ratings` stores one row per profile and title in `title_ratings` (unique
`(profile_id, media_type, tmdb_id)`): `rating` (`dislike`, `like`, `love` or NULL), `favorite`, the display data
(`title`, `poster_path`, `backdrop_path`) and `favorited_at` (orders the Favorites list). `PUT /api/ratings` is a
partial update in a transaction; a row with no rating and `favorite = false` is deleted. Every change (and profile
deletion) calls the `OnChange` hooks with the profile id.

My List migration (`MigrateMyList`, at startup): unless `rating_migrations` already has `mylist_to_favorites_v1`,
every `my_list_items` row whose profile still exists is mapped back from the web's `media_id = "9" + tmdbId`
convention (`927205` → TMDB `27205`; `media_type` `movie`/empty → movie, `tv` → tv) and upserted as
`favorite = true` with `favorited_at`, `created_at` and `updated_at` = `added_at`. Rows without the `9` prefix or
with another media type are counted as unmapped, rows of deleted profiles as orphaned; duplicates collapse. The
marker is written in the same transaction, so a favorite removed later never comes back. `my_list_items` is never
modified or dropped. Migrated rows have no title or artwork; `Backfill` fills empty `title`/`poster_path`/
`backdrop_path` from TMDB details (up to 500 rows per start) without touching `updated_at`.

## Recommendations

`server/internal/modules/recommendations` builds `GET /api/recommendations` from the profile's ratings and TMDB:

1. Signal weight per title: `love` +3, `like` +1.5, `dislike` −3, favorite +2 on top of the rating (love + favorite
   = 5, favorite alone = 2). Recency factor `0.5 + 0.5·e^(−days/120)` (1.0 today, 0.68 after 120 days).
2. Seeds: the 8 strongest positive titles (weight × recency). Dislikes: the 5 most recent `dislike` titles.
3. For every seed and dislike, TMDB `recommendations` and `similar` (page 1 each, through the shared client's 24 h
   cache) are fetched, 6 at a time. Each listed title gets `seed weight × list weight × 1/(1 + 0.08·position)`,
   list weight 1.0 for recommendations and 0.5 for similar. Positive seeds add to its support, dislikes to its
   penalty.
   When `SetSimilar` is wired (`main.go`, `similarTitles`), a positive seed uses its [More Like This](#more-like-this)
   list (unpersonalized, as the recommendations list, similar empty) when it is ready within 6 s; otherwise that seed
   uses TMDB's lists, the matcher keeps computing in the background and the rows are cached for 2 minutes only.
   Dislikes always use TMDB's lists.
4. Genre affinity: genres of the 40 most recently rated titles (TMDB details) are summed by signal weight and
   scaled to [−1, 1]; a candidate gets `1.5 × mean affinity of its genres`.
5. Prior: `0.6 × vote_average/10 × min(1, vote_count/200) + 0.1 × log10(1 + popularity)`.
6. Score = support − penalty + genre term + prior.
7. Dropped: titles the profile rated or favorited (disliked ones included), adult titles, titles without a poster or
   with fewer than 10 TMDB votes, and titles whose dislike penalty is at least as large as their positive support
   (weakly supported titles that look like something the profile disliked).
8. Rows: `Top Picks for You` = best 20 by score. Then the seeds in order give up to 3 `Because you loved/liked
   <Title>` rows (`Because you added <Title> to Favorites` for a favorite without a rating): that seed's
   candidates ordered by its own support plus the genre/prior terms, titles not already shown in an earlier row
   first (up to 20); shown ones are reused only to reach the minimum of 8. Seeds with fewer than 8 candidates get
   no row.
9. No positive signal (or nothing left after filtering): one `Trending Now` row (`/trending/all/week`, rated titles
   removed, reason `popular`).

Results are cached in memory per profile for 30 minutes (empty results are not cached). After that the old rows are
answered at once for up to 24 h while new ones are computed in the background (one at a time per profile and language).
`ratings.OnChange` invalidates the profile: its rows are dropped, so the next request waits for new ones; a computation
that started before the change is neither cached nor shared with later requests. Concurrent requests of one profile
share one computation.

## Surprise me

`GET /api/recommendations/surprise` answers one title per press. It must not keep circling one loved title, so every
press first draws one of three buckets and then a source inside it:

| Bucket | Share | What it picks | `reason.kind` (client text) |
|---|---|---|---|
| `taste` (taste blend) | 50 % | the related titles of one positive title, or a pair of the profile's top genres | `because_loved` / `because_liked` / `favorite_like` / `because_watched` ("Because you loved X"), `genre` ("Because you like drama and crime") |
| `adjacent` (adjacent discovery) | 25 % | a liked genre, but well-rated titles from other eras that are in no seed's related lists | `adjacent` ("A new pick in sci-fi" / "Bilim kurguda yeni bir keşif") |
| `wild` (wild card) | 25 % | acclaimed titles outside the taste: other languages, genres never watched, classics | `wild_card` ("Something completely different", plus "– a highly rated Korean film" / "– a highly rated anime" when `language` is set) |

The pool (`surprisepool.go`), cached 30 min per profile and language and dropped by `Invalidate` (any rating change):

1. Taste titles: ratings and watch history become weighted titles: `love` 3, `like` 2, favorite without a rating 1.2
   (+0.5 on top of love / like), finished title 1.0, started title 0.4 (a rated title ignores its history),
   `dislike` −3; all times the recency factor of [Recommendations](#recommendations). History comes from
   `progress.Service.History` through `recommendations.SetHistory` (adapter `watchHistory` in `main.go`).
2. Genres: TV genres count as their movie counterpart (Action & Adventure → Action, Sci-Fi & Fantasy → Sci-Fi,
   War & Politics → War, Kids → Family). The affinity sums the weights of the 40 most recent taste titles per genre,
   scaled to [−1, 1]. The strength of a genre is `ln(1 + Σ)` over all positive titles carrying it, each title adding
   at most 1 (love / like 1, favorite 0.6, finished 0.5, started 0.2), so several liked titles outweigh one loved
   title. Only genres with a positive affinity have a strength.
3. Taste groups: one per positive title (the 20 strongest; TMDB recommendations and similar titles, weight = the
   title's weight) and one per pair of the 3 strongest genres (TMDB discover, popular and well-rated movies plus popular
   TV; weight 2 × the pair's mean strength / the top strength). Taste titles need a 6.5 average.
4. Adjacent groups: the 4 strongest genres, each through discover sorted by rating: movies up to 1999 and from 2000
   (≥ 300 votes) and TV (≥ 200 votes); titles need a 7.0 average and must not be in any positive or disliked title's
   related lists. Weight = strength / top strength.
5. Wild groups (weight 1 each): acclaimed movies (≥ 500 votes) in Korean, Japanese (no animation), Spanish, French,
   Italian, Turkish, Hindi and German, plus TV for Korean, Spanish and Turkish; anime (Japanese animation, movies and
   TV); 4 random genres the profile never watched (horror is never used; by vote count); classics before 1975. A wild
   title needs a 7.2 average, 500 votes (TV 300), no genre with a negative affinity, no place in a related list, and
   something new: a language other than English, a main genre the profile never watched, or a year before 1975.
6. Everywhere: rated / favorited, adult and artless titles, titles with fewer than 50 votes, titles whose genres
   average below −0.35 affinity and titles the 5 most recent dislikes point to more than the liked titles are dropped.
   A profile without positive titles gets a trending group (`trending_pick`) next to the wild cards.

A press (`surprise.go`, RNG injectable for tests):

1. Removes titles with any watch progress (read fresh), the client's `exclude` list and the profile's last 30 picks
   (server memory). When nothing is left the memory starts over.
2. Draws the bucket 50 / 25 / 25. After an adjacent or wild pick the same bucket is not drawn, after a taste pick taste
   counts half; that keeps the long-run shares at exactly 50 / 25 / 25 without forcing a strict taste / other rhythm.
3. Draws a group of that bucket by weight. The group of the last pick is skipped (never the same seed twice in a row)
   and a group used in the last 4 picks counts 1/5 (seeds rotate). A title whose main genre was one of the last pick's
   genres is skipped. When these guards leave nothing, the other buckets are tried, then the guards are relaxed.
4. Draws one of the group's best 12 titles with a softmax over the scores scaled to [0, 1] (temperature 0.35 for taste,
   0.7 for the discoveries); a title of the same media type as each of the last 2 picks counts half (movies and TV mix).
5. The logo comes from TMDB images (profile language, English, language-less).

Measured (tests with a fixed RNG, `TestSurpriseMixOverThirtyPresses` / `TestSurpriseBucketShares`): 30 presses of a
profile that loves The Godfather Part II use 5 seeds, 12 main genres and 8 non-English titles with no repeat; 1,200
presses split 50 / 24 / 26 % and The Godfather Part II seeds 10 % of the taste picks.

Clients: web `features/surprise` (`SurpriseButton` + `SurpriseModal`: backdrop, logo, reason, overview, Play /
Details / Something else; Play on the home page uses the page's player, elsewhere the detail page with `?play=1`;
the last 30 picks of the tab go out as `exclude`), as a centered pill under the last row of the home page
(`home/components/HomeSurprise`) and next to the search field. TV: a centered button under the last home row
(`RowsScreen` `surprise`, `data-nav-id="home-surprise"`), reached with Down from the last row; while it has focus the
hero explains it. It opens `screens/SurpriseScreen.tsx` (full screen; Play opens the detail page with autoplay, the
pick survives Back, Back returns the focus to the button). Both clients build the reason text (`reasonText`), with
the Turkish locative suffix for adjacent picks (`trLocative`: "Bilim kurguda", "Suçta").

## More Like This

`catalog/similar.go` (pipeline, cache, endpoint), `similar_fetch.go` (TMDB) and `similar_score.go` (pure scoring) build
the "More Like This" list of the detail pages (`GET /api/catalog/:type/:id/similar`, also behind the legacy
`/api/tmdb/:id/related`) and the "Because you ..." rows of [Recommendations](#recommendations). TMDB's own
`/similar` (genre and keyword overlap of any quality) and lately `/recommendations` are poor on their own, so they are
only two of the candidate sources.

Candidates (in parallel; lists through the shared TMDB cache in the request language):

1. The movie's collection (`/collection/{id}`): every released part, shown first in release order (at most 8).
2. TMDB `recommendations` pages 1-2 (position kept: the audience signal) and `similar` page 1.
3. People: the director(s) of a movie or creators of a show (2) and the top 3 billed cast,
   `/person/{id}/combined_credits`: directed / created / written works and lead roles (movie order ≤ 3, 6+ episodes,
   no "Self" roles), best voted 25 each. Cross-type works come from here (El Camino for Breaking Bad).
4. Keywords: the source's keywords get an IDF weight `ln(cap / (1 + titles with it))` from discover's
   `total_results` (cap 20,000 movies / 4,000 shows; counts cached 14 days); weights ≤ 0 and generic keywords
   ("based on novel or book", "sequel", "woman director", "duringcreditsstinger", ...) drop out. The 4 strongest
   keywords that at least 15 titles carry are discovered with the main genre, sorted by vote count.
5. Non-English sources: the best voted titles of the same original language and main genre (2 pages).

Main genre: the first TMDB genre, or the first specific one when it is broad (Drama, Action, Adventure, Comedy;
Interstellar → Sci-Fi, Parasite → Thriller). TV genres are compared in movie terms (`commonGenre`).

Two stages: every candidate is scored on its list data, the best 60 (plus collection parts and spin-offs) get their
details with keywords (`/movie|tv/{id}?append_to_response=keywords`, English, kept 7 days in a compact cache of
the matcher, 15,000 titles) and are scored again. Score:

| Term | Value |
|---|---|
| audience | 1.4 × (recommendations 1/(1+0.05·pos) + similar 0.45/(1+0.05·pos) + 0.35 when in both) |
| genres | 2.5 × weighted Jaccard (main genre 1.6×, broad genres 0.6) |
| keywords | 3.5 × min(1, 2·Σ shared IDF / √(source IDF total × 1.5 × max(8, candidate keywords))) (stage 1: 0.5 per keyword list) |
| people | +1.4 same director / creator (half without a shared genre), +0.35 per top-billed actor in a lead role (2 max) |
| language | same original language +1.5 for ja, ko, tr, hi, zh, th and other strong local markets, +0.7 other non-English, +0.3 English; different: −1.0 (strong source), −0.2 (other non-English), −0.3 (English source); +0.25 shared origin country |
| era | 0.8 × e^(−years apart / 12) |
| quality | 0.9 × (Bayesian rating − 6.6) + 0.2 × log10(1 + votes), prior m = 400 votes (TV 200), halved for non-English sources, quartered for sources under 2,000 votes |
| audience fit | live-action source: kids' animation dropped, other animation −2.2, family titles −1.0; animation source: live action −1.8 (anime), −2.2 (kids' source, non-family title) or −0.8; anime vs western animation −0.6; a kids' source drops non-family horror / crime / thriller titles when either side is animated, else −1.5 |
| cross type | −1.0, only with the same creator, a spin-off title, 2 shared actors or keyword similarity ≥ 0.4 |

Dropped: adult titles, videos, making-ofs / specials (search's `isExtra`), titles without poster, genres or a past
release date, documentaries, news / talk / reality shows and shorts under 45 minutes unless the source is one, titles
with nothing in common (no shared genre, keyword similarity < 0.2, no creator link), and titles under the vote floor:
150 movies / 80 shows, a third for non-English or little-known sources, at most a quarter of the source's votes (at
least 15); collection parts and the creator's works need a third of it.

Spin-offs: a title containing the other as whole words (the shorter needs two words or ten letters) and linked by
the creator or TMDB's lists ("El Camino: A Breaking Bad Movie", "The Mandalorian and Grogu"), up to 3 right after the
collection.

Selection: collection and spin-offs, then by score with at most 2 titles of any other collection (the source's own
only in the block), 2 of one title prefix before a colon ("Star Trek: ..."), 4 of the source's director / creator
and 3 of the other media type; 24 titles, below a score of 3 only to reach 12. `reason` tells `collection`,
`spin_off` or `match`.

Cache: per title and language for 24 h (concurrent requests share one computation; when TMDB fails, the expired list
is served and retried after a minute; a source's details are kept the same way). TMDB requests of the matcher are
limited to 12 at a time server-wide. A cold list takes about 1-5 s (60-120 TMDB requests, most of them cached for
the next title), a cached one a few ms. Per request, with a profile (`profile_id` or `X-User-ID`): disliked titles
are removed, finished ones −1.5 and started ones −0.5, + 0.6 × the mean genre affinity of the search taste; the
collection / spin-off block keeps its order.

Clients: web `fetchRelated` (`features/catalog/titles.ts`, 12 cards with `profile_id`) and TV `related`
(`lib/catalog.ts`, a row of 24) call `/api/catalog/:type/:id/similar`; older TV builds still call `/api/tmdb/:id/related`,
which answers the same list.

## Search filters

`GET /api/catalog/discover` (`catalog/discover.go`, see [API.md](API.md#discover-search-filters)) backs the genre
chips of both search pages. A `Filter` holds the type (`all` / `movie` / `tv`), genres (AND) and the Anime flag
(Animation + `original_language` `ja`). Clients send TMDB movie genre ids; `filterGenres` maps every one to its TV
counterpart (Action / Adventure → Action & Adventure, Sci-Fi / Fantasy → Sci-Fi & Fantasy, War → War & Politics,
Thriller → Mystery, Kids → Family for movies) or to none (History, Horror, Music, Romance; Reality only exists for TV),
which rules that media type out. Without a query the filter becomes TMDB discover queries (`with_genres` joined with
commas = AND), a wider pool of movie and TV pages re-ranked by the [browse score](#browse-ranking); with a query the ranked
search ([Search ranking](#search-ranking), 5 multi-search pages, cached 15 min) is filtered with the same mapping
(`Filter.Match`).

Web (`features/search`): type toggle (All / Movies / TV), chips sorted by their localized names (chips that cannot
match the type are hidden and dropped), filters in the URL (`/search?q=&type=&genres=&anime=1`), infinite scroll while
browsing. TV (`screens/SearchScreen.tsx`, `lib/discover.ts`): the filters sit in the left column under the keyboard
(a type row, then Anime and the genres in a 3-column grid; the column scrolls with the focus), OK toggles; the next
browse page loads when focus nears the end of the grid.

## Search ranking

`catalog/search_rank.go` orders `/api/tmdb/search`, `/api/tmdb/suggestions` and `/api/catalog/discover?q=` itself; TMDB's multi
search (pages 1-3 asked at once, 5 with filters; people and titles without artwork dropped; cached 15 min per language
and query) only finds the candidates. Texts are normalized first (`search_text.go`): lowercase, Turkish letters and
accents folded (İ/ı → i, ş → s, é → e), apostrophes dropped, other punctuation → space; a leading "the / a / an" is
ignored where noted.

Score of a result = 3 × text + 0.4 × log10(1 + popularity) + 0.3 × log10(1 + vote_count) + 0.3 × (1 − TMDB position /
pool size) + 0.15 (released this or last year and popularity ≥ 20) + 0.35 × the profile's mean genre affinity − penalties:

- text: the best match of the query against the title, original title and learned localized titles: exact 1.0 >
  exact without article 0.97 > prefix ending at a word 0.8 (+0.1 × coverage) > other prefix 0.72 > whole-word phrase
  0.68 > all words 0.6 > a word starts with it 0.55 > substring 0.45 > fuzzy 0.6 × similarity (≥ 0.72; 0.6 ×
  Damerau-Levenshtein + 0.4 × Jaro-Winkler, +0.08 for the same phonetic key) > 0;
- −0.6 under 5 votes, −0.25 under 30 (not for titles released in the last 60 days or later);
- −0.5 documentaries and −0.7 making-ofs / specials / podcasts ("making of", "behind the scenes", "special look", ...)
  unless the query asks for them ("documentary", "belgesel", "making", ...); −0.4 talk and news shows; −0.4 no poster;
- taste: genre affinity in [−1, 1] from the profile's ratings (love 2, like 1, dislike −2, favorite +1; 40 titles, genres
  from the cached TMDB details, TV genres mapped onto movie genres), computed in the background and cached 30 min per
  profile; a search never waits for it.

Typo correction (`search_index.go`): a title index built from TMDB's daily ID exports
(`http://files.tmdb.org/p/exports/movie_ids_MM_DD_YYYY.json.gz`, `tv_series_ids_…`; today's appears around 8:00 UTC, so
up to 3 days back are tried) through `platform/upstream` (3 tries, 4 min budget, 128 MB cap). Kept: not adult, not
`video`, popularity ≥ 1, a Latin letter in the title, the 200,000 most popular (measured: 200k titles, about 55 MB heap,
6.6 s download + build, 3.3 MB file, 0.5 s load at start). Saved to `search-index.tsv.gz` in `SEARCH_INDEX_DIR`
(`/app/data`), rebuilt when older than 24 h (checked hourly). Localized titles that TMDB searches return are learned as
aliases (`search-aliases.tsv.gz`, up to 50,000) and become searchable at the next rebuild of the names (every 10 min).
Lookups: trigram postings (a trigram in more than 60,000 names is skipped), a phonetic key (consonant skeleton:
"mandoloarien" and "mandalorian" are both `mndlrn`), Damerau-Levenshtein, Jaro-Winkler and popularity
(+0.06 × log10(1 + popularity)); 2-5 ms per query.

A query is corrected when the index's best hit (similarity ≥ 0.75) differs from it and no result matches at least as a
prefix ending at a word, counting only results with 50 votes or popularity 5, unless the correction is not 10 times as
popular (an obscure film named "Interstelar" does not hide Interstellar). The correction is then searched too (started
right away when a query word is in no indexed title), its results scored against the corrected text and merged (best
score per title); the corrected title itself is added from its details when that search missed it. `did_you_mean`
names it when one of its results matches it at least as a word prefix.

Suggest (`/api/catalog/suggest`) uses only the index: prefix (1.0, 1.05 for a name in the request language), a word
prefix (0.85), a typo-tolerant prefix (0.6 × similarity + 0.1, compared with the name's start one letter longer than the
query), each + 0.15 × log10(1 + popularity), one per title and display text.

Clients: web `features/search` (`api.ts`): ranked results, "Did you mean *X*?" above the grid (click searches X), a
completion list under the field while typing (150 ms debounce, arrow keys + Enter, Escape closes); the navbar dropdown
keeps the server's order. TV (`screens/SearchScreen.tsx`, `lib/search.ts`): completions as a list under the on-screen
keyboard (D-pad Down, OK searches it) and a "Did you mean X?" chip above the results.

## Browse ranking

`catalog/browse_rank.go` orders filter-only browsing (`/api/catalog/discover` without `q`). Pool per allowed media type:
TMDB discover `popularity.desc` pages 1-5, `vote_count.desc` 1-3 and `vote_average.desc` 1-3 with `vote_count.gte=200`
(same AND genres and Anime rule, each page cached 24 h, 6 requests at a time). Score:

- the Bayesian (IMDb) weighted rating WR = v/(v+m)·R + m/(v+m)·C, v = vote_count, R = vote_average, C = the pool's mean
  rating, m = its 75th percentile vote count (a 9.0 from 12 votes does not beat an 8.6 from 30k);
- + 0.35 × log10(1 + vote_count) + 0.3 × log10(1 + popularity) + 0.4 × (1 − age / 3 years) for titles under 3 years;
- server-wide signals (every profile's watch history and ratings, `SetLocalSignals`, cached 5 min, dropped on rating
  changes): + 0.4 × log2(1 + profiles that watched it) + 0.5 per love / like − 0.8 per dislike;
- the current profile (`X-User-ID`): + 0.6 × mean genre affinity (the search taste), − 1.5 when it finished the title,
  − 0.8 when it started it; its disliked titles go after everything else.

Pages of 40 (`type=all`) or 20 come from the ranked pool; later pages continue with TMDB's popularity order from page 6
without the pool's titles. Scores stay internal. Trakt watcher counts are not used (they would need an API key setting
and one lookup per title).

## Top 10

`server/internal/modules/top10` serves Netflix's public weekly Top 10:

- Sources: `all-weeks-countries.tsv` (~32 MB) and `all-weeks-global.tsv` from `www.netflix.com/tudum/top10/data/`.
  When netflix.com cannot be reached (connect timeout 15 s; it was intermittently unreachable from this network
  during development) the newest Wayback Machine copy (`web.archive.org/web/29991231000000id_/<url>`) is used; the
  log says which (`top10: loaded <url>`).
- Both files are stream-parsed line by line. The countries file keeps only rows of the configured countries (`TR`)
  and of each country's latest `week`; the global file keeps its latest week. Ranks are sorted ascending.
- Global: Netflix publishes separate English and non-English lists. They are merged into one top 10 by
  `weekly_views` when every row has it, else by `weekly_hours_viewed`, else the English list is used alone; ranks
  are renumbered 1–10.
- TMDB mapping (`/search/movie` for Films, `/search/tv` for TV): queries are the `show_title` (the series name for
  TV), the `season_title` without `: Season N` / `: Limited Series` …, and the part before a colon; the first
  query with results wins. Within it an exact normalized title (or original title) match with the highest
  popularity is preferred, else the most popular of the first 5 results; adult titles are never picked. Matches
  (and misses, retried after 3 days) are cached in `top10_titles` with their display data, refreshed after 30 days.
- Lists keep Netflix's rank order with unmatched titles skipped (ranks are not renumbered) and a show that appears
  with several seasons only once, at its best rank. They are stored in `top10_lists`, so a restart serves them
  immediately; a refresh runs at startup when any list is missing or older than 12 h, then every 12 h (1 h after a
  failure). A failed download or a list without any match keeps the last good list.

## TV apps

Two TV clients use the same API as the web app. Besides the access rules below, their only client-specific inputs
are the playback caps they send (`hevc`, `hdr`, `dv`, `atmos`, `acodecs`, `split_acodecs`, see docs/API.md) and the
playback log `client`.

- `tv/webos/` (LG): a packaged web app served by the `tvapp` container. It is loaded from `file://`, so its
  requests carry `Origin: null` and need `?app_key=<TV_APP_KEY>`. The player feeds `/play` into Media Source
  Extensions with byte budgets, split audio and seamless splices (`player/mseStream.ts`).
- `tv/android/` (Android TV / Google TV): a native app (APK). It sends no `Origin`, so the Host check (LAN
  addresses, `nextflix.local`) is all that applies. ExoPlayer plays the live fragmented MP4 without its own
  retries; seeks outside the buffer, audio track changes, subalt and late upgrades become a new `/play` request
  at the current position instead of a splice. It ports the webOS rules (resolve, reuse, probe length check,
  seekpoint, stall / sidegrade / upgrade, keepalive, progress, release, next episode, subtitles) and
  logs as client `tv`. It also publishes the profile's Continue Watching to the Android TV home screen (Watch Next).

Home snapshots: both TV apps load the home rows through `GET /api/home` (see [Home endpoint](#home-endpoint)) and
persist the last answer on the device, so the next launch shows the home rows at once from
the snapshot and replaces them when the fresh answer arrives (a `null` section keeps the snapshot's). The webOS app
sends header-less GETs (`?x_lang=`, `?profile_id=` instead of `X-Lang` / `X-User-ID`, so no CORS preflight per
request) only after `/api/home` answered 200 with `version >= 1` from that server; the flag is kept in
`localStorage` `nfx-tv:qp:<server>` and cleared on a 404. Until then it keeps sending the headers.

## Containers

See [DEPLOY.md](DEPLOY.md). `server` and `web` are built from `deploy/server.Dockerfile` and `deploy/web.Dockerfile`
with `server/` and `web/` as build contexts.
