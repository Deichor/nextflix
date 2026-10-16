# Android TV app: module contracts

Native Android TV app for Nextflix: Kotlin, Jetpack Compose for TV (`androidx.tv:tv-material`, no Leanback),
Media3 ExoPlayer. It ports the webOS app (`tv/webos/src`) and talks to the same server API (`docs/API.md`,
`docs/ARCHITECTURE.md`). The server does not change.

This file is the contract between the modules: what each one owns, the public entry points and the core API.
Keep it in step with the code.

## Modules and ownership

| Module | Path | Contents |
|---|---|---|
| (build) | `settings.gradle.kts`, `build.gradle.kts`, `gradle.properties`, `gradle/libs.versions.toml`, `docker/`, `scripts/`, `tools/` | build setup, versions, scripts |
| `:core` | `core/` | HTTP, session, models, shared repositories, prefs, i18n, theme, focus helpers, shared UI, navigation contract |
| `:app` | `app/` | `NextflixApp`, `MainActivity`, `AppRoot` (nav host, side menu shell, Back / exit dialog, intro and its zoom, keep screen on), `AppLink` (system search and Watch Next intents), `WatchNext` (home screen row) |
| `:feature:player` | `feature/player/` | `PlayerScreen`, `AndroidPlaybackCaps`, everything about playback |
| `:feature:subtitles` | `feature/subtitles/` | subtitle data, behaviour, overlay, timing |
| `:feature:browse` | `feature/browse/` | Home, Movies, TV Shows, Surprise |
| `:feature:detail` | `feature/detail/` | Detail page |
| `:feature:search` | `feature/search/` | Search (voice included; its manifest adds `RECORD_AUDIO` and the speech `<queries>`) |
| `:feature:account` | `feature/account/` | Setup, Profiles, Add Profile, Settings, opening Intro |

Rules for the feature modules:

- A feature module keeps its code in its own directory; shared code goes to `:core`, wiring to `:app`, libraries
  to the catalog (`gradle/libs.versions.toml`).
- Feature modules depend on `:core` only (`:feature:player` also on `:feature:subtitles`). Features never depend on
  each other otherwise.
- The public entry points below are what `:app` calls; everything else in a feature module is `internal`.

## Building and checking

Everything runs in Docker inside WSL; never run Gradle, adb or the SDK on the host and never start Gradle outside
these scripts (WSL has little RAM; the scripts allow one Gradle build at a time).

```
# From PowerShell (Git Bash rewrites /mnt/c paths):
wsl -d Ubuntu-24.04 -- env WORK_ID=player bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/check.sh :feature:player:compileDebugKotlin
wsl -d Ubuntu-24.04 -- env WORK_ID=player bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/check.sh :feature:player:testDebugUnitTest
wsl -d Ubuntu-24.04 -- env WORK_ID=player bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/check.sh assembleDebug
```

- `check.sh [tasks…]` (default `assembleDebug`) copies `tv/android` (read-only mount) into the Docker volume
  `nextflix-android-work-<WORK_ID>` and runs Gradle there, so each `WORK_ID` builds incrementally in its own
  build dirs. The default `WORK_ID` is `main`; use another one for a parallel incremental copy.
- It waits while another build holds the lock (`Waiting for another Gradle build…`), then prints only compiler
  errors (`e:` lines, `error:`), failed tasks and Gradle's "What went wrong", and `GRADLE_EXIT=<code>` last.
  `WARNINGS=1` also prints Kotlin warnings.
- Fast loop: `:feature:<name>:compileDebugKotlin`; before finishing: `assembleDebug testDebugUnitTest` (the whole
  app must build; JUnit 4 + kotlinx-coroutines-test are in the catalog).
- `scripts/build.sh` builds `out/nextflix-tv-<version>.apk` (release, R8, signed with the local key in
  `.signing/`) and `out/nextflix-tv-<version>-debug.apk`. Version: `nextflix.versionName` / `versionCode` in
  `gradle.properties`.
- `scripts/adb.sh` runs adb in a container with host networking (`adb.sh connect <tv-ip>:5555`,
  `adb.sh pair <ip:port> <code>`, `adb.sh install -r out/nextflix-tv-0.1.0.apk`, `adb.sh logcat`, `adb.sh stop`;
  see README.md).
- A live server answers at `http://127.0.0.1:8252` (also from the containers). You may `curl` read-only GET
  endpoints (catalog, profiles, progress, ratings, recommendations, top10, subtitles settings, probe) to see real
  JSON. Never call resolve, reuse, prefetch, play, release, instant or any POST / PUT / DELETE.

## Public entry points

All are `@Composable` and called by `app/src/main/kotlin/com/nextflix/tv/AppRoot.kt`.

| Module | File | Signature | Shown for |
|---|---|---|---|
| browse | `feature/browse/src/main/kotlin/com/nextflix/tv/feature/browse/BrowseScreens.kt` | `HomeScreen(nav: Navigator)` | `Route.Home` (tab) |
| browse | same | `MoviesScreen(nav: Navigator)` | `Route.Movies` (tab) |
| browse | same | `TvShowsScreen(nav: Navigator)` | `Route.TvShows` (tab) |
| browse | same | `SurpriseScreen(nav: Navigator)` | `Route.Surprise` |
| detail | `feature/detail/src/main/kotlin/com/nextflix/tv/feature/detail/DetailScreen.kt` | `DetailScreen(route: Route.Detail, nav: Navigator)` | `Route.Detail` |
| search | `feature/search/src/main/kotlin/com/nextflix/tv/feature/search/SearchScreen.kt` | `SearchScreen(nav: Navigator)` | `Route.Search` (tab) |
| account | `feature/account/src/main/kotlin/com/nextflix/tv/feature/account/AccountScreens.kt` | `SetupScreen(nav: Navigator)` | `Route.Setup` |
| account | same | `ProfilesScreen(nav: Navigator)` | `Route.Profiles` |
| account | same | `AddProfileScreen(route: Route.AddProfile, nav: Navigator)` | `Route.AddProfile` |
| account | same | `SettingsScreen(nav: Navigator)` | `Route.Settings` (tab) |
| account | same | `IntroScreen(onDone: () -> Unit, onZoom: (Boolean) -> Unit = {})` | over the first screen on a cold start; calls `onDone` once; `onZoom(true)` while the app should zoom from 1.1 to 1 (webOS `.intro-on .app`) |
| player | `feature/player/src/main/kotlin/com/nextflix/tv/feature/player/PlayerScreen.kt` | `PlayerScreen(request: PlayRequest, nav: Navigator)` | `Route.Player` |
| player | `feature/player/…/AndroidPlaybackCaps.kt` | `class AndroidPlaybackCaps(context: Context, devicePrefs: DevicePrefs) : PlaybackCapsProvider` (also `restrictToSafeAudio()`) | created by `NextflixApp`, exposed as `core.playbackCaps` |
| subtitles | `feature/subtitles/…/SubtitleOverlay.kt` | `SubtitleOverlay(cues: List<Cue>, position: () -> Double, style: SubtitleStyle, modifier: Modifier = Modifier, delaySeconds: Double = 0.0, raised: Boolean = false, sample: String? = null, endInset: Dp = 0.dp)` (`sample`: preview text while no cue shows; `endInset`: room for the appearance panel) | used by the player |
| subtitles | same | `SubtitleSample(text: String, style: SubtitleStyle, modifier: Modifier = Modifier)` (in a bounded box: a scaled small screen with dimming band and position; else one line) and `val SubtitleSampleRendererImpl: SubtitleSampleRenderer` | provided by the app as `LocalSubtitleSample` (Settings preview) |
| subtitles | `feature/subtitles/…/SubtitleController.kt` | `class SubtitleController(core: Core, scope: CoroutineScope)`: `state: StateFlow<SubtitleState>`, `events: SharedFlow<SubtitleEvent>` (`Missing(lang)`: ask subalt now; `Found(lang)`: show the found toast), `var log: SubtitleLogSink?` (`subs` / `subs-none` / `subdelay` into the playback log), `start(query, audioLang)` (every fresh open of a source), `restart(query, audioLang)` (seamless switch only; the old subtitle stays until the new list picks), `preferLanguage(lang)` (before switching to a subalt release), `select(id: String?)`, `setDelay(seconds): Double` (the kept value), `onPosition(seconds, playing?)` (`playing`: provisional timing is asked again only then), `onAudioLanguage(lang)`, `stop()` | used by the player |
| subtitles | `feature/subtitles/…/SubtitleRepository.kt` | `class SubtitleRepository(core: Core)`: `suspend list(query, refresh = false): List<SubtitleTrack>`, `suspend cues(id, at: Double? = null): CueFile`, poll constants | used by the controller |
| subtitles | `feature/subtitles/…/SubtitleModels.kt` | `SubtitleTrack` (`holes`, `isAi`), `SubtitleQuery` (`originalLanguage`; `SubtitleQuery.of(request, hash, index, language, torrentTitle)`), `Cue`, `CueFile` (`status`, `holes`), `SubtitleState` (`sync`, `selected`, `selectedLanguage`; `searching` = no list answer yet) | shared with the player |
| subtitles | `feature/subtitles/…/SubtitleTimingStore.kt`, `Languages.kt`, `SubtitleStrings.kt` | `SubtitleTimingStore(core)`: `get(hash, index)`, `set(hash, index, seconds)` (per file, newest 200); `Languages.norm(code)`, `Languages.name(language, code)`; `SubtitleTrack.menuLabel()` | timing per file; language names and labels for the audio / subtitle menus |

The KDoc on each entry point says what it does (ported from the named webOS files). The player owns video, the
Audio & Subtitles panel UI and the subalt switch (`/torrent-stream/subalt`); the subtitles module owns every
subtitle rule (list, retries, default pick, late uploads, cue loading and polling, timing store) behind
`SubtitleController`, and drawing (`SubtitleOverlay`). The player depends on the members listed above.

## Navigation (`core/…/nav`)

- `Route` (sealed, `@Serializable`): `Setup`, `Profiles`, `AddProfile(existing)`, `Home`, `Movies`, `TvShows`,
  `Search`, `Settings`, `Surprise`, `Detail(type, tmdbId, autoplay = false)`, `Player(request: PlayRequest)`.
  `Tab` = the side-menu routes (Search, Home, TV Shows, Movies, Settings); `route.tab`.
- `Navigator`: `current`, `push(route)`, `replace(route)`, `reset(route)`, `back(): Boolean`.
  `Navigator.Preview` for previews.
- Only the top screen is composed. Each screen has its own `ViewModelStore` (use `viewModel { MyVm(core) }`; the VM
  lives until the screen is popped) and saved state (`rememberSaveable` survives while the screen is below
  others). There is no `SavedStateHandle`.
- Focus restore on Back: put `Modifier.restorableFocus(key)` on focusable elements (before the focusable, e.g. on a
  `Surface`'s modifier; `ContentRow` does it for its cards) and call `FocusRestore(ready, fallbackKey)` once per
  screen. Nothing is focused automatically: every screen must give initial focus (FocusRestore fallback).
- `PlayRequest`: `type, tmdbId, title, year, season, episode, episodeTitle, runtimeMinutes, posterPath,
  backdropPath, originalLanguage, startAt (s), source: SavedSource(hash, index, magnet)?, seasons, cinema`;
  `nextEpisode(seasons)` gives the next episode's request; `StreamProgress.toPlayRequest()` builds a resume
  request (5 s before the position, saved source). Open the player with `nav.push(Route.Player(request))`.
- App shell (`AppRoot.kt`): start route `Setup` without a server, else `Profiles`. Tab screens get the side menu
  rail drawn over their left edge (closed 52 dp): keep content `Dimens.ScreenPadH` (64 dp) from the left; Left from
  the leftmost element enters the rail, which always focuses the active tab. Back: previous screen; tab root ->
  Home; Home -> rail, then the exit dialog; other roots -> exit dialog. A screen that needs Back (closing a panel)
  adds its own `BackHandler`, which runs before the app's. The screen stays on while `Route.Player` shows.
- Outside intents (`app/.../AppLink.kt`): `ACTION_SEARCH` / `MEDIA_PLAY_FROM_SEARCH` put the query in
  `core.pendingSearch` and reset to `Route.Search` (Profiles first without a profile). A Watch Next card
  (`nextflix://open?profile=&type=&id=&season=&episode=&play=1`, explicit intent) switches to its profile and
  opens Home > Detail > Player (the Continue Watching entry as a resume request). No intro then.
- Watch Next (`app/.../WatchNext.kt`): the active profile's Continue Watching list on the home screen
  (`androidx.tvprovider`), synced on profile change, 30 s after a progress save and when the activity stops.
- Progress / ratings changes: `core.progress.version` and `core.ratings.version` grow after each save; screens
  reload what depends on them when they come back (compare with the version they loaded).

## Core API (`core/src/main/kotlin/com/nextflix/tv/core`)

`Core` (`LocalCore.current` or `core()` in composables; `context.core()` elsewhere):

| Member | What |
|---|---|
| `session: Session` | `current` (`SessionState(server, profile)`, `.language`), `state` flow, `server`, `profile`, `profileId`, `language`; `suspend setServer(url)` (normalized), `suspend setProfile(profile)` |
| `api: ApiClient` | `get<T>(path, query)`, `getJson`, `post<T>(path, body, query)`, `put<T>`, `delete`, `call(method, path, query, body): JsonElement`, `raw(...)`: `Response` (caller closes; text bodies, headers), `beacon(path, query)` (fire-and-forget POST), `url(path, query): HttpUrl`, `body(value)`, `checkServer(baseUrl): version`. Adds `X-Lang` + `X-User-ID` to /api calls; throws `ApiException` (`NoServer`, `Network`, `Http(status, message, body)`, `Decode`); `Throwable.userMessage(strings())` |
| `okHttp: OkHttpClient` | shared client (8 s connect, 75 s read); derive with `newBuilder()` (Media3 `OkHttpDataSource.Factory(core.okHttp)`) |
| `cache: AnswerCache` | `scoped(key)`, `get(key, ttl, keep, load)`, `peek`, `invalidate(match)` |
| `profiles: ProfileRepository` | `list()`, `create(ProfileInput)`, `update(id, input)`, `delete(id)` |
| `catalog: CatalogRepository` | `homeFeeds()`, `top10(region, type)`, `recommendations(pid)`, `favorites(pid)`, `popularMovies()`, `discoverMovies(params)`, `providerTv(id, sortBy)`, `details(id, type)`, `movieLogo(id, lang)`, `related(id, type)`, `loadSeasonEpisodes(tvId, season)` (throws), `seasonEpisodes(...)` (placeholders on failure), `tvSeasons(tvId)`, `availability(type, id, season, episode)`, `invalidate(match)` |
| `progress: ProgressRepository` | `continueWatching(pid)`, `forTitle(pid, type, id)` (newest first), `item(...)`, `save(progress)`, `delete(pid, type, id)`, `version` |
| `ratings: RatingsRepository` | `get(pid, type, id)`, `setRating(pid, target, value?)`, `setFavorite(pid, target, favorite)`, `version` |
| `instant: InstantRepository` | `observe(InstantItem)`, `peek(key)`; composable `rememberInstant(item)` |
| `warmup: StreamWarmup` | `prefetch(ResolveParams, next = position to duration)`, `release(params, delaySec)` |
| `playbackCaps: PlaybackCapsProvider` | `current(): PlaybackCaps` (`query(stream)`), `invalidate()`, `restrictToSafeAudio(): Boolean` (the TV refused audio it claimed: AAC / Opus only from now on) |
| `devicePrefs: DevicePrefs` | `settings: StateFlow<DeviceSettings(hdrMode, introAnimation, introSound)>` (defaults until loaded), `suspend current()` (read from disk), setters |
| `playerPrefs: PlayerPrefsStore` | per profile `PlayerPrefs(audioLang, subtitle: SubtitleChoice?, subtitleStyle, autoplayNext)`: `get(pid)`, `all`, `suspend update(pid) { … }` |
| `pendingSearch: PendingSearch` | a query from system search for the Search screen: `pending` flow, `offer(text)`, `take()` |
| `imageUrl(path, size)` | `<server>/tmdb-img/t/p/<size><path>` (`TmdbImages.W342`, `W500`, `W780`, `W1280` …). Never use image.tmdb.org |

Models (`core.model`): `MediaType`, `Profile`, `ProfileInput`, `Title`, `Details`, `SeasonInfo`, `EpisodeInfo`,
`HomeFeeds`, `RecommendationRow`, `Top10Region`, `StreamProgress` (`isFinished`, `fraction`, `remaining`),
`Rating`, `RatingValue`, `RatingTarget`, `Availability`; helpers `nextEpisodeOf`, `List<StreamProgress>.find`,
`JsonElement.toTitles / toTitle / toDetails / toEpisodes / toAvailability`, `yearOf`, `matchPercent`.
Streaming (`core.stream`): `PlaybackCaps`, `PlaybackCapsProvider`, `ResolveParams` (`query()`),
`PlayRequest.resolveParams(maxq)`, `QualityCap`. Prefs (`core.prefs`): `SubtitleStyle` (+ `Option`, `cycle`,
`valueLabel`, `optionLabel`, `subtitleStyleStrings`), `HdrMode`, `SubtitleChoice`.

UI (`core.ui`):

- Theme: `NextflixTheme`, `NfColors`, `NfType` (Inter: regular, semibold, extrabold), `Dimens` (1 webOS rem = 8 dp;
  `ScreenPadH` 64 dp, `ScreenPadTop` 27 dp, card sizes), Material typography set to Inter.
- Focus (`ui.focus`): `restorableFocus(key)`, `FocusRestore(ready, fallbackKey, fallback)`, `FocusRequester.tryFocus()`,
  `Modifier.onKeyDown { }` / `onPreviewKeyDown { }`, `Keys` (Select, Back, media keys), `KeyEvent.repeatCount`.
- Components (`ui.components`): `NfButton` (`ButtonStyle.Primary / Secondary / Ghost`, `shape`, `padding`),
  `RoundIconButton`, `NfChip`, `FocusCard`, `BackdropCard`, `PosterCard`, `RankCard`, `ContinueCard`, `CardImage`,
  `CardSkeleton`, `InstantBadge`, `rememberInstant`, `ProgressBar`, `ContentRow` (titled lazy row with focus
  restore and start-aligned scrolling), `rememberStartAlignedBringIntoView`, `Backdrop` (hero image with shades),
  `FadeInImage(url)` (loads first, then fades in over the previous image), `SideNav` (used by the app), `Avatar`,
  `AvatarKeys`, `ProfileColors`, `profileColor`, `Spinner`, `LoadingBox`, `ErrorMessage` (with Retry),
  `ConfirmDialog`, `Wordmark`, `OnScreenKeyboard`
  (`KeyboardLayout.Search / Url / Text`, voice key, `keyHeight`), `KeyboardField`, `LocalSubtitleSample` (the
  subtitles module's `SubtitleSample`, provided by the app).
- Icons (`ui.icons`): `Lucide.*` (House, Search, Tv, Film, Settings, Shuffle, Play, Pause, Info, Plus, Check, X,
  chevrons, ArrowLeft, Delete, Mic, RotateCcw / RotateCw, SkipForward, ThumbsUp / ThumbsDown, Heart, CircleAlert,
  CalendarClock, Clapperboard, MessageSquare, Type, Zap, Captions, Languages, Volume2, Server, Pencil, Trash2,
  LoaderCircle, Clock, ListPlus and the avatar icons), `ImageVector.filled()`, `DoubleThumbsUp(color, filled)`.
  More icons: add them to `tools/lucide.sh` and regenerate.

## i18n

The UI language follows the active profile (`en` / `tr`), never the device locale: `LocalAppLanguage.current`
(`AppLanguage.En / Tr`, `.locale`). Every user-visible string needs English and Turkish.

- Shared strings: `strings()` (`CoreStrings`: Cancel, Retry, Back, nav labels, Play, Resume, errors,
  availability texts, `match(n)`, `episodeShort(s, e)`, `timeLeft`, `seasons(n)`, `genre(tmdbGenreId)`, …).
- Module strings: declare an `internal interface XStrings`, implement it in `internal object XEn` / `XTr` (the
  compiler checks Turkish is complete), `internal val xStrings = Localized<XStrings>(en = XEn, tr = XTr)`, read
  with `xStrings.current()`. Copy the texts from `tv/webos/src/lib/i18n.ts`.
- Formatting: `Formats.duration(language, minutes)`, `Formats.date(language, "YYYY-MM-DD")`,
  `Formats.episode(language, s, e)`, `Formats.clock(seconds)`, `AppLanguage.plural(count, one, other)`.

## Conventions

- Comments in English, short, only where needed. Idiomatic Kotlin, no AI-slop comments.
- State in ViewModels (`viewModel { …(core) }`), `StateFlow` + `collectAsStateWithLifecycle()`; no global
  singletons besides `Core`.
- Data a screen loaded stays in its ViewModel while the screen is below others, so Back paints at once.
- No secrets in code, nothing that runs shell commands or exposes the device filesystem.
