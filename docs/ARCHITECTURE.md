# Architecture

Nextflix is a modular monolith: one Go binary (`server/`) and one Next.js app (`web/`). Each business domain lives
in its own module on both sides; shared infrastructure lives in `platform/` (server) and `shared/` (web).

```
server/                     Go 1.24, gin, gorm + SQLite, go-redis v8   (module name: nextflix)
  cmd/nextflix/main.go      wiring only
  internal/platform/        config, database, httpserver, cache, tmdb, upstream
  internal/modules/         profiles, progress, ratings, recommendations, top10, catalog, providers,
                            notifications, streaming, images
web/                        Next.js 15 (App Router), React 19, TypeScript
  src/app/                  thin routes: export { default } from '@/features/...'
  src/features/<domain>/    screens, components, api/store files of one domain
  src/shared/               lib (api, profile, image), i18n, navigation, hooks, ui, types
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
| `tmdb` | TMDB HTTP client (v4 token as Bearer, on `upstream`), types, 24 h in-memory cache shared by catalog, providers and notifications; expired entries are kept 7 days and served when TMDB fails (see [DPI resilience](#dpi-resilience)) |
| `upstream` | Resilient HTTP client for TMDB API, image CDN and subtitle providers: short timeouts, retries on a new connection, hedging (GETs), per-request budget, `Post` for repeatable POSTs (see [DPI resilience](#dpi-resilience)) |

### Modules (`server/internal/modules/…`)

| Module | Responsibility | Tables |
|---|---|---|
| `profiles` | Profile CRUD, max 5, per-profile UI `language` (`en`/`tr`, default `en`), seeds "Home" on first start, delete cascades through cleaners | `profiles` |
| `progress` | Continue watching, per-episode progress | `stream_progress` |
| `ratings` | Per-profile ratings (`dislike`/`like`/`love`) and Favorites (replaces My List), one-time My List migration, change hook; see [Ratings and Favorites](#ratings-and-favorites) | `title_ratings`, `rating_migrations` (`my_list_items` is kept, read-only) |
| `recommendations` | Per-profile rows built from ratings + TMDB recommendations/similar; see [Recommendations](#recommendations) | – (memory cache) |
| `top10` | Netflix weekly Top 10 (Turkey and global) mapped to TMDB; see [Top 10](#top-10) | `top10_lists`, `top10_titles` |
| `catalog` | TMDB lists, details, search, related titles, seasons | – |
| `providers` | TV shows per streaming provider (TMDB discover across regions) for the TV Shows page rows | – |
| `notifications` | Redis-backed feed filled by TMDB-only generators | Redis |
| `streaming` | TorrServer files/probe/play (ffmpeg fMP4), auto-resolve through Jackett (plus Bitsearch and IMDb id search) with swarm warm-up and 4K upgrades, indexer settings; see [Streaming](#streaming) | `torrent_configs`, `stream_sources` |
| `subtitles` | Subtitles from the torrent itself (separate files, small embedded text tracks), OpenSubtitles (moviehash sync), SubDL and turkcealtyazi.org; SRT/ASS/VTT → UTF-8 WebVTT with Turkish charset repair, ranking, disk cache, settings | `subtitle_settings`, files in `/app/data/subtitles` |
| `images` | Disk-cached proxy for TMDB images (`GET /tmdb-img/t/p/:size/*file`) plus a home feed warmer; see [Images](#images) | files in `/app/data/img-cache` |

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
2. `database.Open`, then `database.Migrate` with the models of profiles, progress, ratings, top10 and streaming.
3. `cache.New` (on error the server logs it and runs without Redis), `tmdb.New`.
4. Context cancelled by SIGINT/SIGTERM.
5. `progress`, `ratings`, then `profiles` with both as cleaners, plus `EnsureDefault`; `ratings.MigrateMyList()`
   (one time, logged) and `go ratings.Backfill(ctx)`; `recommendations` (ratings adapter + TMDB catalog, hooked to
   `ratings.OnChange`); `top10` (`Start(ctx)` loads stored lists and refreshes in the background).
6. `catalog`, `providers` (both on the shared TMDB client), `images` (`Start(ctx)` launches cleanup and the warmer), `notifications` (`go Start(ctx)`, `defer Close()`), `streaming` (also on the shared TMDB client, for the runtimes resolve and probe check files against) plus `EnsureSettings`.
7. `httpserver.New`, `images.Register(r, …)` on the root, `api := r.Group("/api")`, every other module's `Register`, `httpserver.Run`.

## Web

| Feature (`web/src/features/…`) | Routes |
|---|---|
| `home` | `/` |
| `catalog` | `/movies`, `/tv-shows`, `/new-popular`, `/tmdb-movie/[id]` |
| `search` | `/search` |
| `favorites` | `/favorites` (`/my-list` redirects here) |
| `notifications` | `/notifications` |
| `profiles` | `/profiles` (and `ProfileGate`, which forces a profile pick on every page) |
| `settings` | `/settings` |
| `watch` | player, Watch button, seasons/episodes, Continue Watching row (used by other features) |
| `shell` | navbar and page shell |

### Rules

- Files in `src/app/` are thin: a route re-exports a screen. Screens take no props and read
  `useParams()` / `useSearchParams()`.
- Features may import `shared/*`. Imports between features are kept to the public pieces listed above
  (for example `watch` components, `shell/PageShell`, `favorites/store`); `shared/*` never imports a feature.
- All imports use the `@/` alias.
- Images always go through `/tmdb-img/t/p/<size><path>` (`shared/lib/image.ts`), never `image.tmdb.org`. Card,
  billboard and hero artwork is rendered with `shared/ui/TmdbImage` (see [Images](#images)).
- The active profile lives in `localStorage` under `hfx-profile`; changes fire the `hfx-profile-change` event.
  API calls that are per profile send `profileHeaders()` (`X-User-ID`).

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
2. Server: `httpserver.Lang(c)` reads `X-Lang` (`tr`, `tr-TR` -> `tr`; anything else -> `en`) and sets
   `Vary: X-Lang`. Handlers call `tm.WithLang(lang)`, a view of the shared `tmdb.Client` that sends `language=tr-TR`
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
  (trending, popular TV, coming soon, new episodes, popular movies, the genre rows) and downloads the sizes the
  UI uses (backdrop cards `w92 w300 w500 w780`, poster cards `w92 w342 w500`, the six billboard candidates
  `w300 w780 w1280 original` plus their `w500` logos), 3 at a time (user requests keep most download slots and
  the connection bursts stay small), for English and Turkish lists (Turkish posters
  and logos differ). Keep `homeGenreRows` in `warmer.go` in sync with `GENRE_ROWS` in
  `web/src/features/home/HomeScreen.tsx`.

### DPI resilience

The ISP (Kablonet) resets or stalls some TLS connections to TMDB with DPI. On Windows GoodbyeDPI hides most of
it, but Docker traffic through mirrored WSL networking still sees it: a reset fails every request on that
connection, and a stalled one used to hang until the old 15 s header timeout (images answered `502` after 15 s).
Resets come in bursts: in the measurements all 12 in-flight connections to the CDN were reset at once.

`platform/upstream.Client.Get` (TMDB API and images) reads the whole body and:

- dials with 3 s connect, 3 s TLS handshake and 5 s response header timeouts, and aborts a body that makes no
  progress for 2 s (1.5 s for images);
- retries resets, EOFs, timeouts, truncated or stalled bodies, `429` and `5xx` up to 3 tries in total, after a
  100-300 ms jittered pause, each retry on a new connection (a keep-alive-less transport);
- hedges: when no try has response headers after 1.2 s (images) / 1.5 s (API), it starts one parallel try on a new
  connection; the first good answer wins and the other is cancelled;
- stops at a per-request budget (images 4 s, API 8 s) with an error wrapping `context.DeadlineExceeded`;
- speaks HTTP/1.1 with a per-host pool (16, images 12). Measured from a container on `nextflix_default` (1,200
  poster fetches per variant, 12 at a time): HTTP/2 multiplexes everything on one connection, so one reset failed
  12 requests at once (24 of 1,200 with a plain client); with retries both HTTP/2 and HTTP/1.1 had 0 failures, and
  HTTP/1.1 had the lower p95 (about 250 ms vs 330-420 ms, h2 suffers head-of-line blocking on one connection);
- dials IPv4 only when the host has no IPv6 route (Docker's bridge has none; checked once with a UDP "dial").

`platform/tmdb.Cached` serves stale data on errors: entries stay 7 days past their 24 h TTL; when a refresh
fails the stale value is returned and re-checked after a minute. Combined lists (`GetUpcomingMovies`,
`GetUpcomingTVSeries`, lists with videos) whose parts failed prefer stale complete data and are otherwise cached
for only a minute instead of 24 h. Re-measure with
`go test -tags live -run Live -v ./internal/platform/upstream/` (needs `TMDB_API_KEY`, prints p50/p95/max).

Every network failure that is left after the tries wraps `upstream.ErrTransport`. `Client.Post` sends a body with
the same retries on a new connection but never hedges; it is only for endpoints that are safe to repeat.
`Options.RetryStatus` narrows the retried statuses and `Options.CheckRedirect` limits redirects (a refused redirect
is final, not retried).

Subtitle providers (`modules/subtitles/providers_http.go`) use it too, since the same DPI resets their TLS
connections from Docker (logs showed `connection reset by peer` on turkcealtyazi.org and a cut OpenSubtitles answer,
`unexpected end of JSON input`):

- OpenSubtitles and SubDL share one client (3 tries, hedge after 4 s for GETs, 15 s header timeout, 5 s stall
  timeout, 25 s budget, retries 500/502/503/504); their search GETs, file downloads (SubDL zips, OpenSubtitles
  links) and the OpenSubtitles `login` / `download` POSTs retry resets on a new connection;
- turkcealtyazi.org has its own client: no hedging (one request every 2 s is the polite rate), only 502/504 retried
  (its 503 is a Cloudflare challenge), redirects only on the site (`/tt<id>/` → `/mov/<id>/<name>.html`); its
  download form POST retries resets the same way. When FlareSolverr reports a network error of its browser
  (`net::ERR_CONNECTION_*`, `ERR_EMPTY_RESPONSE`, `ERR_SSL*`, ...) the page is loaded through it once more;
- a provider call that failed on the network (`ErrTransport`, a timeout, 5xx, a cut JSON answer, a FlareSolverr
  network error) is not cached: the next `/api/subtitles` call (the players ask again after 8 s and 20 s) tries
  again. Real "no results" stay cached 30 min, other failures (bad key, quota, title not found) 3 min.

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
   a 4K needs 3 seeders); the fast ones answered and 1.2 s passed, or 1.5 s passed, and there are 3 of 1080p or more
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
   already owns stops. Episode requests only accept the episode, ranges covering it, season packs of its season and
   complete-series packs containing it; bonus material ("Greatest Moments", "Inside the Episode", "Making of",
   "Recap", "Sample", ...) is rejected. The episode info after the title (`tsParseEpisode`, after an optional year /
   country and the repeated title of "Çukur / Cukur (Season: 4 ...") must come first, else the title goes on (another
   show). Notations: `S01E05`, `S01E05-E07`, `S1 E5`, `1x05`, `S01`, `S01-S03`, `Season 1 Episode 5`, `Seasons 1-3`,
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
     wait for the running one and only start their own when it fails. A verified file beats any file of unknown
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
4. `tsPickBestKeep` returns the best good candidate (keeps up, duration verified or runtime unknown) as soon as it is
   there, also from a running check's snapshot. It waits only while a better-ranked check of at least its resolution
   is still running and already delivers ≥ 0.8 of its need (`tsPromising`): a 3 s grace (15 s for a higher resolution
   without an upgrade path), cut short as soon as that check falls below 0.8 or finishes. Unmeasured or slow 4K checks
   are not waited for, they keep running for the upgrade path; checks of the picked resolution keep running too
   (possible spares). 12 s after the checks started (`tsResolveAnswerCap`, a
   hard wall that also cuts a grace short) it answers with a good one if there is one, else with the most playable
   source (`tsChoosePlayable`) among the finished checks and the snapshots of running ones (each check reports one as
   soon as its file is picked, then after every poll): alive (active peers or data, not dead) over a dead or silent
   swarm, fit for the client (not unfit), measured (a verdict, or ≥ 2 s
   of samples) over unmeasured, a clearly better rate / need ratio (≥ 0.1 higher and ≥ 1.5×; a 4K below 0.3 never
   beats a ≤ 1080p with a higher ratio), a verified duration, the lower need (bitrate) when the ratios are close,
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
   that endpoint starts a search restricted to higher resolutions, at most every 10 minutes per title. Up to 2 live
   alternatives of the picked resolution stay as spares; every other candidate is dropped from TorrServer at once.
6. A starting stream gets the line (`quiet.go`): once resolve has answered, its background work (checks that keep
   running, upgrade warm-ups and upgrade searches, spare measurements, late candidate checks, sidegrade searches, their
   probes) waits while another torrent starts: the player's `probe` of it runs, or its play stream is younger than 30 s
   (every seek is a new stream; up to 90 s while the stream has not sent its first bytes, e.g. an MP4 whose `moov`
   still comes from the end of the file), logged as
   `warm: deferring … while … starts` / `resuming … after …`. A measurement interrupted before its verdict starts over
   afterwards; one that already has a verdict ends with it. Checks before the answer (Play, prefetch on the detail
   page) never wait for a starting stream; another title's playback is a different rule (playback priority below).

The chosen source is saved in TorrServer's DB (`save_to_db`, category `nextflix`), so after a drop or a restart it opens
from its stored metadata (≈ 20 ms instead of seconds of metadata exchange). Closed entries older than 7 days are
removed at start and every 6 hours.

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
  arrival or the player's last `draining=1` poll when those are later). `GET /sidegrade` offers one that keeps up, or
  one clearly faster than a starving source (≥ 1.5× its rate, ≥ 0.5× its need), measured within 45 s (else it
  measures them in the background), at most one per title every 2 minutes, never one in `avoid=` (such spares are
  dropped). For a starving source a spare of a lower resolution counts too when it keeps up (the playing resolution
  or higher is preferred).
- A draining player (`draining=1`, polled every 5-10 s while its buffer drains) makes the source count as starving
  whatever resolve measured; `critical=1` (under 6 s of buffer) also allows a lower resolution that keeps up. When
  no spare is left to measure, it starts a background search (`searchSidegrade`, at most every 3 minutes per title):
  the title is resolved again capped at the playing resolution, without the playing torrent and the avoided ones; its
  checks are background work (quiet gate). A pick that keeps up with a fitting length becomes a spare, which a later
  poll offers.
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

The player (`web/src/features/watch/components/TorrentStreamPlayer.tsx`) polls `upgrade` every 15 s during the first
5 minutes while a source below 2160p plays on an HEVC client. At most one upgrade per session, never after a step down.
When playback stalls (two stalls within 60 s or one over 8 s) it first asks `sidegrade` for a faster source of the
same quality (at most every 2 minutes; a source resolve measured as starving is checked 8 s after it is ready, then
every 15 s during the first 3 minutes, before the first frame too when playback started at 0), else it re-resolves
with `maxq=1080`, then `720`. A probe that reports `duration_mismatch` re-resolves with the file avoided, at most twice.
While a sidegrade loads in the hidden slot (below), a stall does not step down: the step down waits for that
preparation and runs only if it fails.

A stream that breaks before its first frame (an element error: the server dropped or cut it) is restarted silently at
the same position once per torrent; if that fails too the title is resolved again once (`refresh`, the failed torrent
in `avoid`), and only then "Playback failed" shows. A stream that ended well before the file end restarts silently
(up to 3 times, then "connection lost"). An active element that ends up paused with no media at all (`readyState` 0, not a hold, not a viewer's pause, which keeps its data) for two 3 s checks restarts the stream at the last position the same way.

Adaptive start buffer (`web/src/features/watch/startBuffer.ts`, ported to `tv/webos/src/player/startBuffer.ts`):
a source whose resolve health (`rate / need`) is below 2×, or unknown (saved progress, given sources), starts paused
(`preload=auto`, still downloading) until 20 s are buffered ahead, at most 10 s after the first frames were playable
(`readyState` ≥ HAVE_FUTURE_DATA); a source at 2× or more starts at once. After a stall (`waiting` once playback
started, not while seeking) any source is held paused until 8 s are buffered, at most 12 s. Either hold also ends
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
like any switch, but its lead stays inside the visible buffer (`min(8 s, max(3 s, buffered ahead − 2 s))`), and with
less than 13 s buffered 2 s from `t0` count as ready, so it takes over at a frame boundary before the visible stream
runs dry; no toast, no overlay. A sidegrade still loading is dropped (its torrent released) once the visible stream
was healthy again for 15 s. Torrents the player left for being slow (any sidegrade or step down away from them) and
sidegrades whose preparation failed are never switched to again in the session (an offer of one is released). The
TV app (`tv/webos/src/player/engine.ts`, one `<video>`) uses the same verdict but cannot switch without a restart: it
holds the offer and switches (with the toast) as soon as the buffer is critical or playback stalls, and drops it
after 15 s of healthy playback.

Switching sources (`web/src/features/watch/seamlessSwitch.ts`): the player has two `<video>` slots, the visible one
and a hidden, muted one; every handler, the controls, the progress saver and the subtitle overlay follow the active
slot. An upgrade or sidegrade offered while the current stream plays (or is paused) is prepared in the hidden slot,
one preparation at a time:

1. `files` (re-adds the torrent), then the same `probe` the player runs before playing. A failed probe,
   `duration_mismatch`, or a length more than 3 s off the playing file ends it silently.
2. The new stream is requested about 8 s ahead of the visible position, with the audio track of the current track's
   language (same channel count first, else the usual default rules) and the same `hevc`/`acodecs` caps.
   `seekpoint` gives its real start `t0` (the keyframe before the request; asked again further ahead while that is
   too close). It is ready once `readyState` ≥ HAVE_FUTURE_DATA with 5 s buffered from `t0` (2 s once the browser
   stopped fetching on its own).
3. When the visible video presents the frame at `t0` (within 50 ms, `requestVideoFrameCallback`; a 40 ms poll where it
   is missing) the hidden one starts playing, still muted and invisible, next to it. Only once its time advances (within
   3 s; else the preparation is dropped and the visible stream is never touched) the visible one is paused, the hidden one gets the current volume, fades in over 150 ms, and becomes the active stream (hash, file, duration, audio tracks, offset, quality badge). 250 ms later the
   old element drops its stream and the old torrent is released. No toast and no loading screen; an upgrade shows a
   small "Now in 4K" note for 2 s while the controls are hidden. Subtitles are listed again for the new file.
4. If the visible stream stalls while the hidden one already holds the current moment (1.5 s of it buffered, also
   before it counts as ready), it takes over at once (after a seek inside its buffer). If the visible video got past the hidden start, the hidden one seeks forward inside its
   buffer, else it restarts further ahead (up to 3 times, the lead grows). A seek that leaves the hidden start ahead
   keeps it; any other seek (or another audio track) restarts the preparation once the visible stream plays again
   (up to 4 times). Not ready within 60 s, an element error, or the end of the title near: the preparation is
   dropped and its torrent released (an upgrade is not tried again this session).

A stalled (or not yet started) player still switches the old way: PlayerCore remounts on the new source at the same
position with the loading screen and a toast ("Switched to 4K", "Switched to a faster source"); a step down always
does. Dev builds expose `window.__nfxDebugSwitch({ hash, index })`, which prepares a seamless switch to that file (for browser tests).

The player has a single exit control, a Back arrow at the top left (shown with the other controls, which stay up while
paused). It does what closing always did (saves progress, releases the title and its torrents, cancels the next
episode's warm-up) and the screen the player was opened from shows again; the player is an overlay without a route
of its own, so no history entry is popped. Esc still closes it.

Release from the web app: an open player marks its title / episode as playing (`holdPlaying` in `api.ts`) and
`releaseTitle` skips a playing title, so only that player releases it when it closes (or on Next). The detail page
sends `release?<title>&delay=60` when it is left (route change or `pagehide`); the player sends `release?<title>` plus `hash=<every torrent it played>` when it closes, drops a replaced
source at once (upgrade, sidegrade, step down) and releases the finished episode on Next. The next episode is
prefetched once 10 minutes are left (85 % for episodes under 25 minutes, `next_pos`/`next_dur` let the server check
it) and released when the player closes before it is reached.

Player preferences live per profile in localStorage `hfx-player:<profileId>` (written by Settings,
`features/settings/playerPrefs.ts`, and by the player for the last audio/subtitle pick): `autoplayNext: false` drops
the 5 s countdown at the end of an episode (only the Next button stays), `subSize` (`small`/`medium`/`large`) scales
the subtitle overlay by `SUBTITLE_SIZE_SCALE`, `audioLang: 'original'` picks the title's original language and skips
the "Turkish profile → Turkish audio" rule (picking the original track in the player keeps `'original'`).

Default audio track (`features/watch/audioPick.ts`, a pure function; the TV app has the same rules in
`tv/webos/src/lib/subtitles.ts`), first match wins: the last picked language (not with `'original'`), Turkish for a
Turkish profile (idem), a track tagged with the title's original language, a track whose title says "Original" /
"orig" or names the original language ("English 5.1"), an untagged / `und` track (usually the original; one without
dub words such as "Dub" / "MVO" / "Дубляж" first, the default one first) over tracks tagged with another language,
the file's default track (`default` in the probe answer), the first track. Commentary tracks are skipped. Resumes
from Continue Watching carry no `original_language`, so both players look it up (`/api/tmdb-movie/:id`, cached) and
the probe step waits up to 1.5 s for it.

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

Embedded text tracks of big files are read live (`modules/subtitles/live.go`): extracting a track of an MKV / MP4
means reading the whole file, so before, such tracks were only offered for small or mostly cached files. Now they are
listed with `live: true`; the file request carries the player's position (`t`) and ffmpeg reads the track from 30 s
before it onward (`-ss … -copyts`, so cues keep the file's times; the pieces it reads are the ones playback needs
next, mostly from TorrServer's read-ahead cache). Cues collect in memory and each answer returns what is there (a
`NOTE nextflix-partial` line and `X-Subtitle-Partial: 1` while more follows); the players ask again every 15 s (the web player also
after a seek). A position more than 5 minutes past the read's frontier (or before its start) starts a new read
there; a read stops at a range read before, after 3 minutes without requests, or when another track of the same file
is chosen (at most 3 reads server-wide). Once every second of the file was read the track is cached on disk like a
full extraction.

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

Results are cached in memory per profile for 30 minutes (empty results are not cached). `ratings.OnChange`
invalidates the profile; a computation that started before the change is not cached. Concurrent requests of one
profile share one computation.

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

## Containers

See [DEPLOY.md](DEPLOY.md). `server` and `web` are built from `deploy/server.Dockerfile` and `deploy/web.Dockerfile`
with `server/` and `web/` as build contexts.
