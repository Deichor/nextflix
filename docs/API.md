# API

Base URL: `http://<host>:8252/api` (the web app also proxies `/api/*` to the server). All responses are JSON
except the video stream. Errors look like `{ "error": "message" }`.

Per-profile endpoints read the active profile from the `X-User-ID` header (My List) or from the `profile_id`
query/body field (progress).

## Health

| Method | Path | Notes |
|---|---|---|
| GET | `/api/health` | 200 `{status, timestamp, version, services: {database}, uptime}`; 503 when the database ping fails |

## Profiles (`modules/profiles`)

Profile: `{ "id": 1, "name": "Home", "avatar": "smile", "color": "#E50914", "created_at": "..." }`

| Method | Path | Notes |
|---|---|---|
| GET | `/api/profiles` | 200 `[Profile]` ordered by id |
| POST | `/api/profiles` | body `{name, avatar, color}` → 201 Profile; 400 if the name is empty or longer than 20 characters, or 5 profiles exist |
| PUT | `/api/profiles/:id` | body with any of `{name, avatar, color}` → 200 Profile; 404 if missing |
| DELETE | `/api/profiles/:id` | 204; also deletes the profile's progress and My List; 400 for the last profile |

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
| GET | `/api/stream-progress?profile_id=` | Continue Watching row: newest first, one entry per title, finished entries (>= 92 %) excluded, max 20 |
| GET | `/api/stream-progress/item?profile_id=&media_type=&tmdb_id=&season=&episode=` | 200 Progress or 404 |
| GET | `/api/stream-progress/title?profile_id=&media_type=tv&tmdb_id=` | every entry of one title for the profile, finished ones included (per-episode progress bars) |
| DELETE | `/api/stream-progress?profile_id=&media_type=&tmdb_id=` | 204; removes all episodes of that title |

## My List (`modules/mylist`)

Scoped by the `X-User-ID` header (400 `X-User-ID header is required` without it). `:id` is the list media id:
`"9"` followed by the TMDB id.

| Method | Path | Notes |
|---|---|---|
| GET | `/api/mylist` | 200 `[{id, user_id, media_id, media_type, notes, priority, added_at}]` |
| GET | `/api/mylist/check/:id` | 200 `{in_list}` |
| POST | `/api/mylist/:id` | body `{media_type, notes?, priority?}` → 200 `{message}`; 500 `item already exists in my list` |
| DELETE | `/api/mylist/:id` | 200 `{message}` |

## Catalog (`modules/catalog`, TMDB)

| Method | Path |
|---|---|
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
| GET | `/api/tmdb/:id/related` |

## Providers (`modules/providers`, TMDB)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/providers` | |
| GET | `/api/providers/showcase` | cached in Redis (`providers:showcase`) |
| GET | `/api/providers/:id` | |
| GET | `/api/providers/:id/content` | |
| GET | `/api/providers/:id/movies` | |
| GET | `/api/providers/:id/tv` | |
| GET | `/api/providers/:id/trending` | |
| GET | `/api/providers/:id/popular` | |
| GET | `/api/providers/:id/top-rated` | |
| GET | `/api/providers/:id/new` | |
| GET | `/api/content/:type/:id/providers` | providers of one title |

## Notifications (`modules/notifications`)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/notifications` | `{ notifications: [], count: 0 }` when Redis is unavailable |
| GET | `/api/notifications/count` | |
| POST | `/api/notifications/:id/read` | |

## Streaming (`modules/streaming`)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/torrent-stream/resolve?type=movie&title=&year=` | auto-pick for the Watch button |
| GET | `/api/torrent-stream/resolve?type=tv&title=&season=&episode=` | → 200 `{magnet, hash, index, file_name, torrent_title, quality, seeders, size}`, 404 `{error: "No playable source found"}`; can take up to ~60 s |
| GET | `/api/torrent-stream/files?magnet=` | `{hash, name, files: [{id, name, path, size}]}` |
| GET | `/api/torrent-stream/probe?hash=&index=` | `{duration, video_codec, audio_tracks: [{index, language, title, codec, channels}]}` |
| GET | `/api/torrent-stream/play?hash=&index=&t=<sec>&audio=<n>` | live fragmented MP4; seeking = new request with a new `t` |
| GET | `/api/settings/indexer` | 200 `{id, jackett_url, jackett_api_key, min_seeders, preferred_quality, created_at, updated_at}` |
| PUT | `/api/settings/indexer` | any of `{jackett_url, jackett_api_key, min_seeders, preferred_quality}` → 200 settings; 400 on invalid values |
| POST | `/api/settings/indexer/test` | optional body `{jackett_url, jackett_api_key}` (empty fields fall back to the saved settings, then env) → 200 `{success: true, message, jackett_url, test_results}`; 400 when nothing is configured, 503 when Jackett fails |

The indexer settings endpoints replace the old `/api/torrents/config` and `/api/torrents/test-connection`.
