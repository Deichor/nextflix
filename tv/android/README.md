# Nextflix for Android TV and Google TV

A native Android TV app: Kotlin, Jetpack Compose for TV (`androidx.tv:tv-material`, no Leanback) and AndroidX
Media3 (ExoPlayer). It has the same screens and behaviour as the webOS app (`tv/webos`), ported from
`tv/webos/src`, and talks to the same server API (`docs/API.md`). The server needs no changes for it.

Targets Android 8.0+ (API 26) TVs and streaming boxes: Android TV, Google TV (Chromecast with Google TV, Google TV
Streamer, TVs with Google TV). It also installs on phones and tablets, but it is built for the remote.

```
tv/android/
  app/                Application, MainActivity, screen stack host, side menu shell, Back / exit dialog,
                      intro zoom, outside intents (AppLink), home screen Watch Next row (WatchNext)
  core/               HTTP client, session, models, repositories, prefs, i18n (en / tr), theme, shared UI,
                      focus helpers, navigation contract (Route, Navigator, PlayRequest)
  feature/
    player/           ExoPlayer engine (resolve / probe / play / seek / upgrade), player UI, trick play,
                      pause screen, Audio & Subtitles panel, playback caps, playback log
    subtitles/        subtitle list rules, VTT parsing, cue overlay, per-file timing
    browse/           Home, Movies, TV Shows, Surprise me
    detail/           Detail page
    search/           Search (keyboard, filters, completions, voice)
    account/          Setup, Profiles, Add Profile, Settings, opening intro
  docker/             android.Dockerfile (JDK 21, Android SDK, Gradle; image nextflix-android:1)
  scripts/            check.sh, build.sh, adb.sh
  tools/              fonts.sh (Inter subsets, banner wordmark), lucide.sh (icons)
  CONTRACTS.md        module rules and the public API of every module
```

Module rules (features depend only on `:core`, the player also on `:feature:subtitles`; entry points; core API)
are in [CONTRACTS.md](CONTRACTS.md).

## Build

Everything runs in containers inside WSL; nothing is built on the host. From PowerShell, call the scripts through
WSL (Git Bash rewrites `/mnt/c` paths):

```
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/check.sh assembleDebug testDebugUnitTest
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/build.sh
```

- `check.sh [tasks…]` (default `assembleDebug`) copies the sources into a Docker volume and runs Gradle there. It
  prints only compiler errors and failed tasks, then `GRADLE_EXIT=<code>`. `WARNINGS=1` adds Kotlin warnings;
  `WORK_ID=<name>` gives a separate incremental build dir.
- `build.sh` builds `out/nextflix-tv-<version>.apk` (release, R8, signed) and `out/nextflix-tv-<version>-debug.apk`.
- Only one Gradle build runs at a time (a lock in `/tmp`); WSL has little free RAM, so never start Gradle any other
  way.
- The first run builds the image (a few minutes) and fills the Gradle cache volume `nextflix-android-gradle`.
- Version: `nextflix.versionName` / `nextflix.versionCode` in `gradle.properties`. Bump both for a release; Android
  refuses an update with a lower or equal `versionCode`.
- The release key is created on the first `build.sh` in `.signing/` (gitignored). Keep it: an APK signed with
  another key cannot update the installed app (uninstall first, which drops the app's settings).

## Emulator on this PC

`scripts/emulator.sh` runs an Android TV 16 emulator (x86_64, 720p) in a container. It needs `/dev/kvm` in WSL and
about 4 GB of free RAM. The screen is served by noVNC at http://127.0.0.1:6080/vnc.html (localhost only): arrow keys
are the D-pad, Enter is OK, Esc is Back.

```
scripts/emulator.sh start            # first run builds nextflix-android-emulator:1 (a few minutes)
scripts/emulator.sh install          # out/nextflix-tv-<version>-debug.apk
scripts/emulator.sh launch
scripts/emulator.sh shot home        # out/emulator/home.png
scripts/emulator.sh key DPAD_DOWN DPAD_CENTER
scripts/emulator.sh stop
```

Server address in the app: `http://nextflix.local:8252` or `http://10.0.2.2:8252` (the emulator's alias for this
machine). The emulator decodes video in software: fine for the UI and H.264 playback, not for 4K, HDR or passthrough.

## Install on the TV

### 1. Developer options and network debugging

- **Google TV** (Android 12+): Settings > System > About, press OK on **Android TV OS build** seven times. Then
  Settings > System > Developer options: turn on **USB debugging**, and on Android 11+ devices also
  **Wireless debugging**.
- **Older Android TV**: Settings > Device Preferences > About, press OK on **Build** seven times. Then Developer
  options: **USB debugging** on, and **Network debugging** where the TV has it.
- Note the TV's IP address (Settings > Network & Internet, or the Wireless debugging screen).

### 2. Connect

`scripts/adb.sh` runs adb in a container with host networking. One adb server container (`nextflix-adb`) stays up,
so a connection survives between calls; its key lives in `tv/android/.adb/` (gitignored), so the TV asks to allow
this computer only once. Paths are relative to `tv/android`.

```
# Network debugging (most Android TV devices): port 5555
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh connect 192.168.1.50:5555

# Wireless debugging (Google TV, Android 11+): "Pair device with pairing code" shows an address and a code once,
# the Wireless debugging screen shows the address to connect to
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh pair 192.168.1.50:37123 123456
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh connect 192.168.1.50:41567

wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh devices
```

Accept **Allow USB debugging?** on the TV (tick "Always allow from this computer").

### 3. Install and start

```
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh install -r out/nextflix-tv-0.1.0.apk
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh shell am start -n com.nextflix.tv/.MainActivity
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh logcat -s Nextflix NextflixPlayer
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/adb.sh stop
```

`adb.sh uninstall com.nextflix.tv` removes it. The app shows in the TV's app list with its banner; on Google TV
sideloaded apps are under Apps > See all apps.

## Point the app at the server

On first start the Setup screen probes `http://nextflix.local:8252`, `http://192.168.1.221:8252` and
`http://127.0.0.1:8252` and preselects the first that answers; edit it with the on-screen keyboard or the remote's
number keys. **Save and continue** checks `/api/health` and `/api/profiles`; **Test connection** only checks. A bare
address gets `http://` and port `8252`. Settings > Server and **Change server** on the profile screen change it later.

The app sends no `Origin` header, so it needs no `TV_APP_KEY` (that is only for the packaged webOS app); the server's
Host check accepts LAN addresses and `nextflix.local`. Plain HTTP to the LAN server is allowed by
`app/src/main/res/xml/network_security_config.xml`. The PC firewall must allow the LAN to reach port 8252.

## Remote control

| Where | Keys |
|---|---|
| Everywhere | D-pad moves, OK selects, Back goes back (on a tab: Home; on Home: the side menu, then "Exit?") |
| Rows | Left from the first card opens the side menu; focus comes back to the same card after Back |
| Search | The on-screen keyboard, a hardware keyboard or the number keys type; the mic key (or the remote's Search key, when the system passes it on) starts voice search |
| Player | OK play / pause, Left / Right seek 10 s; a second press or a held key (30 s, then 60 s steps) opens scrub mode with preview frames, OK seeks, Back cancels. Up shows the buttons (play, ±10 s, next episode, Audio & Subtitles), Back closes the panel, then the player |
| Audio & Subtitles | Up / Down move, OK picks; subtitle timing (Left / Right ±0.5 s, OK resets) and subtitle appearance with a live preview, like webOS |
| Media keys | Play, Pause, Play/Pause, Stop (closes), Rewind / Fast forward (scrub) |

## Android TV and Google TV integration

- **Continue watching on the home screen** (`app/.../WatchNext.kt`): the active profile's Continue Watching list
  (max 12) is published as Watch Next programs through `androidx.tvprovider`: in progress titles as "continue", the
  server's next-episode entries as "next" (not an episode that has not aired yet), with the backdrop through
  `<server>/tmdb-img/`. It syncs when the profile changes, 30 s after progress was saved and when the app goes to the
  background. A card opens the app on
  that profile at Home > Detail > Player, resuming the saved file and position (Back lands on the Detail page). A
  card the viewer removed stays hidden until the title is watched again. Only on devices with the
  `android.software.leanback` feature.
- **System search**: `ACTION_SEARCH` and `MEDIA_PLAY_FROM_SEARCH` ("search for … on Nextflix") open the Search tab
  with the query (`app/.../AppLink.kt`, `core.pendingSearch`). Google TV's own search results (a searchable content
  provider) are not implemented.
- **Voice search** in the app uses `SpeechRecognizer` (it asks for the microphone once); without a recognition
  service or permission it opens the system recognizer, which needs no permission.
- The screen stays on while the player shows; the launcher banner and icon are vectors (`app/src/main/res`).

## Differences from the webOS app

- **Player**: ExoPlayer plays the server's live fragmented MP4 (`feature/player/StreamMedia.kt`), with no Media3
  retries (the server would restart the stream from its beginning). Seeks inside the buffer are instant; other
  seeks are a new `/play` request with a new `t`, like webOS. There is no split mode and no seamless splice:
  audio track changes and subtitle alternatives restart the stream at the current position; an upgrade restarts at
  once only within 90 s of the start, later it waits for the next seek; a faster source found while the buffer
  drains is used at the next real stall. Frame counts in the playback log come from ExoPlayer's decoder counters.
- **Capabilities** (`AndroidPlaybackCaps`): HEVC means a hardware Main10 decoder that handles 4K; HDR and Dolby
  Vision come from the display's HDR types (Dolby Vision also needs a profile 5 decoder); audio codecs count when
  there is a decoder or HDMI passthrough, Atmos needs E-AC-3. If the TV refuses audio it claimed, the app drops to
  AAC / Opus and restarts the stream. Settings shows what was detected and can force HDR on or off.
- **Subtitles**: the timing (delay) is kept per file and comes back when the same file plays again; changing the
  audio language re-runs the default subtitle pick while the viewer never chose one.
- **Settings** also edits the profile language and the server's Indexer and TorBox settings (from the web app).
- Stored data lives in the app's DataStore (server, active profile, device settings, per-profile player prefs and
  subtitle look), not in the server; web and TV apps do not share the subtitle look.

## Not verified yet

Everything compiles and the unit tests pass (`check.sh assembleDebug testDebugUnitTest`), but the app has not run
on a TV or emulator yet: focus movement, playback, the intro and the Watch Next row are checked only by the compiler.
