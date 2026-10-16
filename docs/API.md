# API

Base URL: `http://<host>:8252/api` (the web app also proxies `/api/*` to the server). All responses are JSON
except the video stream. Errors look like `{ "error": "message" }`.

Per-profile endpoints (progress, ratings, favorites, recommendations) read the active profile from the
`profile_id` query/body field and fall back to the `X-User-ID` header when it is missing.

Content language: requests may send `X-Lang: tr|en` (the web sends the active profile's language on every
`/api` fetch). Endpoints with TMDB data (catalog, recommendations, Top 10, notifications, providers) answer
titles, overviews, taglines, genre and episode names in that language (`tr` -> TMDB `tr-TR`, anything else or a
missing header -> `en-US`) and send `Vary: X-Lang`. Text TMDB has no Turkish translation for comes from en-US
(see [ARCHITECTURE.md](ARCHITECTURE.md#localization)). CORS allows the header. Streaming always resolves with the
English title of `tmdb_id`, whatever `title` the client sends.

Query fallbacks: when the `X-Lang` header is missing or empty, `?x_lang=tr|en` is read instead; when `X-User-ID` is
missing or invalid, `?profile_id=N` is read instead (the header wins when both are sent). A GET without custom
headers is a CORS "simple" request, so the packaged webOS app skips the preflight. It is `x_lang`, not `lang`,
because `/api/torrent-stream/subalt` already uses `?lang=` for the subtitle language.

Compression: JSON answers under `/api/` are gzipped when `Accept-Encoding` allows gzip (`Content-Encoding: gzip`,
`Vary: Accept-Encoding`). HEAD, `Range` requests, `/api/torrent-stream/*` (video, proxies, thumbnails), images,
event streams and 204/206/304 answers are never compressed.

## Images (`modules/images`)

Served on the server root, not under `/api`. The web app proxies `/tmdb-img/*` here.

| Method | Path | Notes |
|---|---|---|
| GET, HEAD | `/tmdb-img/t/p/:size/:file` | TMDB image from the disk cache (fetched from `image.tmdb.org` on a miss). `size`: `w92 w154 w185 w300 w342 w500 w780 w1280 original`; `file`: bare TMDB file name. `Cache-Control: public, max-age=31536000, immutable`, `ETag`, `X-Cache: HIT` or `MISS`. 404 for invalid paths or files TMDB does not have, 504 within about 4 s when TMDB is unreachable or too slow (remembered 30 s), 502 for a bad upstream answer |

## Health

| Method | Path | Notes |
|---|---|---|
| GET | `/api/health` | 200 `{status, timestamp, version, services: {database, internet}, uptime}`; 503 when the database ping fails. `internet`: `online` or `offline` (TCP to public resolvers on 443, cached 30 s online / 4 s offline); the players show their offline screen on it and resume once it is `online` |

## Profiles (`modules/profiles`)

Profile: `{ "id": 1, "name": "Home", "avatar": "smile", "color": "#E50914", "created_at": "..." }`

| Method | Path | Notes |
|---|---|---|
| GET | `/api/profiles` | 200 `[Profile]` ordered by id |
| POST | `/api/profiles` | body `{name, avatar, color}` → 201 Profile; 400 if the name is empty or longer than 20 characters, or 5 profiles exist |
| PUT | `/api/profiles/:id` | body with any of `{name, avatar, color}` → 200 Profile; 404 if missing |
| DELETE | `/api/profiles/:id` | 204; also deletes the profile's progress, ratings and favorites; 400 for the last profile |

If there are no profiles at startup, the server creates `{name: "Home", avatar: "smile", color: "#E50914"}`.

## Continue watching (`modules/progress`)

Progress:

```json
{ "id": 1, "profile_id": 1, "media_type": "movie|tv", "tmdb_id": 27205, "title": "Inception",
  "poster_path": "/abc.jpg", "backdrop_path": "/def.jpg", "season": 0, "episode": 0, "episode_title": "",
  "magnet": "magnet:?...", "hash": "40-hex", "file_index": 2, "position": 1234.5, "duration": 8880.0,
  "updated_at": "..." }
```

| Method | Path | Notes |
|---|---|---|
| PUT | `/api/stream-progress` | body = Progress without `id`/`updated_at` → 200 Progress. Upsert key `(profile_id, media_type, tmdb_id, season, episode)`; movies use season 0, episode 0 |
| GET | `/api/stream-progress?profile_id=` | Continue Watching row: newest first, one entry per title, finished entries (>= 92 %) excluded; a show whose newest episode is finished shows the next aired episode instead (its saved row, or a new one with `id` 0 and position 0), max 20 |
| GET | `/api/stream-progress/item?profile_id=&media_type=&tmdb_id=&season=&episode=` | 200 Progress or 404 |
| GET | `/api/stream-progress/title?profile_id=&media_type=tv&tmdb_id=` | every entry of one title for the profile, finished ones included (per-episode progress bars) |
| DELETE | `/api/stream-progress?profile_id=&media_type=&tmdb_id=` | 204; removes all episodes of that title |

## Ratings and Favorites (`modules/ratings`)

Replaces My List (the `/api/mylist*` endpoints are gone). Every profile can rate a title "Not for me"
(`dislike`), "I like this" (`like`) or "Love this!" (`love`) and mark it as a Favorite, independently of each other.

Rating:

```json
{ "profile_id": 1, "media_type": "movie", "tmdb_id": 27205, "title": "Inception",
  "poster_path": "/abc.jpg", "backdrop_path": "/def.jpg", "rating": "love", "favorite": true,
  "updated_at": "2026-10-08T11:00:00Z" }
```

`rating` is `"dislike"`, `"like"`, `"love"` or `null`. `media_type` is `movie` or `tv`. `profile_id` may be left
out of a query or body when `X-User-ID` is sent. 400 `{error}` for a missing profile, bad `media_type`, `tmdb_id`
or `rating`; 400 `Unknown profile` when the profile does not exist.

| Method | Path | Notes |
|---|---|---|
| GET | `/api/ratings?profile_id=N[&favorite=1][&rating=love]` | 200 `[Rating]`, newest change first (`updated_at`). `favorite=1` (or `true`) keeps favorites only, `rating=` one rating value |
| GET | `/api/ratings/item?profile_id=N&media_type=movie&tmdb_id=27205` | 200 Rating, 404 `{error: "No rating found"}` when the title has neither a rating nor a favorite |
| PUT | `/api/ratings` | body `{profile_id, media_type, tmdb_id, title, poster_path, backdrop_path, rating?, favorite?}` → 200 Rating. Partial update: only the fields that are present change; `"rating": null` (or `""`) clears the rating, leaving `rating` out keeps it; `favorite` left out keeps it. Empty `title`/`poster_path`/`backdrop_path` keep the stored values. A row left with no rating and `favorite: false` is deleted, and the response shows that cleared state (`rating: null, favorite: false`) |
| GET | `/api/favorites?profile_id=N` | 200 `[Rating]` with `favorite: true`, the most recently favorited first (rating a favorite later does not move it) |

Examples:

```
PUT /api/ratings {"profile_id":1,"media_type":"tv","tmdb_id":1396,"title":"Breaking Bad","poster_path":"/b.jpg","favorite":true}
→ 200 {"profile_id":1,"media_type":"tv","tmdb_id":1396,"title":"Breaking Bad","poster_path":"/b.jpg","backdrop_path":"","rating":null,"favorite":true,"updated_at":"..."}
PUT /api/ratings {"profile_id":1,"media_type":"tv","tmdb_id":1396,"rating":"love"}
→ 200 {..., "rating":"love","favorite":true, ...}
PUT /api/ratings {"profile_id":1,"media_type":"tv","tmdb_id":1396,"rating":null,"favorite":false}
→ 200 {..., "rating":null,"favorite":false, ...}   (row deleted; GET /api/ratings/item now 404)
```

On startup the server copies the old My List (`my_list_items`, media id `"9"` + TMDB id) into Favorites once;
see [ARCHITECTURE.md](ARCHITECTURE.md#ratings-and-favorites). Migrated rows may start with an empty `title` or
`poster_path` until a background TMDB lookup fills them in.

## Recommendations (`modules/recommendations`)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/recommendations?profile_id=N` | 200 `{rows: [Row]}`; computed from the profile's ratings and favorites (algorithm in [ARCHITECTURE.md](ARCHITECTURE.md#recommendations)), cached 30 min per profile and `X-Lang` (2 min while a "More Like This" list was not ready yet; after that the old rows are answered for up to 24 h while new ones are computed in the background) and recomputed before answering after any rating/favorite change of that profile |

Row: `{id, title, reason, items: [Item]}`. Item:

```json
{ "id": 1396, "media_type": "tv", "title": "Breaking Bad", "overview": "...", "poster_path": "/p.jpg",
  "backdrop_path": "/b.jpg", "vote_average": 8.9, "first_air_date": "2008-01-20", "genre_ids": [18, 80] }
```

`id` is the TMDB id. Movies carry `release_date`, TV shows `first_air_date` (the other key is absent). `genre_ids`
is always an array. `original_language` (ISO 639-1) is sent when TMDB lists it.

| Row `id` | `title` | `reason` |
|---|---|---|
| `top-picks` | `Top Picks for You` (20 items) | `top_picks` |
| `because-<media_type>-<tmdb_id>` | `Because you loved <Title>` | `because_loved` |
| `because-<media_type>-<tmdb_id>` | `Because you liked <Title>` | `because_liked` |
| `because-<media_type>-<tmdb_id>` | `Because you added <Title> to Favorites` (favorite without a rating) | `because_favorite` |
| `trending` | `Trending Now` (20 items) | `popular` |

With positive signals the response is `top-picks` followed by up to 3 `because-*` rows (8 to 20 items each).
A profile without any `like`/`love`/favorite (or when TMDB returns nothing) gets the single `trending` row instead.
`rows` is `[]` only when TMDB is unreachable.

```json
{ "rows": [
  { "id": "top-picks", "title": "Top Picks for You", "reason": "top_picks", "items": [ { "id": 670292, "media_type": "movie", "title": "The Creator", "...": "..." } ] },
  { "id": "because-movie-27205", "title": "Because you loved Inception", "reason": "because_loved", "items": [ "..." ] }
] }
```

### Surprise me

| Method | Path | Notes |
|---|---|---|
| GET | `/api/recommendations/surprise?profile_id=N[&exclude=movie-27205,tv-1396]` | 200 `{title, reason}`: one title the profile is likely to enjoy, different on every call (algorithm in [ARCHITECTURE.md](ARCHITECTURE.md#surprise-me)). `exclude` lists `<media_type>-<tmdb_id>` (or `:`) keys the client showed lately; the server also skips the profile's last 30 picks, rated / favorited titles and titles with any watch progress. 400 without a profile, 404 `{error}` when nothing is left (TMDB unreachable), `Cache-Control: no-store`. Titles follow `X-Lang` |

`title` is an Item (above, plus `original_language`) with `logo_path` (`""` when TMDB has no logo; profile language,
then English, then language-less). `reason` always has `kind` and `bucket` (`taste` about half of the presses,
`adjacent` and `wild` a quarter each):

| `kind` | `bucket` | Fields | Meaning |
|---|---|---|---|
| `because_loved` | `taste` | `title`, `media_type`, `tmdb_id` | related to a title rated "Love this!" |
| `because_liked` | `taste` | `title`, `media_type`, `tmdb_id` | related to a title rated "I like this" |
| `favorite_like` | `taste` | `title`, `media_type`, `tmdb_id` | related to a favorite without a rating |
| `because_watched` | `taste` | `title`, `media_type`, `tmdb_id` | related to a title from the watch history |
| `genre` | `taste` | `genre_ids` (1–2 TMDB movie genre ids), `genre_id` (the first) | from a pair of the profile's top genres |
| `adjacent` | `adjacent` | `genre_id` | a well-rated title of a liked genre that is not like any seed |
| `wild_card` | `wild` | `language` (original language, only when not English) | acclaimed and outside the profile's taste |
| `trending_pick` | `taste` | – | trending this week (a profile without history) |

Genre ids are movie ids (a TV genre is reported as its movie counterpart, e.g. 10765 → 878). The seed `title` is
stored text (the language it was saved in). Clients build the sentence: "Because you loved Dune" / "Bayıldığın Dune
filmine benzediği için" (`media_type` picks "filmine" / "dizisine"), "Because you like drama and crime" / "Dram ve
suç sevdiğin için", "A new pick in sci-fi" / "Bilim kurguda yeni bir keşif", "Something completely different – a
highly rated Korean film" / "Bambaşka bir şey – çok beğenilen bir Kore filmi" (anime when `language` is `ja` and the
title has genre 16; a plain "Something completely different" for English or unknown languages).

```json
{ "title": { "id": 1408, "media_type": "tv", "title": "Queen of the South", "overview": "...", "poster_path": "/p.jpg",
    "backdrop_path": "/b.jpg", "logo_path": "/l.png", "vote_average": 7.6, "first_air_date": "2016-06-23",
    "genre_ids": [80, 18], "original_language": "en" },
  "reason": { "kind": "because_loved", "bucket": "taste", "title": "Breaking Bad", "media_type": "tv", "tmdb_id": 1396 } }
```

## Top 10 (`modules/top10`)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/top10?region=TR\|global&type=movie\|tv` | 200 `{region, week, type, items: [Item]}`; `region` defaults to `global` (case-insensitive, answered as `TR` / `global`), `type` to `movie`. 400 for other values, 503 `{error: "Top 10 is not available yet"}` before the first successful download |

Item: `{rank, tmdb_id, media_type, title, poster_path, backdrop_path, overview, vote_average, release_date | first_air_date}`
(movies `release_date`, TV `first_air_date`). Up to 10 items in Netflix's rank order. `rank` is Netflix's rank, so a
gap means that title could not be matched to TMDB; a show listed with two seasons appears once at its best rank
(the TR TV list of 2026-10-04 has 9 items for that reason). `week` is the last day (Sunday) of Netflix's week. With
`X-Lang: tr` the stored (English) list is answered with `title`, `overview`, `poster_path` and `backdrop_path` from
the Turkish TMDB details (cached 24 h); ranks are unchanged and titles without a translation keep the stored text.

```json
{ "region": "TR", "week": "2026-10-04", "type": "movie", "items": [
  { "rank": 1, "tmdb_id": 1492640, "media_type": "movie", "title": "UNABOMBER", "poster_path": "/....jpg",
    "backdrop_path": "/....jpg", "overview": "...", "vote_average": 7.1, "release_date": "2026-09-25" },
  { "rank": 2, "tmdb_id": 939243, "media_type": "movie", "title": "Sonic the Hedgehog 3", "...": "..." } ] }
```

## Catalog (`modules/catalog`, TMDB)

| Method | Path |
|---|---|
| GET | `/api/home` (see [Home](#home)) |
| GET | `/api/upcoming-movies` |
| GET | `/api/upcoming-tv-series` |
| GET | `/api/tmdb-movie/:id` |
| GET | `/api/tmdb/search` |
| GET | `/api/tmdb/suggestions` |
| GET | `/api/tmdb/movie/latest` |
| GET | `/api/tmdb/movie/now-playing` |
| GET | `/api/tmdb/movie/upcoming` |
| GET | `/api/tmdb/movie/popular` |
| GET | `/api/tmdb/discover/movie` |
| GET | `/api/tmdb/movie/:id/images` |
| GET | `/api/tmdb/movie/:id/similar` |
| GET | `/api/tmdb/movie/:id/recommendations` |
| GET | `/api/tmdb/tv/:id/similar` |
| GET | `/api/tmdb/tv/:id/recommendations` |
| GET | `/api/tmdb/tv/:id/season/:season` |
| GET | `/api/tmdb/:id/related` (legacy, same list as `/api/catalog/:type/:id/similar`) |
| GET | `/api/catalog/:type/:id/similar` (see [More Like This](#more-like-this)) |
| GET | `/api/catalog/discover` (see [Discover](#discover-search-filters)) |
| GET | `/api/catalog/suggest` (see [Search](#search-ranking-did-you-mean-suggest)) |

All catalog answers follow `X-Lang`; lists and details are cached 24 h per language (seasons 6 h, search pools 15 min).
An expired list (up to 7 days old) is answered at once while the server refreshes it in the background; expired
movie/TV details wait for the refresh. With `X-Lang: tr`,
`/api/tmdb/movie/:id/images` lists only Turkish, English and language-less images (`include_image_language=tr,en,null`),
and details videos include English trailers (`include_video_language=tr,en,null`).

### Home

`GET /api/home?x_lang=tr|en` returns the profile-independent home rows in one answer (one request instead of about
ten). It takes no profile (`X-User-ID` is ignored); the language comes from `X-Lang`, then `x_lang`. Clients send it
without custom headers.

```json
{
  "version": 1,
  "lang": "en",
  "generated_at": "2026-10-13T09:00:00Z",
  "upcoming_movies": { "trending_daily": [], "trending_weekly": [], "now_playing": [], "upcoming": [], "cached_at": "..." },
  "upcoming_tv_series": { "airing_today": [], "on_the_air": [], "trending_daily": [], "trending_weekly": [], "cached_at": "..." },
  "popular_movies": { "results": [], "page": 1, "total_results": 20 },
  "top10_tr_movie": { "region": "TR", "week": "2026-10-04", "type": "movie", "items": [] },
  "top10_tr_tv": { "region": "TR", "week": "2026-10-04", "type": "tv", "items": [] },
  "genres": [
    { "id": "action_adventure", "params": { "with_genres": "28|12", "vote_count.gte": "300" },
      "data": { "results": [], "total_results": 20, "filters": { "sort_by": "popularity.desc", "include_adult": "false", "...": "..." } } }
  ]
}
```

- Each section is exactly the body of the endpoint it replaces: `GET /api/upcoming-movies`, `GET /api/upcoming-tv-series`,
  `GET /api/tmdb/movie/popular` (page 1), `GET /api/top10?region=TR&type=movie|tv` and, per genre row,
  `GET /api/tmdb/discover/movie?sort_by=popularity.desc&include_adult=false&<params>`.
- `genres` has a fixed order: `action_adventure` (`with_genres=28|12`, `vote_count.gte=300`), `comedies` (`35`, `300`),
  `scifi_fantasy` (`878|14`, `300`), `horror` (`27`, `200`), `acclaimed` (`sort_by=vote_average.desc`,
  `vote_count.gte=5000`) (`tmdb.HomeGenreRows`). `params` lists the row's params without the defaults
  `sort_by=popularity.desc` and `include_adult=false` (the row's params win). Clients look rows up by `id`, ignore unknown
  ids and treat a missing id as `null`.
- A section is `null` when it failed or was not ready within 8 s; its fetch keeps running and fills the cache for the
  next call. Top 10 is `null` before the first download, and every section is `null` without a TMDB key. The answer is
  still 200.
- `Cache-Control: private, max-age=60` when no section is `null`, `no-store` otherwise. `Vary: X-Lang, Accept-Encoding`.
- A 404 or 405 means an older server: clients then call the endpoints above in parallel.

### More Like This

`GET /api/catalog/{movie|tv}/{id}/similar?profile_id=&limit=` (limit 1-40, default 24; the profile also from `X-User-ID`)
→ 200 `{results: [SimilarItem], total_results, media_type, media_id}`. The list comes from the matcher in
[ARCHITECTURE.md](ARCHITECTURE.md#more-like-this) (cached 24 h per title and `X-Lang`; the profile's dislikes are removed,
watched titles moved down and liked genres nudged up per request). An item is a SearchResult (`id`, `media_type`,
`title`, `original_title`, `overview`, `release_date` (TV: first air date), `poster_path`, `backdrop_path`,
`vote_average`, `vote_count`, `popularity`, `genre_ids`, `original_language`) plus `reason`: `collection` (another part
of the movie's collection, first, in release order), `spin_off` (same franchise, e.g. El Camino for Breaking Bad) or
`match`. A few items may be of the other media type. `debug=1` adds `score` and `why` (the score terms). When the
matcher fails and nothing is cached, TMDB's similar + recommended lists are returned (`reason` absent); 400 for a type
other than `movie`/`tv`.

`GET /api/tmdb/:id/related?type=movie|tv&limit=` (default 20, max 50) answers the same list (`year` is ignored); kept
for TV apps installed before the endpoint above.

```
GET /api/catalog/movie/240/similar?limit=4 → {"results":[{"id":238,"title":"The Godfather","reason":"collection",...},
  {"id":242,"title":"The Godfather Part III","reason":"collection",...},{"id":769,"title":"GoodFellas","reason":"match",...},...]}
```

### Search (ranking, did you mean, suggest)

- `GET /api/tmdb/search?q=` (default `type=multi`) → 200 `{page: 1, total_pages: 1, total_results, results: [SearchResult],
  did_you_mean?}`: every result on one page, ordered by our own score (see
  [ARCHITECTURE.md](ARCHITECTURE.md#search-ranking)); `page > 1` answers `[]`. `did_you_mean` (omitted when empty) is the
  corrected query whose results were merged in, e.g. `"The Mandalorian"` for `mandoloarien`. `type=movie|tv` still
  passes TMDB's own answer through (unranked). With `X-User-ID` the profile's genre taste lifts titles slightly.
- `GET /api/tmdb/suggestions?q=` → the 5 best ranked results (`{results, total_results, did_you_mean}`) for the navbar.
- `GET /api/catalog/suggest?q=&limit=` (default 8, max 15) → 200 `{query, ready, results: [{title, media_type, id,
  original_title?}]}`: title completions from the typo index only (no TMDB call, a few ms): titles starting with `q`
  (articles ignored), titles with a word starting with `q`, then typo-tolerant prefixes; one per title and per text.
  `title` is the matching name (a learned localized title only in its language, then `original_title` is set).
  `ready: false` until the index is loaded.

```
GET /api/tmdb/search?q=mandoloarien  → {"did_you_mean":"The Mandalorian","results":[{"id":82856,"title":"The Mandalorian",...},...]}
GET /api/catalog/suggest?q=thron     → {"results":[{"title":"Game of Thrones","media_type":"tv","id":1399},{"title":"Thronecast",...},...]}
```

### Discover (search filters)

`GET /api/catalog/discover?type=all|movie|tv&genres=16,878&anime=1&q=&page=` → 200
`{page, total_pages, total_results, results: [SearchResult], did_you_mean?}` (the normalized `/api/tmdb/search` shape: TV `name` /
`first_air_date` come as `title` / `release_date`, with `media_type`, `genre_ids`, `original_language`, `popularity`).

- `genres`: comma-separated TMDB movie genre ids (TV-only ids are accepted too), combined with **AND**. They are mapped per
  media type: Action and Adventure → Action & Adventure (10759), Sci-Fi and Fantasy → Sci-Fi & Fantasy (10765), War →
  War & Politics (10768), Thriller → Mystery (9648), Kids → Family for movies. History, Horror, Music, Romance and
  TV Movie have no TV genre and Reality / News / Soap / Talk no movie genre: a selection containing one of them only
  returns the other media type.
- `anime=1`: Animation (16) and `original_language` `ja`, combinable with genres.
- `type` (default `all`) limits the media type.
- Without `q`: a pool of TMDB `/discover/movie` and `/discover/tv` pages (popularity 1-5, vote count 1-3, rating 1-3 with
  `vote_count.gte=200`; no adult titles; each page cached 24 h per language) ranked by the browse score (see
  [ARCHITECTURE.md](ARCHITECTURE.md#browse-ranking); per profile with `X-User-ID`), 40 results per page for `type=all`, 20
  for one type. After the ranked pages, TMDB's popularity order continues from its page 6 without the pool's titles;
  `total_pages` = ranked pages + the rest of TMDB's (max 500). Infinite scroll asks `page=2, 3…`.
- With `q`: the ranked search of `q` (5 TMDB multi-search pages, cached 15 min per language, typo correction as in
  [Search](#search-ranking-did-you-mean-suggest)), filtered by the selection, as one page (`total_pages: 1`; `page > 1`
  answers `[]`).
- Titles without poster and backdrop are dropped. 502 `{error}` when TMDB fails.

```
GET /api/catalog/discover?genres=878&anime=1           (X-Lang: tr)
→ 200 {"page":1,"total_pages":70,"total_results":1877,"results":[{"id":65733,"title":"Doraemon","media_type":"tv",
        "genre_ids":[10759,16,35,10765,10762,10751],"original_language":"ja", ...}, ...]}
GET /api/catalog/discover?q=ghost&anime=1               → 10 results (Ghost in the Shell, Ghost Hound, ...)
GET /api/catalog/discover?type=tv&genres=27             → 200 {"page":1,"total_pages":0,"total_results":0,"results":[]}
```

### Availability

`GET /api/availability?media_type=movie|tv&tmdb_id=N[&season=S&episode=E]` → 200

```json
{ "status": "available" | "not_released" | "in_theaters" | "unknown",
  "release_date": "YYYY-MM-DD" | null, "theatrical_date": "YYYY-MM-DD" | null, "digital_date": "YYYY-MM-DD" | null,
  "digital_release_found"?: true }
```

400 `{error}` for a bad `media_type`, `tmdb_id`, `season` or `episode`. TMDB failures answer `unknown` (never a
block). Dates are compared with today's date in Europe/Istanbul.

- TV episode (`season` + `episode`, from the season details, cached 6 h): the episode's `air_date` after today →
  `not_released` with `release_date` = air date; on or before today → `available`. An episode without a date (or
  missing from the season) is `not_released` (`release_date: null`) while the season's `air_date` is in the future,
  else `unknown`. Only `season`: the season's `air_date`. Neither: the show's `first_air_date` (a show without one
  whose TMDB status is Planned / In Production / Pilot is `not_released`).
- Movie (TMDB `/movie/{id}/release_dates`, all regions, cached 6 h): `available` when any digital (4), physical (5)
  or TV (6) date is on or before today. Else `in_theaters` when a theatrical date (2 limited, 3) is on or before
  today: `digital_date` = `release_date` = the earliest announced future digital date (or null). Films in cinemas
  for over 120 days without an announced digital date, and films released in cinemas over a year ago, count as
  `available` (TMDB often lists no home release for them). Else `not_released` with `release_date` = the earliest
  upcoming theatrical date, else the earliest upcoming home release (null after a festival premiere (1) alone; a
  premiere older than 120 days without other dates is `available`). Without any release dates the movie's
  `release_date` decides: in the future → `not_released`, else `unknown`. `theatrical_date` is the earliest
  theatrical date, `digital_date` the earliest digital one.
- Movie home release found: TMDB's digital dates lag the real ones, so a movie TMDB lists as `in_theaters`, or
  `not_released` with a passed (or no) release date or a digital date ahead, is searched on the indexers first
  (like resolve, no TorrServer; the answer waits up to 4 s for it, the verdict is cached per film 6 h when found,
  1 h otherwise, see [ARCHITECTURE.md](ARCHITECTURE.md#films-in-cinemas-with-a-home-release-homereleasego)).
  When home releases (WEB-DL, WEBRip, BluRay, ...; never a cinema recording, bonus material or a namesake) of at
  least 2 release groups exist the answer is `available` with `digital_release_found: true` and `release_date` =
  the theatrical date; the other dates stay. Home releases whose probed length is wrong (resolve's checks, or the
  home check's own header probe of up to 3 TorBox-cached ones) turn it back: when none had the film's length and at
  least 2 had another one, the film is `in_theaters` again for 6 h and those releases never count again.
```
GET /api/availability?media_type=tv&tmdb_id=320754&season=1&episode=1      (Money Trap, on 2026-10-08)
→ 200 {"status":"not_released","release_date":"2026-10-16","theatrical_date":null,"digital_date":null}
GET /api/availability?media_type=movie&tmdb_id=1368337                     (The Odyssey, on 2026-10-09: "WEB-DLs" out)
→ 200 {"status":"available","release_date":"2026-07-15","theatrical_date":"2026-07-15","digital_date":"2026-11-17",
       "digital_release_found":true}
  ... seconds later, the TorBox-cached "WEB-DLs" probed as 86 min for 173 min:
→ 200 {"status":"in_theaters","release_date":"2026-11-17","theatrical_date":"2026-07-15","digital_date":"2026-11-17"}
GET /api/availability?media_type=movie&tmdb_id=27205                       (Inception)
→ 200 {"status":"available","release_date":"2010-12-03","theatrical_date":"2010-07-15","digital_date":"2020-08-13"}
```

## Providers (`modules/providers`, TMDB)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/providers/:id/tv` | TV shows on one streaming provider (TMDB discover, merged across US/IN/BD); `sort_by` (default `popularity.desc`; the merged list keeps that order, `vote_average.*` only counts shows with 200+ votes), `page` (pages may repeat shows of earlier ones). Used by the TV Shows page rows |

## Notifications (`modules/notifications`)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/notifications` | `{ notifications: [], count: 0 }` when Redis is unavailable. With `X-Lang: tr`, `tmdb_titles`, `media_details[].title/overview/genres` and the first title's `title`/`overview`/`genres` are Turkish (lookups slower than 3 s stay English for that answer) |
| GET | `/api/notifications/count` | |
| POST | `/api/notifications/:id/read` | |

## Streaming (`modules/streaming`)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/torrent-stream/resolve?type=movie&title=&year=` | auto-pick for the Watch button |
| GET | `/api/torrent-stream/resolve?type=tv&title=&season=&episode=` | → 200 `{magnet, hash, index, file_name, torrent_title, quality, seeders, size, hdr?, health?, duration?, duration_verified, cached?, format?, source?, debrid?, caps_fallback?, cinema?}`, 404 `{error: "No playable source found"}`, 503 `{error, code: "offline"}` when the server has no internet (answered at once, and instead of 404 / 502 when the connection dropped during the search; `reuse` answers the same); 425 `{error: "not_released", status: "not_released"\|"in_theaters", release_date, digital_date}` (dates `YYYY-MM-DD` or null) when `tmdb_id` names a title that is not out yet or only in cinemas with no home release on the indexers yet (see [Availability](#availability): a film in cinemas whose WEB-DL is out is `available` and resolves normally): nothing more is searched. `unknown` availability, a lookup slower than 4 s, a cached source or a resolve already running never answer 425. A resolve whose checks show that the home releases have the wrong length (the film is in cinemas after all) answers 425 `in_theaters` instead of 404. Can take up to ~60 s. `health` = `{rate, peak?, need, active_peers, seeders, keeps_up, dead?}` (bytes/s; `rate` is the rate delivered after the warm-up start burst, `peak` the best 3 s window for comparison). `quality` is the probed video size once the header was read (else the release name). `format` = `{audio?, channels?, atmos?, dv?, dv_only?, hdr10?, hdr10plus?, source?, truncated?, probed?, undecodable?, video?, subs?}` (see [Playback capabilities](#playback-capabilities); `probed` once ffprobe corrected it; `video`: the probed video codec; `subs`: ISO 639-1 languages of the text subtitles inside the file or next to it in the torrent, from the check). When nothing keeps up the answer comes about 12 s after the checks started (up to 16 s while nothing is measured yet) with the most playable source so far (`health.keeps_up: false`); every alternative keeps measuring for `sidegrade`. `caps_fallback`: why the file breaks the client's caps (taller than `maxq`, or video play re-encodes for it, e.g. `2160p above the 720p cap, hevc yuv420p10le video is re-encoded`); only set when no fitting file was found (see [Playback capabilities](#playback-capabilities)). Optional: `tmdb_id`, `runtime` (minutes), `hevc=1`, `hdr=1`, `dv=1`, `atmos=1`, `acodecs=`, `maxq=1080\|720`, `refresh=1` (skip the cache, avoid the cached torrent), `avoid=<hash>,<hash>` (up to 5 torrents that must not be picked again, ranked last; a cached source among them is dropped and the title searched again), `t=<s>` (where playback starts: a cached source whose swarm was not measured in the last 3 minutes is measured there first, ≤ 8 s, and one that does not keep up is dropped and the title resolved fresh without it; a TorBox source with TorBox usable is never measured; see [ARCHITECTURE.md](ARCHITECTURE.md#streaming)), `cinema=1` (a cinema recording of a film that is only in cinemas: the `in_theaters` 425 is skipped, recordings such as TELESYNC / HDTS / CAM are ranked, best capture first, the duration check still applies; own cache key; the answer then carries `cinema: true`; send it on prefetch, sidegrade, upgrade, subalt and release of that playback too, see [ARCHITECTURE.md](ARCHITECTURE.md#cinema-recordings-on-request-cinemago)), `cached=1` (only answer from the cache, never search), `dry=1` (search and rank only: 200 `{title, year, runtime, results, usable, took_ms, candidates: [{title, seeders, size, quality, source, score, parts}]}` with the ranked candidates (up to 100); no TorrServer, TorBox, source cache or hold; the same 425 gate) |
| GET | `/api/torrent-stream/reuse?<same query as resolve>&hash=&index=[&magnet=][&t=<s>]` | How a saved source (Continue Watching, a resumed episode: the `hash` / `index` / `magnet` stored with the progress, `t` the resume position) plays → 200 `{action: "play", source: <resolve result>}` or `{action: "resolve", reason}`. TorBox on and the hash cached: `source` is the TorBox file (`debrid: true`, `source: "torbox"`, `index` = TorBox's), registered and its link fetched. Otherwise the torrent is added and its swarm measured at `t` (≤ 8 s): keeps up or unmeasured → `play` with `health`; starving, dead or no metadata → `resolve` (call resolve with that hash in `avoid`). A saved hash picked by a step down (`maxq`) gives way to the title's uncapped cached source. Holds the title like resolve. 400 for an invalid hash / index or resolve query. See [ARCHITECTURE.md](ARCHITECTURE.md#saved-sources-reusego) |
| POST | `/api/torrent-stream/prefetch?<same query as resolve>` | 202 `{status: started|ready|running|busy|failed_recently|early|not_released|deferred}`; the intent to watch: starts the resolve job in the background and holds the title warm (a cached source is re-added to TorrServer and its header read). While another title plays (playback priority, `docs/ARCHITECTURE.md`) a detail-page prefetch answers `deferred`: the title is held but nothing is searched or loaded until the last stream ends, then it starts by itself; a next-episode prefetch (`next_pos`) of the playing show is never deferred.|ready\|running\|busy\|failed_recently\|early\|not_released}`; the intent to watch: starts the resolve job in the background and holds the title warm (a cached source is re-added to TorrServer and its header read). Optional `next_pos=&next_dur=` (seconds of the current episode): a next-episode prefetch only runs once 10 min are left (85 % for episodes under 25 min), else `early`. A title that is not out yet (resolve would answer 425) gets `{status: "not_released", availability: "not_released"\|"in_theaters", release_date, digital_date}` and no job is started |
| POST | `/api/torrent-stream/release?<resolve query>[&delay=<s>]` / `?hash=<h>,<h>` | 202 `{status: released\|scheduled, titles?, hashes?}`; drops a title's warmed torrents from TorrServer (every capability variant of that title / episode) and cancels its background work (resolve job unless a player waits for it, upgrade warm-up, late indexer results). `delay` (≤ 120 s) waits first; any prefetch, resolve or play of the title cancels it. `hash` (≤ 10) drops torrents a closing player used; `why=<reason>` (with `hash`) is logged: why the player left them (a switch to them failed, or how the upgrade that replaced them went). Playing torrents and ones another title holds are never dropped: no torrent a player reads (a `play` stream, `probe` or `seekpoint`), or whose `play` stream ended less than 60 s ago, is dropped by any path (the drop is retried once it is free). A title release while a player reads one of the title's torrents (any capability variant) is checked again 5 s later and ignored if the player still reads it; a `hash` that a player reads is ignored. Made for `navigator.sendBeacon`; 400 without a title or hash |
| GET | `/api/torrent-stream/sidegrade?<same query as resolve>&current=<hash>&current_q=[&draining=1][&critical=1][&avoid=<hash>,<hash>]` | 200 resolve result of a different torrent of at least `current_q` that keeps up now (measured within 45 s; a spare the resolve kept warm) and, when `current`'s last measurement kept up too, delivers ≥ 1.5× its rate / need; while `current` starves (or with `critical=1`) also one of a lower resolution that keeps up (the playing resolution or higher first). A spare that does not keep up is never offered. 204 otherwise (then it measures the spares in the background). At most one per title every 2 minutes; spares exist only until 60 s after the pick without playback or 3 minutes into playback (or after their arrival / the last `draining=1` poll, whichever is later). `draining=1`: the player's buffer drains; when no spare is left to measure, the server searches for alternatives in the background (at most every 3 min per title, capped at the playing resolution, without `current` and `avoid`; its checks wait while a stream starts) and a pick that finished good (never the search's "best so far" cap answer) is offered on a later poll under the same rules. `critical=1` (buffer under 6 s, implies `draining`): a lower resolution that keeps up is fine too. `avoid=` (≤ 5): torrents the player found slow, never offered (spares among them are dropped). 400 for an invalid `current` |
| GET | `/api/torrent-stream/subalt?<same query as resolve>&current=<hash>[&current_q=][&lang=tr][&avoid=]` | 200 resolve result (with `format.subs`) of another release of the title whose subtitles include `lang` (default: the profile's, `X-Lang`): checked for the same client caps, kept up when measured (within 30 min), of a fitting length, of the playing resolution, a higher one or one step lower (not below 720p unless playing below; no 4K without `hevc=1`; `maxq` holds). 204 otherwise; then the server scans up to 3 untried candidates' headers in the background (one scan server-wide, at most every 10 min per title) and answers 204 with `Retry-After: 30` while it runs. The player calls it when the playing file has no subtitle in the profile's language; the cached source is not changed. 400 for an invalid `current` |
| GET | `/api/torrent-stream/upgrade?<same query as resolve>&current=<hash>&current_q=<2160\|1080\|720\|480>` | 200 resolve result of a higher resolution than the playing `current` once its swarm keeps up (delivered rate ≥ 1.3× its bitrate from a finished measurement, duration verified; resolutions are the probed ones, and for the cached source the server's probed resolution wins over `current_q`); 204 otherwise (also for requests without `hevc=1`, with `maxq`, or `current_q=2160`). Without a running background warm-up it starts one upgrade search for the title (at most every 10 min); `failed=<reason>`: the player's last switch to an offered upgrade failed (logged; it tries once more); 400 for an invalid `current` |
| POST | `/api/torrent-stream/instant` | body `{items: [{type: "movie"\|"tv", tmdb_id, title?, year?, season?, episode?}]}` (≤ 100, invalid items dropped) → 200 `{enabled, results: {"movie:123": true\|false\|null}}`: whether the title has a release TorBox has cached (the "⚡" card chip). Answers at once from memory; `null` = unknown (being checked, or the check failed: asked again after 10 min); titles without a fresh answer are queued for the background check. With TorBox off `enabled: false` and every answer `null`, nothing is queued. TV shows are checked by `season` / `episode` (default S01E01). See [ARCHITECTURE.md](ARCHITECTURE.md#instant-play-hints) |
| GET | `/api/torrent-stream/files?magnet=` | `{hash, name, files: [{id, name, path, size}]}`; adds the torrent to TorrServer, except a TorBox source, whose known files are listed without TorrServer |
| GET | `/api/torrent-stream/probe?hash=&index=` | `{duration, video_codec, width, height, hdr, dolby_vision, direct, audio_tracks: [{index, language, title, codec, channels, default, direct, split_direct}], split, expected_runtime?, duration_mismatch?}`; `direct` = sent untouched to this client (takes `hevc`, `acodecs` like play); `split_direct` = sent untouched in a `track=audio` stream (`acodecs` plus `split_acodecs`); `split: true` = play takes `track=` (clients detect split support from it); `default` = the track's default disposition in the file. With `type` and `tmdb_id` (plus `season`, `episode`; or `runtime` minutes) it adds `expected_runtime` (minutes) and `duration_mismatch` (same rule as resolve) |
| GET | `/api/torrent-stream/play?hash=&index=&t=<sec>&audio=<n>&acodecs=&hevc=1` | live fragmented MP4; seeking = new request with a new `t`. Audio tracks whose codec is in `acodecs` and fits MP4 (aac, opus, flac, mp3, ac3, eac3) are copied; others are re-encoded to Opus (when `acodecs` has `opus`) or AAC at 48 kHz, 5.1 for multichannel sources (7.1 → 5.1, 384 kbps), stereo 160 kbps (Opus) / 256 kbps (AAC). Without `acodecs` only AAC is copied. `track=video` / `track=audio` (split mode, see [Split streams](#split-streams)): only that elementary stream (`audio/mp4` for audio), on the file's own timeline; also copies the `split_acodecs` codecs; 400 for another `track`. `pace=1` (players that fetch the stream themselves, Safari): the input is read at playback speed after a 30 s burst (`-readrate 1 -readrate_initial_burst 30`), since WebKit keeps downloading whatever the server sends |
| GET | `/api/torrent-stream/seekpoint?hash=&index=&t=&audio=&acodecs=&hevc=1` | `{t, exact?}`: where a play stream requested at `t` really starts, in file time (from the file's start time, like `play`). Muxed (no `track`): the file time of the stream's time 0, so position = `t` + the element's time; streams with copied video or audio start on the keyframe before `t` (found with play's own seek), and time 0 is the first decode time, up to two frames before that keyframe shows; also answered for `t=0` (then slightly negative, ≥ -1). With `track=`: where the split video starts (the keyframe). `exact: false`: unknown, e.g. ffmpeg timed out or an MPEG-TS seek landed after `t`; the TV app asks again (`track=` and `split_acodecs` as in play: a split pair copies the split-only codecs too). See [ARCHITECTURE.md](ARCHITECTURE.md#playback-timeline-playgo-seekpointgo) |
| POST | `/api/torrent-stream/keepalive?hash=&index=` | 204; a player paused by the viewer sends it every 15 s. The torrent counts as playing for the stream guard and playback priority (as if its `play` stream had just ended), its title's 15 min hold is renewed, and a TorBox file keeps a fresh link and a live CDN connection (a one-byte read, at most every 20 s), so the restart after a long pause starts at once. Made for `navigator.sendBeacon`; 400 for an invalid hash |
| GET | `/api/torrent-stream/thumb?hash=&index=&t=<sec>[&w=<px>]` | Trick play frame: `image/jpeg` of the keyframe at or before `t` snapped down to the 10 s grid (`X-Thumb-T` = the grid time), `w` (64-1280, default 320) rounded up to 160 / 240 / 320 / 480 / 640, HDR tone-mapped to SDR. 200 with `Cache-Control: public, max-age=31536000, immutable` and `X-Cache: HIT\|MISS`. 204 + `Retry-After: 2` (`X-Thumb-Wait` says why) when the file's pieces must not be read now: its stream is starting (probe running, or the newest play stream has not sent its first bytes), or another torrent plays or starts; cached frames are always served. 504 + `Retry-After` when the frame is not ready within 6 s (the job goes on up to 15 s, so a retry is usually a hit), 404 when ffmpeg finds no frame there (remembered 1 min), 400 for an invalid hash / index / `t` (not 0-86400, or past the probed end) / `w`. See [ARCHITECTURE.md](ARCHITECTURE.md#trick-play-thumbnails) |
| POST | `/api/torrent-stream/thumb/prefetch?hash=&index=&t=<sec>,<sec>…[&w=]` | 202 `{queued, cached}` or `{queued: 0, skipped: <why>}`; makes up to 12 frames (grid times) in the background, one at a time and only while no viewer waits for a frame. Only while the torrent's play stream runs steadily (flowing, older than 30 s, no other torrent starting); otherwise nothing is queued. 400 for invalid times or more than 12 |
| GET | `/api/settings/indexer` | 200 `{id, jackett_url, api_key_set, min_seeders, preferred_quality, bitsearch_enabled, created_at, updated_at}`; the API key is never returned |
| PUT | `/api/settings/indexer` | any of `{jackett_url, jackett_api_key, min_seeders, preferred_quality, bitsearch_enabled}`; for `jackett_api_key` an empty string (or a missing field) keeps the stored key, `null` clears it → 200 like GET; 400 on invalid values |
| POST | `/api/settings/indexer/test` | optional body `{jackett_url, jackett_api_key}` (empty fields fall back to the saved settings, then env) → 200 `{success: true, message, jackett_url, test_results}`; 400 when nothing is configured, 503 when Jackett fails |
| GET | `/api/settings/debrid` | 200 `{torbox: {api_key_set, enabled, prefer, add_uncached}, updated_at}`; the key is never returned. Defaults: `enabled` and `prefer` on, `add_uncached` off |
| PUT | `/api/settings/debrid` | `{torbox: {api_key?, enabled?, prefer?, add_uncached?}}`; for `api_key` an empty string (or a missing field) keeps the stored key, `null` clears it → 200 like GET; 400 for an invalid key (spaces, > 200 chars) |
| POST | `/api/settings/debrid/test` | optional body `{torbox: {api_key}}` (empty: the stored key) → TorBox `user/me`: 200 `{success: true, plan, plan_name, premium_expires_at, active}` (`active`: a paid plan that has not expired); 400 without a key, 401 when TorBox rejects it, 503 when TorBox is unreachable |

The indexer settings endpoints replace the old `/api/torrents/config` and `/api/torrents/test-connection`.

TorBox (debrid): with a key set and TorBox enabled, resolve asks TorBox which of the best 40 candidates it has
cached and, with `prefer`, ranks a cached release first within its resolution. A cached pick answers with
`source: "torbox"` and `debrid: true`; its `health` is a short read of the TorBox link (`active_peers` 0). `probe`,
`play`, `seekpoint`, `thumb` and the subtitle endpoints take the same `hash` / `index` and read the file from TorBox
on the server (index = the file's place in TorBox's list + 1); when TorBox fails (down, 429, unauthorized, expired
plan) the same calls play it from TorrServer. Uncached releases are only sent to TorBox with `add_uncached`. See
[ARCHITECTURE.md](ARCHITECTURE.md#debrid-torbox).

### Playback capabilities

A client declares what it can play with the same query parameters on every streaming call of a title (`resolve`,
`prefetch`, `release`, `sidegrade`, `upgrade`, and `probe` / `seekpoint` / `play`). Resolve ranks for them and they are
part of the source cache key, so a TV and a PC never share a cached pick; send the same set on every call or the
calls address different cache entries.

| Parameter | Meaning | Used by |
|---|---|---|
| `hevc=1` | decodes HEVC Main / Main10 (4K HEVC is copied, not re-encoded) | ranking, key, probe/play |
| `hdr=1` | HDR display (only with `hevc=1`); without it HDR is tone-mapped by the display path and ranks below SDR | ranking, key |
| `dv=1` | shows Dolby Vision itself, profile 5 included (only with `hevc=1`): DV ranks first, DV-only releases are fine | ranking, key |
| `atmos=1` | passes Atmos (E-AC-3 JOC / TrueHD) through to the speakers; only counts together with `eac3` in `acodecs` | ranking, key |
| `acodecs=` | comma list of audio codecs the client decodes: `aac`, `opus`, `flac`, `mp3`, `ac3`, `eac3`, `truehd`, `dts` (ffprobe names; unknown names are ignored) | ranking (with `atmos=1`; `ac3`, `truehd`, `dts` are part of the key), probe/play |
| `split_acodecs=` | audio codecs the client plays only on their own, in an audio-only SourceBuffer (an LG TV: `ac3,eac3`); copied only into `track=audio` streams, never into a muxed one. Resolve-family calls declare them in `acodecs` instead (ranking, key) | probe/play/seekpoint |

The caps hold for the probed file, not only its name: a file taller than `maxq`, or whose video `play` has to
re-encode for the client (HEVC without `hevc=1`, AV1, VP9, MPEG-4, Hi10P / 4:4:4), does not fit. Resolve prefers
any fitting file and checks up to 4 more candidates before it plays one that does not fit; then it takes the
cheapest (copied above the cap before re-encoded, then the lowest resolution) and says why in `caps_fallback`.
For a capped client (`maxq`, or no `hevc=1`) a release name without a resolution tag ranks after every labeled one
until probed. A cached source that breaks the caps is resolved again (a caps fallback is reused for 6 h). `cinema=1`
(a cinema recording, see resolve) is part of the source cache key as well (`…|cinema`).

Ranking within one resolution (resolution, source tier and the measured swarm health always come first):

- Native audio (`atmos=1` and `eac3` in `acodecs`): Atmos (DD+ Atmos; TrueHD Atmos only with `truehd`; DTS:X only with
  `dts`) > 7.1 > 5.1 > stereo. A multichannel codec the client did not declare ranks a bit below a declared 5.1 (the
  server would re-encode it).
- Otherwise (browsers): multichannel (5.1 / 7.1, any codec, Atmos included) > stereo; the play stream re-encodes it
  to Opus / AAC 5.1.
- HDR with `dv=1`: DV > HDR10+ > HDR10 > SDR. Without `dv=1`: HDR10 / HDR10+ / DV with an HDR10 layer > SDR on an HDR
  display; DV-only releases (profile 5) are ranked far down and, once ffprobe shows profile 5, only picked when
  nothing else is left.
- Higher bitrate (size per minute) up to about twice the typical one of the resolution; more seeders count fully up
  to 30, barely above.

Key examples: a PC browser `…|hevc|hdr`; a DV / Atmos TV `…|hevc|hdr|dv|atmos+ac3` (`+truehd`, `+dts` when declared).
`play` copies `aac`, `opus`, `flac`, `mp3`, `ac3` and `eac3` into its fragmented MP4 when `acodecs` declares them
(AC-3 / E-AC-3 with `delay_moov`, so the init segment comes with the first fragment); E-AC-3 Atmos (JOC) survives
the copy. TrueHD and DTS are always re-encoded there (5.1 at most).

### Split streams

`play?track=video` and `play?track=audio` deliver one elementary stream each, so a client can feed them into two
SourceBuffers of one MediaSource (an LG TV with Chrome 120 takes E-AC-3 / Atmos only in an audio-only SourceBuffer;
`video/mp4; codecs="avc1…,ec-3"` is refused). Both requests of a pair carry the same `t`, `audio`, `hevc`, `acodecs`
and `split_acodecs`, so both ffmpeg runs seek the same way (`-ss t`, `-noaccurate_seek` when video or audio is copied):
copied video starts on the keyframe at or before `t` (what `seekpoint` with `track=video` answers), audio with the
first audio frame from that point (measured within one video frame of it). Unlike the muxed stream, which starts at 0
(= the seek point, so players add the `seekpoint` offset), split streams carry the file's own timestamps: stream time
= file time (from the file's start), `-output_ts_offset t` undoes the input offset and `frag_discont` +
`negative_cts_offsets` without edit lists keep `tfdt` absolute. A client appends both as they are, needs no offset or
`seekpoint`, and seeks the element to `t` once data is in. Chapters are dropped from split streams.

Search: resolve asks each configured Jackett indexer that carries the media type (`/api/v2.0/indexers/<id>/results`)
and ranks as soon as enough is there: once every indexer it waits for answered; once the indexers that usually answer
within 1 s (moving average per indexer, kept in memory; unmeasured ones count as fast) answered, or 1.2 s passed, and
3 usable, title-matched candidates of the top wanted resolution arrived (2160p for HEVC clients without `maxq`, else
1080p or the cap); once the fast ones answered and 1.2 s passed (or 1.5 s passed) with 3 of 1080p or more, 4K
included (4K clients); at the latest after 3.5 s. These times count from the start of the resolve: the title is
searched at once, while the TMDB / IMDb lookups below run next to it and add their queries when they answer.
Slower ones (1337x) are merged when they finish (≤ 25 s) and can still improve the cached source or feed `upgrade`.
The log line `N results in Xs (<reason>)` says which rule cut. A resolve answers 502 `Torrent search
failed. Check the Jackett settings.` only when every indexer request failed and Bitsearch found nothing either.

Bitsearch (`bitsearch_enabled`, on by default): the search page of bitsearch.to (a DHT index, `BITSEARCH_URL`; it
redirects to bitsearch.eu, which is remembered) is scraped next to Jackett as one more source of the same search
(same cut-off rules, its answer time averaged like an indexer's); what it finds later joins the late results. It finds torrents no Jackett indexer lists (single files such as
`Kardeşler.Araştırma.2026.mp4`). Each name is searched once in folded form (`kardesler arastirma`), movies with a
year only as `<name> <year>`, TV with the `SxxEyy` queries first, then the absolute and bare ones (a season query
is dropped when its bare name is searched), at most 4 queries per search round. Results outside video categories (Movies, TV, Anime,
Other/Video, Other) are dropped; the site's own category filter is not used because DHT finds are filed as
Other/Video. The magnet is built from the info hash with the page's trackers plus well-known open trackers. At most
one request per second, results cached 30 min per query, concurrent identical queries share one request; a
Cloudflare challenge goes through FlareSolverr (`FLARESOLVERR_URL`), any other failure pauses Bitsearch for 2 min
(logged) and the search goes on with Jackett alone.

IMDb: with `tmdb_id`, resolve looks up the IMDb id (TMDB external ids) and IMDb's English title
(`https://v3.sg.media-imdb.com/suggestion/x/<id>.json`, cached 7 days, failures 10 min, never waited for more than
2 s). The title joins the other names below ("Detective Brothers" on TMDB is "Brothers Investigation" on IMDb). For
movies, every indexer whose torznab caps list `imdbid` for movie search is also asked with
`t=movie&imdbid=<id>` once the id is known (part of the same per-indexer deadline); items that name another IMDb id
are dropped. These
results are accepted even when their name matches none of the known titles (the title part of the release name
before its year is used for ranking).

Other titles: with `tmdb_id`, resolve also looks up the original title, original language and alternative titles
(TMDB, ≤ 2.5 s, cached). Releases of foreign titles usually carry the original title in ASCII ("Money Trap" is
"Organize İşler: Karun Hazinesi" on TMDB, released as "Organize.Isler..."). For a non-English original the search
also runs the original title, its ASCII transliteration (ş→s, ğ→g, ı/İ→i/I, ç→c, ö→o, ü→u, other accents dropped),
for TV the part before a colon, IMDb's English title, and up to 2 alternative titles (US, GB, TR first; non-Latin
ones skipped), at most 4 extra names, each with the same queries as the title, added to the running search as soon
as the lookup answers, within the same per-indexer deadline (each query's results count as soon as they arrive; ones
that answer after the cut join the late results). The search does not end on "every indexer answered" before these
queries ran, and ranking waits for the other names when the title alone found fewer than 5 usable results. A name that starts with an already searched
one is matched but not searched. For an English original the other names (IMDb title, alternative titles) are
searched only when the title found fewer than 5 usable results. Release names are matched against the title and
every other name, normalized the same way (`Organize İşler` matches `Organize.Isler.S01E01.1080p`); duplicates are
merged as usual.

Episodes: TV is searched as `<name> SxxEyy` and `<name> Sxx`. Foreign releases often use other notations, so for a
non-English original the other names also add the bare original name (ASCII, at most 2) and, from season 2 on,
`<name> <absolute episode>` ("Cukur", "Cukur 93"); an English original gets the bare title only when the first round
found fewer than 5 usable results. With `tmdb_id` the show's season list (`episode_count` per season, specials left
out, cached) gives the absolute episode number (Çukur S4E1 = 33 + 34 + 25 + 1 = 93); without it, release names that
carry both numbers ("Cukur 94 S4 E1") decide. Release and file names are read with these notations: `S01E05`,
`S1 E5`, `1x05`, `Season 1 Episode 5`, `Sezon 1 Bölüm 5`, `2. Sezon 5. Bölüm`, `S01.Bolum.05`, `01.sez`; `E05`, `B05`,
`BL.90`, `Bölüm 5`, `5. Bölüm`, `ep.1`, `Episode 1`, `Episodes 01-107`; a bare number right after the title is the
absolute number. Years, resolutions (720, 1080, ...), codecs (264, 265) and channel counts (5.1) are not episode
numbers. A name with season and episode matches by them; a number without a season matches the absolute number;
episode numbers past their season's length that fit its absolute numbers are absolute ("Season: 1 / Episodes:
01-107 of 107" is the whole show and accepted as a complete pack, up to 300 GB). In a pack the file is picked by
season and episode (the file's name, its folder "Season 2/", or the pack's season), then by its absolute number.

Resolve checks each candidate before answering: TorrServer must open it, its swarm must keep up with the file's
bitrate, and its duration (ffprobe of the header, cached for play) must fit the expected runtime: movies within
max(10 min, 12 %), episodes within max(8 min, 25 %); releases tagged extended / director's cut / uncut may run up to
40 % longer. The expected runtime comes from TMDB when `tmdb_id` is given (movie runtime, episode runtime from the
season, else the show's `episode_run_time`; cached in memory), else from `runtime`. When either value is unknown
the file is not rejected. `tmdb_id` and `runtime` are not part of the source cache key.

A candidate whose duration was verified beats every candidate of unknown duration (the probe failed), whatever its
resolution; one of unknown duration is only picked when no verified one exists (`duration_verified: false`). A probe
still running after 12 s goes on (up to 25 s more) as soon as the swarm shows peers or data; one that failed early is
retried once. One ffprobe runs per file: the player's `probe` (and `play`) waits for a running one instead of
starting a second. The player
probes again before playing; on `duration_mismatch` it resolves with `refresh=1&avoid=<hash>` and shows "Finding a
complete version…", at most twice per title.

Swarm health is measured after a warm-up, not cold: the server reads the middle of the file and polls TorrServer's
stat every second, for up to 15 s for the highest resolution on offer (8 s for lower ones), starting once the header
was probed (at the latest 2.5 s after the file was picked) so the two reads do not compete. The verdict is the median
of the per-second rates (read rate or TorrServer's download speed, whichever is higher) after the first 1.5 s, over
the last 6 s, so a start burst or one spike never makes a weak swarm look healthy; it keeps up at ≥ 1.3× the file's
average bitrate (size / runtime) and stops early once that median covers 3 s; no peers and no data for 8 s means dead.
A candidate that keeps up with a verified duration (or an unknown runtime) answers at once; resolve waits (3 s, cut
short when the promise fades) only while a better-ranked check of at least its resolution delivers ≥ 0.8 of its need
so far. A higher resolution still warming (HEVC clients without `maxq`) keeps warming in the background (up to 2
candidates, 90 s from the start of the checks).
Once it keeps up it replaces the cached source, so `upgrade` offers it and the next play starts with it.

Resolve answers at the latest 12 s after the checks started (a hard wall, also for a grace) with the best so far
(verified duration first, then the highest rate relative to its need; a check whose duration is known counts with its
health so far) instead of waiting for every check; a candidate with active peers or data always beats a dead or silent one. When
no check has a verdict or a checked duration yet (slow
metadata, a slow probe), a check's snapshot from before its verdict answers: the highest rate so far for its need,
else the most active peers, then the most seeders (`health`, when sampled, shows the rate so far with `keeps_up: false`;
`duration_verified` is false). Only without any candidate's metadata does it wait for the first one (≤ 20 s).
The others keep measuring: one of the same resolution that keeps up, or is clearly faster
(≥ 1.5× the pick's rate), becomes a spare for `sidegrade` and the cached source; a higher one feeds `upgrade`. The
player asks `sidegrade` 8 s after such a start (also before the first frame when it started at 0), then every 15 s,
and whenever its buffer drains during playback (every 5-10 s; see the proactive sidegrade in ARCHITECTURE.md).

Once resolve has answered, its background work (checks still running, upgrade warm-ups and searches, spare
measurements, late candidates, sidegrade searches, their probes) waits while another torrent starts: the player's
`probe` of it runs, or its play stream is younger than 30 s (up to 90 s while it has sent no bytes yet), so the
starting stream (or a seek) has the line to itself; a running measurement that already has a verdict ends with it.
Prefetch on the detail page and the checks before the answer never wait.

Quality: an explicit resolution in a release name (2160p, 4K, 1080p, 720p, 480p) wins over UHD-ish words, and once
ffprobe read a candidate's header its quality follows the video size (height ≥ 1600 or width ≥ 3200 → 2160p, ≥ 900 /
≥ 1800 → 1080p, ≥ 600 / ≥ 1200 → 720p, else 480p), for the answer, the cached source and the upgrade decisions.
Source cache keys are versioned (`v10`): picks of older rankings (v9: language-blind, v8: format-blind) are resolved again.
The subtitle part (below) did not bump it: an older pick still plays, and `subalt` covers one without the subtitle.

Audio language: resolve reads the profile language from `X-Lang` (web and TV send it on every call). Release names
with dub-only markers (`MVO`, `Dub`, `Дубляж`, `Rus`, `iTA` without `ENG`, `TRUEFRENCH`, ...) rank slightly lower,
`MULTi` / `Dual Audio` / `ENG` slightly higher. A checked file whose audio tracks are all tagged with languages that
are neither the title's original language (TMDB) nor the profile's is taken only when no other candidate is left
(see ARCHITECTURE.md). The language is not part of the cache key.

Subtitle language: for the profile language (not the original one) release names that name its subtitle (`TR Sub`,
`Türkçe Altyazı`, `Subs: ENG, TUR`, for Turkish also `TUR` / `Turkish`) rank slightly higher (+6), `MULTi-SUB` /
`Multi Subs` a little (+4); a checked file with a text subtitle track or a subtitle file of that language gets +8, one
without it loses the name's hint. These parts stay below a resolution step and never beat the swarm or duration checks.

## Skip segments (`modules/segments`)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/skip-segments?hash=&index=&tmdb_id=&season=&episode=[&duration=<s>]` | 200 `{intro: {start, end, source} \| null, credits: {start, end, source} \| null, duration}` (seconds; `credits.end` is the file's length when the credits run to the end; `source`: `chapters`, `theintrodb`, `introdb` or `theintrodb+introdb`). Named chapters of the probed file (`hash` / `index`) come first; what they lack comes from TheIntroDB / IntroDB for the episode (`tmdb_id` of the show, `season`, `episode`), cached in `skip_segments`. `duration` (the player's length) is used when the file cannot be probed. Segments that break the rules (see [ARCHITECTURE.md](ARCHITECTURE.md#skip-intro-and-next-episode)) are left out. 400 without a file or an episode |

## Subtitles (`modules/subtitles`)

Sub: `{ "id": "<opaque>", "source": "torrent|embedded|opensubtitles|subdl|subsource|turkcealtyazi|subf2m|yifysubtitles|ai", "language": "tr",
"label": "Türkçe", "release": "BDRip HDRINVASION", "hi": false, "forced": false, "synced": false, "score": 0.3,
"author": "WoraZ", "fps": 23.976, "downloads": 6527, "live": true, "quota": true }` (`author`, `fps`, `downloads` only when known; `live`: an
embedded track of a big file, read while it plays, see below; `quota`: an OpenSubtitles item while its download quota is
used up, listed after the other items of its language; `ai_from`: the language an `ai` item is translated from, see
[AI translation](#ai-translation); `holes`: lines a reference of the playing file has where this subtitle has none, see
[Holes](#holes))

| Method | Path | Notes |
|---|---|---|
| GET | `/api/subtitles` | `tmdb_id`, `media_type=movie\|tv`, `season`, `episode` (required for tv), `hash` + `index` (playing TorrServer file), `langs=tr,en` (default: the settings order), `title` (playing torrent name, ranking hint), `year`, `ui_lang` (the profile's language, the AI translation target `profile`), `refresh=1` (late uploads: ask the providers again, skipping the 30 min list cache and the 24 h page cache of the scraped sites (pages older than 10 min); at most every 5 min per title, otherwise the cache answers) → 200 `{items: [Sub], opensubtitles_quota_until?: "<RFC 3339>"}` (the field only while the OpenSubtitles download quota is used up); 400 without `tmdb_id` and `hash` |
| GET | `/api/subtitles/file?id=[&t=<sec>]` | 200 `text/vtt; charset=utf-8`; a subtitle listed for a playing file (not made for it) is timed to that file (see [ARCHITECTURE.md](ARCHITECTURE.md#automatic-subtitle-timing-modulessubtitlessyncgo)): while that runs the answer is the subtitle as it is, marked partial like a live track (ask again), then the timed one; while the file plays, timing from the played audio keeps improving it, and such answers stay partial too (players keep asking every 15 s and replace the cues). Every 200 answer carries `X-Subtitle-Sync` (exposed to CORS, values below); for a `live` embedded track the cues read so far from about `t` (the player's position) on, marked by a `NOTE nextflix-partial` line and `X-Subtitle-Partial: 1` (`no-store`) while more follow (ask again; the whole track is cached once every part of the file was read); 400 unknown id, 409 `{error, ai: true}` for an `ai` item while the AI translation is off, 429 provider quota or rate limit (OpenSubtitles quota: `{error, quota: true, reset_at: "<RFC 3339>"}`, answered without asking OpenSubtitles until then), 502 provider failure (`{error, ai: true}`: the Claude key or account cannot be used) |
| GET | `/api/settings/subtitles` | 200 `{opensubtitles: {api_key_set, username, password_set}, subdl: {api_key_set}, subsource: {api_key_set}, turkcealtyazi: {enabled}, subf2m: {enabled}, yifysubtitles: {enabled}, ai_translate: {enabled, api_key_set, langs: ["profile","de"], languages: [{code, name, native}], model, usage: {month, lines, input_tokens, output_tokens, cost_usd}}, default_langs: ["tr","en"]}`; secrets are never returned |
| PUT | `/api/settings/subtitles` | any of `{opensubtitles: {api_key, username, password}, subdl: {api_key}, subsource: {api_key}, turkcealtyazi: {enabled}, subf2m: {enabled}, yifysubtitles: {enabled}, ai_translate: {enabled, api_key, langs}, default_langs}`; an empty string keeps the stored value, `null` clears it → 200 like GET |
| POST | `/api/settings/subtitles/ai-test` | `{api_key?}` (empty: the stored key) → 200 `{ok: true}` or `{ok: false, error}`; one tiny Claude request |

`X-Subtitle-Sync` on `/api/subtitles/file` (one token, `[a-z]`):

| Value | Meaning | Partial? |
|---|---|---|
| `original` | not timed: made for the file (torrent file, embedded track, OpenSubtitles hash match) or no file named in the id | as the track (live embedded tracks are) |
| `pending` | not timed yet (no reference yet, or nothing fits so far): served as it is | yes while the timing runs or the file plays |
| `live` | timed (or replaced by a fitting subtitle) from what was read and played so far; changes as more plays | yes while the file plays, then `no-store` without the marker |
| `synced` | timed to fit the file, final | no |
| `unchanged` | already fitted, served as it is, final | no |
| `replaced` | did not fit: the best fitting other subtitle of its language is served, final | no |
| `poor` | did not fit, nothing better: served as it is, final | no |
| `failed` | no reference (file unreadable, or too dear to read and not playing): served as it is, tried again later (`no-store`) | no |

`pending`, `live` and `failed` answers are `no-store`; the final ones may be cached (`max-age=86400`). A partial
answer has the `NOTE nextflix-partial` line and `X-Subtitle-Partial: 1`; ask again every 15 s and take the new cues.
A subtitle with holes also carries `X-Subtitle-Holes` (exposed to CORS): `2 holes, 40 lines` (`, 38 filled` with the
AI translation on), see [Holes](#holes).

Sources: subtitle files inside the torrent and embedded text tracks (whole when the file is small or mostly cached, else
live: read from the playing position on while it streams, see ARCHITECTURE.md), OpenSubtitles.com
(API key; username/password log in for a JWT cached 23 h, which raises the download quota), SubDL (API key), SubSource
(API key),
turkcealtyazi.org, subf2m.co and yifysubtitles.ch (scraped, no key, on by default; columns `turkcealtyazi_enabled`,
`subf2m_enabled`, `yify_enabled`). `/api/subtitles` answers within 6 s with what is ready; slower providers keep
running and fill the 30 min in-memory list cache for the next call (failures are cached 3 min, except network
failures and 5xx, which the next call retries). Provider requests go through `platform/upstream`: resets, stalls and
cut answers are retried on a new connection (also the OpenSubtitles login/download and turkcealtyazi form POSTs). So the players (web and
TV) ask again 8 s and 20 s after the first answer when it is empty or has no subtitle in the profile's language (the
first of `langs`), merge the lists by id and stop once that language is there; while the viewer has not picked a
subtitle and none is shown, the default pick runs again on the fuller list. Still missing after the retries (and the audio
is in another language), the players ask `subalt` for a release that carries it and re-query the list every 10 min with
`refresh=1` (see ARCHITECTURE.md). Settings live in the
`subtitle_settings` table.

- OpenSubtitles: search by `tmdb_id` (movies) or `parent_tmdb_id` + season/episode, plus the `moviehash` of the playing
  file (size + 64-bit word sums of the first and last 64 KiB, read through TorrServer range requests); a hash match is
  marked `synced`. `429` honours `Retry-After`; a `406` on download (or a `429` whose body names the quota, or a
  download answering `remaining: 0`) blocks downloads until `reset_time_utc` (1 h without it). Meanwhile the list marks
  OpenSubtitles items `quota: true` and ranks them last in their language, and their file requests answer 429 at once.
  Only `/api/subtitles/file` downloads: listing only searches, and one id is downloaded once (concurrent requests share
  the download, later ones and `refresh=1` lists use the cached file).
- SubDL: search by `tmdb_id` + type (+ season/episode, and a second `full_season=1` query for packs) with `unpack=1`, so a
  pack's single episode file is downloaded directly; otherwise the episode is picked from the zip.
- SubSource (`api.subsource.net/api/v1`, key in the `X-API-Key` header): each season of a show is its own title,
  found by IMDb id (`movies/search?searchType=imdb&season=N`, cached 6 h), then one `subtitles` listing per language.
  Items whose release names name another episode are dropped (anime absolute numbers like `- 02` count); items naming
  none are season packs, whose episode is picked from the zip.
- turkcealtyazi.org: the title page is found by IMDb id (`/tt<id>/`, via TMDB external ids), the download is the page's
  POST form (`/ind`) returning a zip. At most one request every 2 s server-wide, pages cached 24 h on disk, a clear
  User-Agent; on a Cloudflare challenge the page is loaded through FlareSolverr and its cookies are reused. RAR archives
  are not supported (none seen so far).
- subf2m.co (the Subscene archive; most Turkish subtitles of older films and series): its search is disallowed in
  robots.txt, so the title page is guessed from the TMDB English (then original) title in Subscene's path style
  (`/subtitles/the-mentalist-second-season`, `.../the-mentalist-season-2`; movies `inception-2010`, then `inception`; at
  most 4 tries, 404s are skipped) and accepted only when the IMDb id it links matches (movies without one: the year ±1).
  The found path is kept 24 h (a miss 6 h). One page per language (`/subtitles/<path>/turkish`, at most 3 languages);
  for an episode, rows naming other episodes are dropped and rows naming none are season packs (episode picked from
  the zip). Rows rated good / bad move up / down; `.HI` / SDH names are HI, an uploader comment calling it forced
  ("forced BluRay altyazı", not "forced dahil") marks it forced. The file is `<row>/download` (a redirect to
  `isubcdn.com`, a zip).
- yifysubtitles.ch: movies only, one page per IMDb id (`/movie-imdb/tt<id>`, all languages), files at
  `/subtitle/<name>.zip`; its up/down votes rank like subf2m's rating.
- Both scraped sites: one request per site every second server-wide, no hedging, pages cached 24 h on disk
  (`subf2m-pages`, `yify-pages`), redirects only on the site (and subf2m's CDN). A 404 title is an empty answer.

Ranking, per language in the requested order: synced (same torrent, embedded, OpenSubtitles hash match) first, then
ones already timed to the playing file that fit (shown `synced: true`), then release similarity with `title`
(release group, source family BluRay/WEB/HDTV/DVD, streaming service, resolution, edition, FPS mismatch penalty
against the release name or the file's probed frame rate) and provider quality (downloads, rating, trusted uploader,
turkcealtyazi quality tier, subf2m / YIFY rating); ones that did not fit even when timed move down. HI is penalised only when the language
has a regular subtitle; multi-CD subtitles are penalised; forced-only and machine/AI-translated subtitles always come
last. In a language with a subtitle known to have holes, the complete ones go ahead of it (a holey one still beats
one that does not fit the file). For an episode, subtitles whose release names (or torrent paths) name only other
episodes (`S02E06`, `2x06`) are left out; season packs are kept.

With `hash` + `index`, every item not made for that file gets an id that also names the file, so its file request
is timed to it; the ids stay the same for the same file.

### Holes

Some subtitles skip whole scenes (seen on Avatar: Seven Havens S01E02: turkcealtyazi's "WEB-DL Fenix", a machine
translation of the English GRACE subtitle, has nothing at 550-614 s and 1265-1312 s, 40 lines). Every file request of
a provider subtitle listed for a playing file (timed and fitting: `synced`, `unchanged`, `live`, `replaced`) compares
it with a reference of the same file in another language: a subtitle made for the file whose cues are on disk (its
own text track, an OpenSubtitles hash match), else one timed to fit the file, else the best one not timed yet (then
timed in the background; with the AI translation on it may be downloaded, never from OpenSubtitles); English without
HI first. A hole is a pause of the subtitle of 15 s or more holding 4 or more of the reference's dialogue lines (HI
parts and songs left out, the middle of a line 1.5 s inside the pause); the pauses before the first and after the
last cue count too (a subtitle starting late or ending early). Windows of the file's own subtitle track (text, or
a PGS track's packet times; only once it was found timed to the file, see ARCHITECTURE.md) that cover the
pause and show almost no lines there (under a third of the reference's), or audio heard there that is silent (under
3 % speech frames), veto a hole; speech never makes one (music and action reach 70-80 %). The result is kept per
subtitle and file (`synced/`) for the ranking and the list's `holes`, and logged (`subtitles: <id> on <hash8>:<index>
has holes against <ref>: 550-614 s (25 lines), …`).

With the AI translation on, the reference's lines in the holes are translated into the subtitle's language (the same
job and checks as an AI translation, stored under `translations-v2` keyed by reference, language and `holes`; nearest
to the player first; the reference's other lines go along as context with the subtitle's own line at that time as
their translation) and served in the reference's timing, cut to the subtitle's pause so they never overlap its own
cues, which stay as they are. A subtitle without holes is served unchanged. While lines are missing the answer is
partial (players keep asking), with `NOTE nextflix-ai-translating` while the player is in a hole or 10 s before it
(the first answer there waits up to 10 s); while the reference is still being timed the answer is partial too. The
log line adds `filled N lines with AI` once the holes are done. Without the AI translation the holes only count in
the ranking.

### AI translation

Off by default (`ai_translate_enabled`, key `anthropic_api_key`, languages `ai_translate_langs`: `profile` and/or
codes). When it is on and a list has no usable subtitle (not forced, not quota-blocked) in a target language, an `ai`
item (never marked HI) is added in that language's place: Claude Haiku 5.5 translates the best other subtitle. The
source is chosen by timing first: the file's own text tracks and subtitles made for it (a track still being read
live after a complete one), then subtitles timed to the file, then the rest; within that one already translated
first, then English without HI, English, without HI, any. OpenSubtitles comes after all others unless its file is on
disk; forced, quota-blocked and `poor` (not fitting the file) subtitles are never used. Targets and English are
added to the searched languages. Its file answer is the source exactly as served for the player (timed for the file
like any provider subtitle, and following later re-timing) with every cue in the source's timing; when the source
turns out not to fit the file (`poor` timing), another listed subtitle is translated instead (an OpenSubtitles
download may be used for it). Sound notes, music notes and speaker labels of HI sources are removed before
translating (an SDH track loses its sound-only cues). A language that has a subtitle gets no `ai` item; when that
subtitle has holes, they are filled instead (see [Holes](#holes)).

Once a player asks, the whole source is translated in the background: the lines from `t` on first (first request 20
lines, then up to 60; 3 requests at a time, 6 in total), then the lines before `t`, nearest first; a seek moves the
order, lines added to a source that is still read live join it. A line not translated yet is left out and the answer
stays partial (`NOTE nextflix-partial`, the players keep asking with their position) until every line of the source
is done and the source itself is complete. While the line at `t` is missing the answer also carries `NOTE
nextflix-ai-translating` (players ask again every 3 s and show a note); the first answer waits up to 10 s for it.

Requests are JSON lines with an id each (two lines around them as context, with their translation when there is
one); the answer is structured output (one JSON schema): per id the first 3 words of its line copied and the
translation. Every item is checked before it counts: it must echo its own line, be non-empty and not in Chinese,
Japanese or Korean script for other targets; two or more items echoing other lines, an id answered twice or no JSON
discard the whole answer; the neighbours of a skipped, empty or wrongly echoed line are asked again too (they may hold
its text). Answers that only look off (much longer or shorter than the line, speaker dashes lost, left untranslated, a
name of a neighbouring line) are asked once more and taken when the second answer looks off too. Failed lines are
retried after 2 s, 10 s, 30 s and 1 min in smaller requests (8, 3, then 1 line), so a line that is always refused
isolates itself; after 5 answers it is shown untranslated (not stored, tried again next time). A failed request
(network, overload) pauses the translation 10 s, doubling up to 5 min, without counting against the lines; a key or
account error pauses it 2 min. Checked lines are stored for good under `/app/data/ai-subtitles/translations-v2`
(`AI_SUBTITLE_DIR`, outside the swept cache; `{version: 2, lang, texts}`), keyed by source subtitle and language, so
a line is never paid twice and a restart goes on where it stopped. The translations of the old numbered-lines
protocol (`translations/`), some of which belonged to other lines, are deleted at start. For an episode the opening
(about 3 min) of the next one is translated once in the background. Usage per month is kept in
`ai_translation_usage`.

Cost (Haiku 5.5, $0.10 / $0.50 per million input / output tokens): a 22 min episode (294 lines after the HI strip)
took 30 requests, 35k input and 17k output tokens, $0.012, including two discarded 60-line answers. That is about
$0.02 for a 45 min episode (about 700 lines) and $0.05 for a 2 h film (about 1800 lines). The prompt is too small for
prompt caching to pay; the cost is mostly the answers' output tokens.

Files are converted to UTF-8 WebVTT once and cached forever under `/app/data/subtitles` (`SUBTITLE_CACHE_DIR`), keyed by
provider and file id, so a cached file never counts against a provider quota again.

## Playback log (`modules/playbacklog`)

| Method | Path | Notes |
|---|---|---|
| POST | `/api/playback-log` | Playback diagnostics from the players (clients `tv`, `web` for the web player's subtitle events, `web-a4k` for its upscaler), written to the server log and to daily files under `PLAYBACK_LOG_DIR` (UTC, kept 14 days, at most 50 MB a day). JSON body (any content type; the TV sends `text/plain` to skip the CORS preflight), at most 32 KB: `{sid, seq, client, ctx: {type, tmdb, s, e, title, src: "<hash8>:<index>", mode: muxed\|split\|src, debrid, q, dur}, dropped, events: [{t: <epoch ms>, k: <kind>, p: <file position s>, x: {<field>: number\|string\|bool}}]}`, at most 200 events. One `playback: <sid> <client> #<seq> ...` header line plus one line per event; invalid events are skipped, strings are reduced to safe tokens. 204; 400 bad shape, 413 too big, 429 more than a burst of 8 batches, then one per 4 s, per client IP |

Event kinds (`tv/webos/src/player/playbackLog.ts`, engine): `wait` (a `waiting` until it played again: `d` ms, `end`, buffered `ahead`, `rs` readyState, `ns` networkState, `seeking`, `start`), `freeze` (media time fell behind the wall clock by `ms` without a `waiting`), `jump` (position moved by `by` s without a seek), `drops` (over 5 frames dropped in ~2 s), `late` (the 250 ms timer fired `ms` late: a long main-thread task), `stalled`, `seek` (`why`: `user`, `skip-intro`, `gap`, `align-start`, `align-split`, `splice-stuck` or `unmarked`), `restart` / `open` (`why`), `seekpoint` (`fix`), `buffering`, `hold` / `hold-end`, `health`, `splice` / `splice-end` / `splice-drop`, `subs` / `subs-none` / `subdelay` (below), `stream-fail`, `error`, `offline` (`net`: `offline` or `unreachable`) / `online`, `early-end`, `ended`, `next`, `exit` (`subdelay` while a subtitle is shown), `quota` (a SourceBuffer was full), `ranges` (each SourceBuffer's ranges near the playhead at a `waiting`), and a `summary` every 60 s (`ahead_min`, `ahead_avg`, `dropped`, `decoded`, `waits`, `freezes`, `jumps`, `late`, `late_max`, `subdelay` while a subtitle is shown). Batches go every 30 s and on pause, end, next episode and exit (`keepalive`). Off: `PLAYBACK_LOG = false` in `playbackLog.ts`, or localStorage `nfx-tv:playback-log` = `off` on the TV. Read with `scripts/playback-log.sh [days] [filter]` (e.g. `2 S02E09`).

Subtitle events (both players, fields from `subsLog.ts` in `tv/webos/src/player/` and `web/src/features/watch/`; the web
player sends only these and `exit`, as client `web`). String values are made log-safe tokens (Turkish letters folded to
ASCII, other characters `_`, at most 40 chars); fields are printed sorted:

- `subs`: a subtitle was loaded, or a later answer changed it. `id` (first 24 chars), `cues`, `partial`, `lang`,
  `provider` (`source` of the list item: `opensubtitles`, `subdl`, `subsource`, `turkcealtyazi`, `torrent`, `embedded` ...),
  `name` (its release name), `sync` (raw `X-Subtitle-Sync`; `-` none, `hidden` when the browser cannot read it on a
  cross-origin answer), `why` (`open` the file's first default pick, `auto` a later pick by the player (fuller list,
  late upload), `user`, `fallback` (the one before gave no cues), `poll` (a partial subtitle asked again)), `first` /
  `last` (start of the first / last cue, 0.1 s), `subdelay` (s, + shows them later). A `poll` is logged only when the
  cues were replaced, `partial` / `sync` changed, or the cues moved (`shift`: median start change of the cues matched by
  text, `moved`: how many moved by 0.1 s or more; moves alone at most once a minute, counted from the last `subs`
  event); a live track that only grew is not logged.
  `... pos=312.4 subs cues=643 first=1.2 id=b3M6MTIzNDU2Nzg5MDpoYXNo lang=tr last=2580.4 name=Show.S02E10.HDTV.x264-LOL partial=true provider=turkcealtyazi subdelay=0 sync=pending why=open`
  `... pos=342.9 subs cues=643 first=3.6 id=b3M6MTIzNDU2Nzg5MDpoYXNo lang=tr last=2582.8 moved=641 name=Show.S02E10.HDTV.x264-LOL partial=false provider=turkcealtyazi shift=2.4 subdelay=0 sync=synced why=poll`
- `subs-none`: no subtitle shows. `why`: `off` (the viewer turned them off), `open` (the default pick chose none; `pref=off`
  when the viewer's last choice was off), `failed` (the shown one gave no cues and no other of its language is left),
  `missing` (after the list retries the profile's language is not listed); `lang` (profile language), `n` (subtitles
  listed), `have` (their languages, `+` separated), `audio` (audio track language).
  `... pos=0 subs-none audio=en have=en+es lang=tr n=14 why=missing`
- `subdelay`: the viewer changed the subtitle delay; logged 3 s after the last change with the settled value, `from`
  (the value logged before), `id`, `provider`; a pending change is logged at once on exit or when another file starts.
  `... pos=95.1 subdelay from=0 id=b3M6MTIzNDU2Nzg5MDpoYXNo provider=turkcealtyazi subdelay=1.5`
