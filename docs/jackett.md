# Jackett and FlareSolverr

Nextflix finds video through [Jackett](https://github.com/Jackett/Jackett), which searches many public torrent
indexers with one API. Some indexers (1337x, for example) sit behind Cloudflare; Jackett sends those requests
through [FlareSolverr](https://github.com/FlareSolverr/FlareSolverr), which solves the challenge in a headless
browser. Both run as services of `deploy/docker-compose.yml`:

| Service | Address inside the stack | From the host |
|---|---|---|
| `jackett` | `http://jackett:9117` | `http://127.0.0.1:9117` (localhost only) |
| `flaresolverr` | `http://flaresolverr:8191` | not published |

Jackett's config lives in `deploy/data/jackett/` (bind mount, gitignored; override with `JACKETT_CONFIG_DIR`).

## How the Watch button uses it

`GET /api/torrent-stream/resolve` (module `streaming`) builds a few queries from the title (and `SxxEyy` for TV),
searches all Jackett indexers in parallel, rejects wrong titles, bad sources (cam, telesync, ...) and dead
torrents, ranks the rest for direct browser playback, and confirms the best few through TorrServer before
answering. Results are cached for a few hours. The Jackett URL and API key come from **Settings -> Indexer**; if they are empty, the server falls back to
`JACKETT_URL` / `JACKETT_API_KEY` from the environment and finally to `http://jackett:9117`.

## Setup

1. Start the stack (`docker compose -f deploy/docker-compose.yml up -d`).
2. Open `http://127.0.0.1:9117`.
3. In Jackett's settings, set **FlareSolverr API URL** to `http://flaresolverr:8191` and save.
   (The service name must stay `flaresolverr`, otherwise this URL breaks.)
4. Click **Add indexer** and add public indexers. A working set: 1337x, EZTV, Internet Archive, LimeTorrents,
   Nyaa.si, The Pirate Bay, YTS. Use the wrench icon -> **Test** on each one.
5. Copy the **API Key** from the top right of the dashboard.
6. In Nextflix open **Settings -> Indexer**, set the Jackett URL to `http://jackett:9117` (the address the server
   container uses, not `localhost`), paste the API key and press **Test**. Save.
   Alternatively put the key into `JACKETT_API_KEY` in `deploy/config/server.env`.

## Testing from the host

```bash
curl "http://127.0.0.1:9117/api/v2.0/indexers/all/results?apikey=<API_KEY>&Query=big+buck+bunny"
```

The response is JSON with a `Results` array. An empty array with `Indexers[].Error` set usually means the indexer
is down, blocked by DNS (see the DNS note in the README) or needs FlareSolverr.

## Troubleshooting

- **"No playable source found"**: no release passed the filters. Add more indexers or try again; resolving can take
  up to about a minute.
- **Cloudflare errors on an indexer**: check that FlareSolverr is running
  (`docker compose -f deploy/docker-compose.yml logs flaresolverr`) and that Jackett's FlareSolverr URL is
  `http://flaresolverr:8191`.
- **Indexers time out or return nothing**: the ISP may hijack DNS for torrent sites. Use DNS over HTTPS on the host
  (README, "DNS note").
- Jackett has no authentication by default. Keep port 9117 bound to `127.0.0.1` or set an admin password in Jackett.
