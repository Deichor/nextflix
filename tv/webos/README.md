# Nextflix for LG webOS TVs

A packaged webOS web app (Vite + React + TypeScript) with a 10-foot Netflix-style UI for the Nextflix server.
It talks to the same API as the web app (`docs/API.md`) and streams through the server's `/play` fragmented MP4.

Targets webOS 6+ (Chromium 79+). The build is lowered to Chromium 53 (one classic IIFE script plus polyfills), so
webOS 4.x and 5.x should work too, but they are untested.

```
tv/webos/
  appinfo.json        webOS manifest (version comes from package.json at build time)
  index.html          Vite entry
  src/
    main.tsx          polyfills, fonts, spatial navigation, React root
    App.tsx           screens, Back key, player overlay, exit dialog
    nav/              spatial.ts (D-pad focus engine), router.tsx (screen stack + focus restore)
    lib/              api client, catalog, streaming, subtitles, i18n (en / tr), webOS helpers
    screens/          Setup, Profiles, Add Profile, Home, Movies, TV Shows, Search, Surprise, Detail, Settings
    player/           engine.ts (resolve / probe / play / seek / upgrade), Player UI, panel, subtitle overlay
    components/       rows, cards, hero, side menu, on-screen keyboard
  scripts/            build.sh, check.sh, dev.sh, ares.sh, smoke.sh (+ smoke.mjs, finalize.mjs)
  docker/             ares.Dockerfile (webOS CLI), smoke.Dockerfile (Playwright + Chrome)
```

Everything runs in containers. From PowerShell, call the scripts through WSL, for example
`wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/webos/scripts/build.sh`.
`node_modules` lives in the Docker volume `nextflix-tv-node-modules`, not on the Windows drive.

## Build

```
scripts/check.sh      # type check only, prints TSC_EXIT=0
scripts/build.sh      # type check + build -> dist/, then out/com.nextflix.tv_<version>_all.ipk
```

`build.sh` builds the image `nextflix-ares:3.2.6` (Node + `@webos-tools/cli`) on first use and packages `dist/`
with `ares-package`. Bump `version` in `package.json` for a new release.

## Desktop dev mode

```
scripts/dev.sh                # foreground; DETACH=1 scripts/dev.sh runs it in the background
```

Open http://127.0.0.1:5173 (bound to localhost only). Arrow keys move, Enter selects, Esc or Backspace is Back.
`?server=http://192.168.1.221:8252` in the URL overrides the stored server address. `docker stop nextflix-tv-dev`
stops a background server.

`scripts/smoke.sh` drives the dev server in headless Google Chrome and writes screenshots to `out/screens/`.
`PLAY=1 scripts/smoke.sh` also plays "Night of the Living Dead" (1968, public domain) and removes the progress it
saved when the profile had none before. `SCRIPT=intro scripts/smoke.sh` checks the opening intro instead (frames
in `out/screens/intro-*.png`, key skip, settings switch, sting level).

## Install on the TV

### 1. Developer Mode on the TV

1. Create a free account at https://webostv.developer.lge.com.
2. On the TV, install **Developer Mode** from the LG Content Store and sign in with that account.
3. Turn **Dev Mode Status** on; the TV restarts.
4. Open Developer Mode again and turn **Key Server** on. Note the TV's IP address and the **passphrase** shown there.

Developer Mode expires after 50 hours; the app's "Extend" button (or a re-login) resets the timer. Apps installed
in Dev Mode disappear when it expires.

### 2. Register the TV (once)

`scripts/ares.sh` runs any `ares-*` command in a container with host networking. The device list and the SSH key
are kept in `tv/webos/.ares/` (gitignored), so this is done once.

```
scripts/ares.sh ares-setup-device -a tv -i "host=192.168.1.50" -i "port=9922" -i "username=prisoner"
scripts/ares.sh ares-novacom --device tv --getkey      # asks for the passphrase from the TV
scripts/ares.sh ares-device --device tv --system-info  # checks the connection
```

Replace `192.168.1.50` with the TV's address. `-i` can be repeated; `ares-setup-device -l` lists devices and
`ares-setup-device -r tv` removes one.

### 3. Install and start

```
scripts/build.sh
scripts/ares.sh ares-install --device tv out/com.nextflix.tv_0.1.19_all.ipk
scripts/ares.sh ares-launch --device tv com.nextflix.tv
```

`ares-install --device tv --remove com.nextflix.tv` uninstalls it; `ares-inspect --device tv --app com.nextflix.tv`
prints a DevTools URL for debugging.

## Point the app at the server

On first start the app tries `http://nextflix.local:8252`, `http://192.168.1.221:8252` and `http://127.0.0.1:8252`
and preselects the first that answers (not every TV resolves `.local` names); you can still edit the address. Type it with the on-screen
keyboard (or the remote's number keys) and choose **Save and continue**; **Test connection** only checks it. A bare
address gets `http://` and port `8252` added. It is stored on the TV (localStorage) and can be changed later in
Settings or with **Change server** on the profile screen.

The packaged app is loaded from `file://`, so its requests carry `Origin: null`, the same origin a sandboxed iframe on
any website sends (also in the TV's own browser, which has the same user agent). The server accepts that origin only
with the per-install key `TV_APP_KEY` from `deploy/config/server.env`: `scripts/build.sh` creates it on the first
build and embeds it, and `lib/appKey.ts` adds it as `?app_key=` to the app's fetches and beacons. After the first
build restart the server (`docker compose -f deploy/docker-compose.yml up -d`) so it reads the key; an app built
with another key gets 403. The app's images and video (no Origin) only need the webOS user agent. The PC firewall
must allow the LAN to reach port 8252 (see the root docs).

## Remote control

| Where | Keys |
|---|---|
| Everywhere | arrows move, OK selects, Back goes back (on Home: side menu, then "Exit?") |
| Rows | Left from the first card opens the side menu; focus comes back to the same card after Back |
| Player | OK play / pause, Left / Right seek 10 s; a second press (or holding the key: 30 s, then 60 s steps) opens scrub mode with a strip of preview frames, where OK seeks to the shown time, Back cancels and Up / Down close (15 s without a key: closed, no seek; playback goes on underneath). Up shows the buttons (play, ±10 s, next episode, Audio & Subtitles), Back closes |
| Audio & Subtitles | Up / Down move, OK picks; Back, Left past the audio column, a Magic Remote click outside the panel (it never reaches the video, so no play / pause) or 15 s without a key or pointer move close it. **Subtitle timing** (shown while a subtitle is on, above Subtitle appearance): Left / Right show the subtitles 0.5 s earlier / later (up to ±60 s), OK resets to 0; it is kept for the playing file, like on the web. In **Subtitle appearance** (last row of the subtitle list) Left / Right or OK change the focused value with a live preview, Back returns to the lists |
| Media keys | Play, Pause, Play/Pause, Stop (closes), Rewind / Fast forward (seek) |
| Magic Remote | pointing focuses, click selects, clicking the progress bar seeks |

## Keyboard, search filters and Surprise me

- On-screen keyboard (`components/Keyboard.tsx`): names (Add Profile) use the Turkish alphabet order with q, w, x
  in their Latin places, 9 keys per row (`a b c ç d e f g ğ` / `h ı i j k l m n o` / `ö p q r s ş t u ü` /
  `v w x y z ' - .`), then the digits and Shift, Space, Delete, Clear. Search uses compact keys sharing the column
  width: the Turkish order in 4 rows of 8 with a Turkish profile, the Latin order in rows of 9 with an English one,
  then a row of 10 digits and Space, Delete, Clear. The server address keeps its own layout (digits and `. : / - _`
  first).
- Search: the left column holds the field, the keyboard, the title completions and the filters: All / Movies / TV
  Shows in one row, then Anime and the genres (sorted by name) in a 3-column grid (chosen: white with a check). It
  scrolls with the focus when taller than the screen (a fade at the bottom while more is below); Up / Down move to
  the nearest element there. OK toggles a filter, several genres must all match. Filters alone browse
  (`/api/catalog/discover`, the next page loads near the end of the grid); with text they narrow the search results.
  The results use the full height on the right (4 columns); Right from the left column enters them, Left from the
  first column returns to the element last focused on the left.
- Surprise me (Home: Down past the last row, a centered button; the hero explains it while focused): one title per
  press with the reason (close to the taste "Because you loved …", a new pick in a liked genre, or "Something
  completely different"), Play (detail page with autoplay), Details and **Something else** for the next pick. Back
  returns the focus to the button.

## Opening intro

On a cold start the red N lands on black, crimson light ribbons fan out of it and it zooms through into the app
(`components/Intro.tsx`, about 2.8 s, transform / opacity only; fewer layers on webOS 4/5). The N, its glow and the
radial lights are drawn once on canvases, which the GPU only scales: as text and CSS gradients they were re-rastered
at the zoom's largest scale and froze the TV's compositor for the whole animation. The intro holds its black first
frame until the TV draws frames steadily (`whenOnScreen` in `lib/intro.ts`; `window.__nxBoot` logs the launch
timeline). A two-hit sting is synthesized with Web Audio (`lib/intro.ts`, no audio files, peak about -12 dBFS).
Profiles load behind it, any key or click skips it, and it does not replay when the app comes back from the
background. Settings has **Opening animation** and **Opening sound** switches; `prefers-reduced-motion` skips the
animation.

## Playback

- Capabilities are detected once: HEVC and Dolby Vision via `MediaSource.isTypeSupported` / `canPlayType`, HDR10,
  Dolby Vision and Atmos via the TV's config (Luna `com.webos.service.config/getConfigs`, the keys webOSTV.js reads),
  audio codecs via MSE (not FLAC, whose silent frames stall the TV's MSE): the ones it plays muxed with video
  (`aac, opus`) and the ones it plays only on their own (`ac3, eac3`: an LG TV refuses `avc1/hvc1 + ec-3` in one
  SourceBuffer). Resolve-family calls (resolve, prefetch, release, sidegrade, upgrade, subalt) send all of them in
  `acodecs=` plus `hevc=1`, `hdr=1`, `dv=1`, `atmos=1`, so Atmos releases rank first; probe, seekpoint and play send the
  muxed ones in `acodecs=` and the others in `split_acodecs=`, so a muxed stream never copies them.
  Settings shows what was detected and can force HDR on or off.
- The `/play` stream reaches the `<video>` through Media Source Extensions (`player/mseStream.ts`): webOS reads the
  metadata of a live fragmented MP4 given as `src` but decodes no frame of it (sound only, black picture). The
  codecs come from the init segment (`player/mp4Codecs.ts`: avc1 / hvc1 / hev1 / dvh1, mp4a / opus / flac / ac-3 /
  ec-3); a type the TV refuses falls back to `src`. Every seek is a new request into a new MediaSource. Each
  SourceBuffer keeps to its byte budget (75 MB video, 15 MB audio; the TV refuses ~79 / ~18 MB, and past its limit
  it also drops data ahead of the playhead): up to 2 min ahead (resumes below 1.5 min), up to a minute behind.
  The bytes held are counted per append (not average bitrate × buffered seconds, which a dense scene of a VBR
  stream pushes past the limit), and after a splice the bitrate counts only the new stream.
- Split mode (the server's probe says `split: true` and the track is E-AC-3 / AC-3): `play?track=video` and
  `track=audio` go into two SourceBuffers on the file's timeline, so E-AC-3 and its Atmos reach the TV untouched.
  An older server gets the muxed request with converted audio. `localStorage nfx-tv:split` = `always` / `never`
  overrides the choice for tests.
- Upgrades, faster sources and audio track changes are spliced into the playing MediaSource (no loading screen) when
  the codec family stays; an upgrade to another family (H.264 to HEVC) waits for the next seek. A restart onto another
  source only follows a real stall and never starts at 0 from a position past 30 s. The console logs `[restart] …`,
  `[splice] …` and `[health] …` lines; `window.__nxPlayer` is the engine in DevTools.
- Like the web player: saved torrents are reused, the probe's duration check finds a complete version, stalls restart
  the stream, a starving source is swapped for a faster one (sidegrade) or a lower resolution, and below 4K the
  player asks `upgrade` every 15 s for 5 minutes and splices it in at the current position. A draining buffer
  (`player/bufferHealth.ts`, shared logic with the web, sized to the byte budget on the TV) asks `sidegrade` early;
  only a clearly faster offer (rate / bitrate ≥ 1.3 and above the playing source) is kept and spliced in.
- Skip intro and Next episode (`player/skipSegments.ts`, `GET /api/skip-segments`): a focused "Skip intro" button
  during the intro (OK skips, Left / Right still seek); during the end credits a focused "Next episode" button fills
  up for 5 s next to "Watch credits", then the next episode starts. Without credits data the countdown only comes
  when the video ends. See docs/ARCHITECTURE.md "Skip intro and Next episode".
- Subtitles are fetched as WebVTT and drawn over the video at `stream offset + video time`. While the server still times
  a subtitle to the file, it is asked again (every 15 s while partial, every 5 s while still untimed in the first 3
  minutes after it was put on, every minute while playing when complete but not final, see docs/API.md) and the new
  cues replace the old; only final answers are kept. Once the next episode's prefetch found its source, the subtitle
  the open would pick there is timed by the server before it opens (`player/pretime.ts`). Without a subtitle in the
  profile's language the list is fetched again after 8 s and 20 s (slower providers finish later). A subtitle file
  that brings no cues (e.g. an OpenSubtitles quota answer) is replaced by the next one of the language, another
  provider first. A TorBox (debrid) source shows a "⚡ TorBox" badge.
- Subtitle appearance (`lib/subtitleStyle.ts`, in the player panel and in Settings): size (small / medium / large /
  extra large: 0.8 / 1 / 1.25 / 1.5 × the old size), background (none / see-through box / black box, one box per
  line), dimming (none / light / dark: a static gradient band over the bottom 40 % that fades in while cues show and
  stays through gaps under 1.5 s), edge (shadow / outline from eight hard text-shadows / none), color (white /
  yellow / cyan) and position (bottom / a bit higher). Defaults are the old look. No blur or backdrop-filter. Stored
  per profile in localStorage `hfx-player:<profileId>` (`subSize` like the web app, the rest in `subStyle`); the
  server keeps no per-profile subtitle look, so web and TV do not share it.
- Trick play (`player/TrickPlay.tsx`): while Left / Right scrub, the frame at the target time shows large in the
  middle with the frames one and two steps away at the sides (server `GET /thumb`, see
  `docs/ARCHITECTURE.md#trick-play-thumbnails`); frames not loaded yet show their time and seeking never waits for
  them. While playing, the frames 30 s around the position are prefetched every 20 s.
- Progress is saved every 10 s, on pause, when the player closes and when the TV's Home button hides the app.
  Closing releases the title and every torrent the player used. The next episode is warmed near the end and starts
  after a 5 s countdown (turn off in Settings).
