# TMDB setup

All titles, artwork, seasons, trailers and provider data come from [TMDB](https://www.themoviedb.org/).
Nextflix stores nothing locally except profiles, progress, My List and indexer settings.

## Get a token

1. Create a free account at [themoviedb.org](https://www.themoviedb.org/).
2. Open **Settings -> API** and request an API key (developer, personal use).
3. Copy the **API Read Access Token** (v4, a long JWT starting with `eyJ`). The short v3 "API Key" does not work:
   the server sends the value as `Authorization: Bearer <token>`.

## Configure

Put the token into `deploy/config/server.env`:

```
TMDB_API_KEY=eyJhbGciOi...
```

and restart the server: `docker compose -f deploy/docker-compose.yml up -d server`.

Check it from the host:

```bash
curl -s "http://127.0.0.1:8252/api/tmdb/movie/popular" | head -c 300
```

## How the app uses TMDB

- The server (`platform/tmdb`) calls `api.themoviedb.org` and keeps a 24-hour in-memory cache shared by the
  catalog and notifications modules. Provider showcases are additionally cached in Redis.
- Browsers never talk to TMDB. Images go through the web container's `/tmdb-img/*` proxy, for example
  `/tmdb-img/t/p/w500/<file>.jpg` (`web/src/shared/lib/image.ts`). Common sizes: `w185`, `w342`, `w500`, `w780`,
  `w1280`, `original`.
- If TMDB requests fail although the token is valid, the ISP may be hijacking DNS; see the DNS note in the README.

The full TMDB API reference used during development is in [reference/tmdb_api_doc.md](reference/tmdb_api_doc.md).
