package com.nextflix.tv.feature.player

import android.os.SystemClock
import android.util.Log
import androidx.annotation.OptIn
import androidx.media3.common.C
import androidx.media3.common.MimeTypes
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.Tracks
import androidx.media3.common.VideoSize
import androidx.media3.common.util.UnstableApi
import androidx.media3.exoplayer.ExoPlaybackException
import androidx.media3.exoplayer.ExoPlayer
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.model.Availability
import com.nextflix.tv.core.model.AvailabilityStatus
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.find
import com.nextflix.tv.core.model.nextEpisodeOf
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.nav.SavedSource
import com.nextflix.tv.core.prefs.PlayerPrefs
import com.nextflix.tv.core.prefs.SubtitleStyle
import com.nextflix.tv.core.stream.QualityCap
import com.nextflix.tv.core.stream.ResolveParams
import com.nextflix.tv.core.stream.resolveParams
import com.nextflix.tv.feature.subtitles.SubtitleController
import com.nextflix.tv.feature.subtitles.SubtitleEvent
import com.nextflix.tv.feature.subtitles.SubtitleLogSink
import com.nextflix.tv.feature.subtitles.SubtitleQuery
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import kotlin.math.abs
import kotlin.math.floor
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt

/**
 * Playback of one Player screen (port of webOS player/engine.ts) on Media3 ExoPlayer.
 *
 * The stream is a live fragmented MP4 requested at `t` that really starts at `offset` (/seekpoint), so positions
 * are offset + the player's time (file time everywhere). Seeks outside the buffer restart the stream. ExoPlayer plays
 * muxed streams, so split streams are not used, and every source or audio track switch is a new stream at the
 * position (no MSE splice): an upgrade restarts early in playback or waits for the next seek, a faster source found
 * while the buffer drains is held for a real stall.
 *
 * Runs on the main thread. [destroy] saves progress and releases everything.
 */
@OptIn(UnstableApi::class)
internal class PlayerEngine(private val core: Core, request: PlayRequest) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val api = StreamApi(core)
    private val media = StreamMediaFactory(core.okHttp)
    private val language: AppLanguage = core.session.language
    private val pid: Int? = core.session.profileId

    val exo: ExoPlayer = createStreamPlayer(core.appContext)
    val subtitles = SubtitleController(core, scope)
    val thumbs = Thumbs(core, scope)

    private val _state = MutableStateFlow(PlayerState(meta = request))
    val state: StateFlow<PlayerState> = _state.asStateFlow()
    private val s: PlayerState get() = _state.value
    private var destroyed = false

    // current item
    private var meta = request
    /** where the request asks to start; null: the saved progress decides */
    private var startAt: Double? = request.startAt.takeIf { request.source != null || it > 0 }
    /** where the item starts (saved progress included): a retry before any position was shown resumes here */
    private var resumePos = 0.0
    private var given: SavedSource? = request.source
    private var seasons: List<SeasonInfo> = request.seasons

    // source
    private var origin = Origin.Given
    private var source: StreamSource? = null
    private var autoRefreshes = 0
    private val avoid = ArrayList<String>()
    private var lengthRetries = 0
    private var cap: QualityCap? = null
    private val triedCaps = ArrayList<QualityCap>()
    private var downgrades = 0
    private var upgraded = false
    private var lastSidegrade = 0L
    private var busy = false
    private val health = BufferMonitor()
    private var sideNext = 0L
    private var sideDelay = PROACTIVE_POLL_MS
    /** a faster source held for a real stall */
    private var pendingSide: StreamSource? = null
    private var lastSwitch = 0L
    /** torrents left for being slow: never switched to again */
    private val slow = HashSet<String>()
    private var resolveJob: Job? = null
    private var resolveTick: Job? = null
    private var videoHeight = 0

    // stream of the current source
    private var session = 0
    private var hash = ""
    private var index = 0
    private var offset = 0.0
    private var exact = true
    private var seekSeq = 0
    private var seekJob: Job? = null
    /** an upgrade that waits for the next seek (a restart anyway) */
    private var deferredUpgrade: Pair<StreamSource, Msg>? = null
    private var realStallTimer: Job? = null
    private var stallConfirmTimer: Job? = null
    private var hasPlayed = false
    private var everPlayed = false
    /** any source of this item showed frames (everPlayed is per source) */
    private var itemPlayed = false
    private var recoveries = 0
    private var stallTimer: Job? = null
    private var stallArmed = false
    private var pausedAt = 0L
    private var keepAliveTimer: Job? = null
    private var resumedAt = 0L
    private var stallResetTimer: Job? = null
    private var stall: Stall? = null
    private val stallTimes = ArrayList<Long>()
    private var longStallTimer: Job? = null
    private var hold: Hold? = null
    private var holdTimer: Job? = null
    private var holdSource: StreamSource? = null
    private val pollTimers = ArrayList<Job>()
    /** a seek inside the buffer is running (its target, for a restart when Media3 cannot do it) */
    private var seekTarget: Double? = null
    /** file and position of the last trick play prefetch */
    private var lastThumbs = ""

    // background (Home): nothing plays, decodes or downloads while the app is hidden
    private var background = false
    /** the stream stopped in the background at this position; Play / OK / a seek starts it again */
    private var parkedAt: Double? = null
    /** the last startItem / openSource call, repeated when the background interrupted it */
    private var lastLoad: (() -> Unit)? = null
    private var resumeLoad: (() -> Unit)? = null

    // subtitles (list, picks, cues, timing and their log events live in :feature:subtitles)
    private var subAltSource: StreamSource? = null

    // session wide
    private val played = LinkedHashSet<String>()
    private var current = ""
    private var nextWarm: ResolveParams? = null
    // the next episode's subtitle timed before it opens; cancelled when the player moves on
    private var nextPretime: Job? = null
    private var countdownTimer: Job? = null
    private var toastTimer: Job? = null
    private val plog: PlaybackLog

    private val listener = object : Player.Listener {
        override fun onPlaybackStateChanged(playbackState: Int) {
            if (destroyed) return
            when (playbackState) {
                Player.STATE_BUFFERING -> if (exo.playWhenReady) onWaiting()
                Player.STATE_READY -> if (seekTarget != null) seekDone()
                Player.STATE_ENDED -> onEnded()
                else -> Unit
            }
        }

        override fun onIsPlayingChanged(isPlaying: Boolean) {
            if (!destroyed && isPlaying) onPlaying()
        }

        override fun onPlayWhenReadyChanged(playWhenReady: Boolean, reason: Int) {
            // Our own calls are handled where they are made; this is audio focus loss or unplugged audio
            if (destroyed || reason == Player.PLAY_WHEN_READY_CHANGE_REASON_USER_REQUEST) return
            if (playWhenReady) onPlayEvent() else onPauseEvent()
        }

        override fun onPlayerError(error: PlaybackException) {
            if (!destroyed) onError(error)
        }

        override fun onTracksChanged(tracks: Tracks) {
            if (!destroyed) checkTracks(tracks)
        }

        override fun onVideoSizeChanged(videoSize: VideoSize) {
            if (videoSize.width > 0 && videoSize.height > 0) {
                set { copy(aspect = videoSize.width * videoSize.pixelWidthHeightRatio / videoSize.height) }
            }
        }
    }

    init {
        exo.addListener(listener)
        plog = PlaybackLog(core, logProbe(), ::logContext) {
            mapOf("subdelay" to subtitles.state.value.takeIf { it.selectedId != null }?.delaySeconds)
        }
        every(10_000) { if (s.playing) persist() }
        every(1000) { sampleHealth() }
        var ticks = 0
        every(PlaybackLog.TICK_MS) {
            plog.tick()
            onTime()
            if (++ticks % 4 == 0 && s.phase == Phase.Ready && hash.isNotEmpty()) subtitles.onPosition(positionNow(), exo.isPlaying)
        }
        subtitles.log = SubtitleLogSink { event, fields -> plog.note(event, fields) }
        scope.launch { subtitles.events.collect { onSubtitleEvent(it) } }
        loadSeasons()
        startItem()
    }

    // ---- state ----

    private inline fun set(change: PlayerState.() -> PlayerState) {
        if (!destroyed) _state.value = s.change()
    }

    private fun toast(msg: Msg) {
        toastTimer?.cancel()
        set { copy(toast = msg) }
        toastTimer = after(TOAST_MS) { set { copy(toast = null) } }
    }

    private fun now() = SystemClock.elapsedRealtime()

    private fun after(ms: Long, block: () -> Unit): Job = scope.launch {
        delay(ms)
        block()
    }

    private fun every(ms: Long, block: () -> Unit): Job = scope.launch {
        while (true) {
            delay(ms)
            block()
        }
    }

    private fun poll(ms: Long, block: suspend () -> Unit) {
        pollTimers += scope.launch {
            delay(ms)
            block()
        }
    }

    private fun stale(session: Int) = destroyed || session != this.session

    private fun params(maxq: QualityCap? = cap): ResolveParams = meta.resolveParams(maxq)

    private fun prefs(): PlayerPrefs = core.playerPrefs.get(pid)

    private fun updatePrefs(change: (PlayerPrefs) -> PlayerPrefs) {
        core.scope.launch { core.playerPrefs.update(pid, change) }
    }

    private val hasMedia: Boolean get() = exo.currentMediaItem != null

    /** Exact playback position in the file (seconds). */
    fun positionNow(): Double = max(0.0, offset + (if (hasMedia) exo.currentPosition else 0L) / 1000.0)

    /** Seconds buffered ahead of the position. */
    private fun ahead(): Double = if (hasMedia) max(0.0, (exo.bufferedPosition - exo.currentPosition) / 1000.0) else 0.0

    private fun logContext(): LogContext {
        val m = meta
        return LogContext(
            type = m.type.apiName, tmdb = m.tmdbId, season = m.season, episode = m.episode, title = m.title,
            src = if (hash.isNotEmpty()) "${hash.take(8)}:$index" else "",
            debrid = source?.debrid == true, quality = s.quality?.label.orEmpty(), duration = s.duration.roundToInt(),
        )
    }

    private fun logProbe() = object : LogProbe {
        override val mediaTime get() = if (hasMedia) exo.currentPosition / 1000.0 else 0.0
        override val position get() = positionNow()
        override val ahead get() = ahead()
        override val running get() = exo.isPlaying && seekTarget == null
        override val playbackState get() = exo.playbackState
        override val loading get() = exo.isLoading
        override val droppedFrames get() = exo.videoDecoderCounters?.let { it.ensureUpdated(); it.droppedBufferCount } ?: -1
        override val renderedFrames get() = exo.videoDecoderCounters?.let { it.ensureUpdated(); it.renderedOutputBufferCount } ?: -1
        override val videoWidth get() = exo.videoSize.width
        override val videoHeight get() = exo.videoSize.height
    }

    // ---- item: resolve the source ----

    /** [fresh]: resolve (with avoid) even though a saved or given source exists. */
    private fun startItem(refresh: Boolean = false, fresh: Boolean = false, at: Double? = null, wrongLength: Boolean = false) {
        cancelResolve()
        val startPos = at ?: startAt
        lastLoad = { startItem(refresh, fresh, startPos, wrongLength) }
        stopStream()
        subtitles.stop()
        session++
        val t0 = now()
        fun stage(ms: Long): Msg = when {
            wrongLength -> Msg.FindingComplete
            ms < 4000 -> if (refresh) Msg.TryingAnother else Msg.FindingSource
            ms < 12_000 -> Msg.SearchingIndexers
            else -> Msg.CheckingSources
        }
        set { copy(phase = Phase.Resolving, resolveElapsedMs = 0, stage = stage(0), error = null, unreleased = null, fatal = null) }
        // Published on a new stage or second (and once past 300 ms, when the status text shows)
        resolveTick = every(250) {
            val ms = now() - t0
            val prev = s.resolveElapsedMs
            if (stage(ms) != s.stage || ms / 1000 != prev / 1000 || (prev < 300 && ms >= 300)) {
                set { copy(resolveElapsedMs = ms, stage = stage(ms)) }
            }
        }
        val m = meta
        resolveJob = scope.launch {
            try {
                var start = startPos ?: 0.0
                // Reuse the torrent saved with earlier progress instead of searching again
                var saved: SavedSource? = null
                var from = Origin.Given
                if (!refresh && !fresh) {
                    val g = given
                    if (g != null) {
                        saved = g
                    } else if (pid != null) {
                        val entries = core.progress.forTitle(pid, m.type, m.tmdbId)
                        val p = if (m.type == MediaType.Tv) entries.find(m.season ?: 0, m.episode ?: 0) else entries.find()
                        if (p != null && p.hash.isNotEmpty()) {
                            saved = SavedSource(p.hash, p.fileIndex, p.magnet.ifEmpty { null })
                            from = Origin.Saved
                            start = startAt ?: if (p.isFinished) 0.0 else max(0.0, p.position - 5)
                        }
                    }
                }
                resumePos = start
                if (saved != null) {
                    // TorBox when it has the release cached, the torrent when its swarm keeps up at the resume
                    // position, else a fresh resolve without it
                    val a = api.reuse(params(), saved, start)
                    if (a == null || a.play) {
                        finishResolve()
                        openSource(a?.source?.toSource() ?: saved.toSource(), start, from)
                        return@launch
                    }
                    val h = saved.hash.lowercase()
                    Log.i(TAG, "[restart] saved source ${h.take(8)} not reused (${a.reason ?: "slow"}): resolving again")
                    if (h !in avoid) avoid += h
                }
                val r = api.resolve(params(), refresh = refresh, avoid = avoid, t = start)
                finishResolve()
                openSource(r.toSource(), start, if (r.cached) Origin.Cached else Origin.Fresh)
            } catch (e: CancellationException) {
                throw e
            } catch (e: StreamError) {
                finishResolve()
                if (e.status == 425) {
                    set { copy(phase = Phase.Unreleased, unreleased = e.availability ?: Availability(AvailabilityStatus.NotReleased)) }
                    return@launch
                }
                val error = when {
                    e.status == 404 || e.message.isNullOrEmpty() -> PlayerError.Key(Msg.NoSourceHint)
                    e.status == 0 -> PlayerError.Key(Msg.Network)
                    else -> PlayerError.Text(e.message.orEmpty())
                }
                set { copy(phase = Phase.Error, error = error) }
            } catch (e: Exception) {
                finishResolve()
                set { copy(phase = Phase.Error, error = PlayerError.Key(Msg.NoSourceHint)) }
            }
        }
    }

    private fun finishResolve() {
        resolveTick?.cancel()
        resolveTick = null
        resolveJob = null
    }

    private fun cancelResolve() {
        resolveJob?.cancel()
        finishResolve()
    }

    /** A failed source is resolved again (up to MAX_AUTO_REFRESH times per item); a fresh one is avoided. */
    private fun sourceFailed(position: Double, auto: Boolean): Boolean {
        if (auto && autoRefreshes >= MAX_AUTO_REFRESH) return false
        if (auto) {
            autoRefreshes++
            // A fresh source that failed is not picked again; a reused one gets a fresh resolve
            val failed = source?.hash?.lowercase()
            if (origin == Origin.Fresh && failed != null && failed !in avoid) avoid += failed
        }
        startItem(refresh = true, at = position)
        return true
    }

    // ---- source: probe, then stream ----

    private fun openSource(src: StreamSource, startAtIn: Double, from: Origin, reason: String = "open") {
        val prev = source?.takeIf { !it.hash.equals(src.hash, ignoreCase = true) }?.let { Previous(it, origin, s.duration) }
        // Sent first: queued events belong to the previous source's context
        plog.flush()
        plog.note("open", mapOf("why" to reason, "origin" to from.name.lowercase(), "to" to "${src.hash.take(8)}:${src.index}", "at" to startAtIn, "debrid" to src.debrid))
        lastLoad = { openSource(src, startAtIn, from, reason) }
        stopStream()
        val session = ++this.session
        // Counted from the open: a slow probe (the header from a weak swarm) counts too
        if (!src.debrid) poll(SLOW_START_MS) { slowStart(session) }
        source = src
        origin = from
        val h = src.hash.lowercase()
        // A replaced torrent (upgrade, sidegrade, step down) is dropped at once
        if (current.isNotEmpty() && current != h) {
            api.releaseTorrents(listOf(current))
            played -= current
        }
        current = h
        played += h
        hash = h
        index = src.index
        val hevc = core.playbackCaps.current().hevc
        set {
            copy(
                phase = Phase.Ready, error = null, unreleased = null, fatal = null, ended = false, buffering = true, reconnecting = false,
                status = Msg.Connecting, duration = 0.0, position = startAtIn, audioTracks = emptyList(), audio = 0,
                quality = qualityOf(null, src, hevc), audioBadge = "", debrid = src.debrid, cinema = src.cinema,
                file = PlayingFile(h, src.index),
            )
        }
        scope.launch {
            var start = startAtIn
            // TorrServer may have dropped a saved torrent since
            src.magnet?.let {
                api.files(it)
                if (stale(session)) return@launch
            }
            set { copy(status = Msg.ReadingInfo) }
            val probe = try {
                api.probe(h, src.index, meta)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                if (stale(session)) return@launch
                if (!sourceFailed(start, true)) {
                    set { copy(fatal = e.message?.takeIf { it.isNotEmpty() }?.let { PlayerError.Text(it) } ?: PlayerError.Key(Msg.CouldNotRead)) }
                }
                return@launch
            }
            if (stale(session)) return@launch
            // Not the whole title (e.g. 86 of 173 minutes): look for a complete version
            if (probe.durationMismatch && lengthRetries < MAX_LENGTH_RETRIES) {
                lengthRetries++
                avoid += h
                startItem(refresh = true, at = start, wrongLength = true)
                return@launch
            }
            val duration = probe.duration
            val tracks = probe.audioTracks
            val switching = reason != "open" && reason != "revert" && prev != null
            // Another cut of the title (a WEB-DL vs a BluRay): the same moment, mapped by the lengths
            if (switching && prev.duration > 0 && duration > 0 && lengthsMatch(prev.duration, duration)) {
                start *= timeScale(prev.duration, duration)
            }
            // A switch never starts over: a file that does not reach the position (a pack's wrong file, another
            // cut) sends the player back to the source it came from (its torrent is kept 60 s after its stream)
            if (switching && start > 30 && duration > 0 &&
                (start >= duration - 10 || (prev.duration > 0 && abs(duration - prev.duration) > 60))
            ) {
                Log.i(TAG, "[restart] guard: ${h.take(8)}:${src.index} lasts ${duration.roundToInt()} s, position ${start.roundToInt()}: back to ${prev.src.hash.take(8)}")
                api.releaseTorrents(listOf(h))
                openSource(prev.src, start, prev.origin, "revert")
                return@launch
            }
            // Resumes (Continue Watching) carry no original language; the default track needs it
            if (meta.originalLanguage == null && tracks.size > 1) {
                val m = meta
                val lang = withTimeoutOrNull(1500) { quiet { core.catalog.details(m.tmdbId, m.type).originalLanguage } }
                if (stale(session)) return@launch
                if (!lang.isNullOrEmpty() && meta === m) {
                    meta = m.copy(originalLanguage = lang)
                    set { copy(meta = meta) }
                }
            }
            val audio = pickAudioTrack(tracks, language.code, meta.originalLanguage, prefs().audioLang)
            set {
                copy(
                    duration = duration, audioTracks = tracks, audio = audio, quality = qualityOf(probe.video, src, hevc),
                    audioBadge = audioBadge(tracks.firstOrNull { it.index == audio }, src.atmos), status = Msg.Buffering,
                )
            }
            videoHeight = probe.video?.let { videoQuality(it.width, it.height) } ?: 0
            val at = openStart(start, duration, reason == "open" && !itemPlayed)
            if (at == null) {
                Log.i(TAG, "[restart] $reason at ${start.roundToInt()} s of ${duration.roundToInt()}: the title is over")
                stopStream()
                set { copy(buffering = false, position = duration) }
                finish()
                return@launch
            }
            loadSubtitles()
            restartAt(at, reason)
            scheduleUpgrade(session)
            if (src.starving) scheduleSidegrade(session, start)
        }
    }

    /**
     * A torrent without a first frame SLOW_START_MS after it opened: a sidegrade that keeps up, else resolve again
     * without it at the same position, instead of restarting the same swarm for minutes. TorBox keeps the usual restarts.
     */
    private suspend fun slowStart(session: Int) {
        val src = source
        if (stale(session) || everPlayed || s.fatal != null || src == null || src.debrid || autoRefreshes >= MAX_AUTO_REFRESH) return
        val pos = positionNow().takeIf { it > 0 } ?: s.position
        val h = current
        Log.i(TAG, "[restart] no frame ${SLOW_START_MS / 1000} s after the start of ${h.take(8)}: trying another source")
        slow += h
        if (trySidegrade(videoHeight, restart = true)) return
        if (stale(session) || everPlayed) return
        autoRefreshes++
        if (h !in avoid) avoid += h
        startItem(fresh = true, at = pos)
    }

    /** Restarts the stream at [target]; /seekpoint tells where it really starts (copied video starts on a keyframe). */
    private fun restartAt(target: Double, reason: String = "restart") {
        if (hash.isEmpty()) return
        if (parkedAt != null) {
            parkedAt = null
            loadSubtitles(again = true)
        }
        // Live tracks are read from here on; the 1 s tick only catches up later
        subtitles.onPosition(target)
        health.reset()
        val seq = ++seekSeq
        seekJob?.cancel()
        seekJob = null
        seekTarget = null
        beginStall(true)
        disarmStall()
        realStallTimer?.cancel()
        Log.i(TAG, "[restart] $reason t=${"%.1f".format(target)} ${hash.take(8)}:$index")
        plog.note("restart", mapOf("why" to reason, "at" to target, "mode" to "muxed", "recoveries" to recoveries))
        plog.onEmptied()
        offset = target
        exact = target <= 0
        hasPlayed = false
        set { copy(buffering = true, ended = false, position = target) }
        val length = if (s.duration > 0) s.duration - target + 60 else 6 * 3600.0
        exo.setMediaSource(media.create(api.playUrl(hash, index, target, s.audio), seq.toString(), length))
        exo.prepare()
        // A borderline or unmeasured swarm starts only with a larger buffer; only the first stream of a source holds
        clearHold()
        val src = source
        if (background) {
            set { copy(playing = false) }
        } else if (holdSource !== src && needsStartHold(src?.ratio) && src?.debrid != true) {
            holdSource = src
            startHold(HoldKind.Start)
        } else {
            videoPlay()
        }
        armStall()

        // Until the answer comes positions (and subtitles) run up to one keyframe interval ahead. An unknown start
        // (ffprobe waiting for pieces of a slow swarm) is asked again once the stream's data is cached. The answer is
        // the file time of the stream's time 0 (slightly negative at t=0: the first decode time).
        val audio = s.audio
        seekJob = scope.launch {
            var attempt = 0
            var t: Double?
            while (true) {
                t = api.seekPoint(hash, index, target, audio, timeoutMs = 15_000)
                if (seq != seekSeq || destroyed) return@launch
                if (t != null || attempt >= SEEKPOINT_RETRY_MS.size) break
                delay(SEEKPOINT_RETRY_MS[attempt])
                if (seq != seekSeq || destroyed) return@launch
                attempt++
            }
            if (t == null) Log.i(TAG, "[restart] no seek point for t=${"%.1f".format(target)}: positions may be off by a keyframe interval")
            plog.note("seekpoint", mapOf("at" to target, "fix" to (t?.let { it - target } ?: "none"), "attempt" to attempt))
            offset = t ?: target
            exact = true
        }
    }

    private fun stopStream() {
        clearHold()
        disarmStall()
        stallResetTimer?.cancel()
        longStallTimer?.cancel()
        pollTimers.forEach { it.cancel() }
        pollTimers.clear()
        seekJob?.cancel()
        seekJob = null
        seekSeq++
        stall = null
        stallTimes.clear()
        hasPlayed = false
        everPlayed = false
        recoveries = 0
        health.reset()
        // A held sidegrade belongs to the stream it was meant to replace
        pendingSide?.let {
            api.releaseTorrents(listOf(it.hash))
            pendingSide = null
        }
        videoPause()
        detach()
        // The pause above starts it; without a stream there is nothing to keep alive
        keepAliveTimer?.cancel()
        parkedAt = null
    }

    /** Stops the download and empties the player. */
    private fun detach() {
        seekTarget = null
        if (hasMedia) {
            exo.stop()
            exo.clearMediaItems()
            plog.onEmptied()
        }
    }

    // ---- player events (the <video> events of the webOS engine) ----

    /** play(): like the element's, fires the play handler and a wait when nothing can play yet. */
    private fun videoPlay() {
        if (!exo.playWhenReady) {
            exo.playWhenReady = true
            onPlayEvent()
        }
        if (exo.playbackState == Player.STATE_BUFFERING) onWaiting()
    }

    private fun videoPause() {
        if (exo.playWhenReady) {
            exo.playWhenReady = false
            onPauseEvent()
        }
    }

    private fun onPlayEvent() {
        set { copy(playing = true) }
        keepAliveTimer?.cancel()
        if (pausedAt != 0L && now() - pausedAt >= LONG_PAUSE_MS) resumedAt = now()
        pausedAt = 0
    }

    private fun onPauseEvent() {
        if (destroyed || hold != null) return // paused by the buffer hold, not the viewer
        set { copy(playing = false) }
        persist()
        plog.onPause()
        if (exo.playbackState == Player.STATE_ENDED) return
        pausedAt = now()
        keepAliveTimer?.cancel()
        keepAliveTimer = every(KEEPALIVE_MS) { if (hash.isNotEmpty()) api.keepAlive(hash, index) }
    }

    private fun onWaiting() {
        plog.onWaiting(seekTarget != null)
        // A real stall (still waiting with under 1 s buffered after MIN_STALL_MS) with a faster source in hand:
        // switch to it instead of waiting the stall out
        realStallTimer?.cancel()
        if (hasPlayed && seekTarget == null) {
            realStallTimer = after(MIN_STALL_MS) {
                if (s.buffering && ahead() < 1 && hold?.kind != HoldKind.Start && pendingSide != null) usePending("stall")
            }
        }
        if (!hasPlayed) set { copy(buffering = true) }
        // Not re-armed by every wait: a stall hold ends every few seconds, which kept the watchdog from ever
        // restarting a stream that delivers nothing
        if (!stallArmed) armStall()
        if (hasPlayed) {
            beginStall(false)
            confirmStall()
        }
    }

    private fun onPlaying() {
        stallConfirmTimer?.cancel()
        set { copy(buffering = false, reconnecting = false) }
        disarmStall()
        endStall()
        hasPlayed = true
        everPlayed = true
        itemPlayed = true
        if (seekTarget != null) seekDone()
        plog.onPlaying()
        stallResetTimer?.cancel()
        stallResetTimer = after(60_000) { recoveries = 0 }
    }

    private fun seekDone() {
        seekTarget = null
        plog.onSeeked()
    }

    private fun onTime() {
        if (destroyed || s.phase != Phase.Ready || !hasMedia || s.ended) return
        if (seekTarget != null && exo.playbackState == Player.STATE_READY) seekDone()
        val position = positionNow()
        // The UI shows whole seconds (exact time: positionNow)
        if (floor(position) != floor(s.position)) set { copy(position = position) }
        val m = meta
        val next = s.next
        if (m.type == MediaType.Tv && next != null && nextWarm == null && nextWarmDue(position, s.duration)) {
            val nextMeta = m.copy(season = next.first, episode = next.second, episodeTitle = null, runtimeMinutes = null)
            val p = nextMeta.resolveParams(cap)
            nextWarm = p
            core.warmup.prefetch(p, next = position to s.duration)
            pretimeNext(p, nextMeta)
        }
    }

    /** Once the next episode's source is known, the subtitle the open would pick there is timed before it opens. */
    private fun pretimeNext(params: ResolveParams, next: PlayRequest) {
        nextPretime?.cancel()
        nextPretime = scope.launch {
            repeat(PRETIME_TRIES) { i ->
                if (i > 0) delay(PRETIME_POLL_MS)
                val src = api.cached(params) ?: return@repeat
                val title = src.torrentTitle?.takeIf { it.isNotEmpty() } ?: src.fileName
                subtitles.pretime(SubtitleQuery.of(next, src.hash.lowercase(), src.index, language, title)).join()
                return@launch
            }
        }
    }

    private fun onEnded() {
        if (!hasMedia || s.phase != Phase.Ready) return
        val duration = s.duration
        plog.onEnded()
        // The stream ended well before the file did: ffmpeg or TorrServer dropped it
        if (duration > 0 && positionNow() < duration - 30) {
            plog.note("early-end")
            recover()
            return
        }
        finish()
    }

    /** The title is over: saved as watched, the next episode counts down. */
    private fun finish() {
        disarmStall()
        set { copy(playing = false, ended = true) }
        persist(finished = true)
        // A countdown the end credits started goes on
        if (s.next != null && prefs().autoplayNext && countdownTimer == null) startCountdown()
    }

    private fun onError(error: PlaybackException) {
        if (s.phase != Phase.Ready || exo.currentMediaItem?.mediaId != seekSeq.toString()) return
        if (error.isReopen) {
            // A seek Media3 could not do inside its buffer: a new stream at the target
            restartAt(seekTarget ?: positionNow(), "seek")
            return
        }
        if (isAudioFormatError(error) && restrictAudio()) {
            restartAt(positionNow(), "codec")
            return
        }
        Log.w(TAG, "[player] playback error ${error.errorCodeName}: ${error.message}")
        plog.note(if (error.errorCode in IO_ERRORS) "stream-fail" else "error", mapOf("code" to error.errorCodeName, "played" to hasPlayed))
        if (hasPlayed) {
            recover()
            return
        }
        disarmStall()
        if (!sourceFailed(positionNow(), true)) set { copy(fatal = PlayerError.Key(Msg.PlaybackFailed)) }
    }

    /** Audio this device cannot take after all (no decoder and no passthrough): converted AAC / Opus from now on. */
    private fun checkTracks(tracks: Tracks) {
        if (tracks.containsType(C.TRACK_TYPE_AUDIO) && !tracks.isTypeSupported(C.TRACK_TYPE_AUDIO) && restrictAudio()) {
            restartAt(positionNow(), "codec")
        } else if (tracks.containsType(C.TRACK_TYPE_VIDEO) && !tracks.isTypeSupported(C.TRACK_TYPE_VIDEO)) {
            plog.note("error", mapOf("code" to "video_unsupported"))
        }
    }

    private fun restrictAudio(): Boolean {
        if (!core.playbackCaps.restrictToSafeAudio()) return false
        Log.w(TAG, "[player] the device refused the audio: asking for AAC / Opus")
        plog.note("codec", mapOf("audio" to "safe"))
        return true
    }

    private fun isAudioFormatError(error: PlaybackException): Boolean {
        if (error.errorCode == PlaybackException.ERROR_CODE_AUDIO_TRACK_INIT_FAILED) return true
        val format = (error as? ExoPlaybackException)?.rendererFormat ?: return false
        return MimeTypes.isAudio(format.sampleMimeType) && error.errorCode in DECODER_ERRORS
    }

    private fun disarmStall() {
        stallTimer?.cancel()
        stallArmed = false
    }

    private fun armStall() {
        stallTimer?.cancel()
        stallArmed = true
        // Right after a long pause a stalled stream has probably died meanwhile: restart it quickly
        val base = when {
            !hasPlayed -> FIRST_START_MS
            now() - resumedAt < 60_000 -> RESUME_STALL_MS
            else -> STALL_MS
        }
        stallTimer = after(base * (1 + recoveries)) {
            stallArmed = false
            // A paused player is not stalled (a buffer hold is)
            if (exo.playWhenReady || hold != null) recover()
        }
    }

    /** A wait mid-playback that lasts STALL_CONFIRM_MS: show buffering and resume only with a few seconds buffered. */
    private fun confirmStall() {
        stallConfirmTimer?.cancel()
        val seq = seekSeq
        val from = exo.currentPosition
        stallConfirmTimer = after(STALL_CONFIRM_MS) {
            if (seq != seekSeq || destroyed || !hasPlayed || exo.playbackState == Player.STATE_ENDED || (!exo.playWhenReady && hold == null)) return@after
            if (exo.playbackState == Player.STATE_READY && exo.currentPosition > from) return@after
            plog.note("buffering", mapOf("rs" to exo.playbackState, "seeking" to (seekTarget != null)))
            set { copy(buffering = true) }
            if (seekTarget == null && hold == null) startHold(HoldKind.Stall)
        }
    }

    private fun startHold(kind: HoldKind) {
        clearHold()
        hold = Hold(kind, since = if (kind == HoldKind.Stall) now() else null)
        plog.note("hold", mapOf("kind" to kind.name.lowercase()))
        set { copy(holding = true) }
        // Keeps downloading while paused
        exo.playWhenReady = false
        holdTimer = every(250) { checkHold() }
    }

    private fun clearHold() {
        holdTimer?.cancel()
        holdTimer = null
        hold = null
        if (s.holding) set { copy(holding = false) }
    }

    private fun checkHold() {
        val h = hold ?: return
        val now = now()
        if (h.since == null && exo.playbackState == Player.STATE_READY) h.since = now
        val ahead = ahead()
        // A growing buffer is progress: the stall watchdog only restarts a stream that stopped
        if (ahead > h.ahead + 0.5) {
            h.ahead = ahead
            armStall()
        }
        val dur = s.duration
        val end = positionNow() + ahead
        val done = holdDone(
            h.kind, ahead = ahead, waitedMs = h.since?.let { now - it } ?: 0, fetching = exo.isLoading,
            atEnd = dur > 0 && end >= dur - 1,
        )
        if (!done || background) return
        plog.note("hold-end", mapOf("kind" to h.kind.name.lowercase(), "ahead" to ahead, "waited" to (h.since?.let { now - it } ?: 0L)))
        clearHold()
        videoPlay()
    }

    private fun recover() {
        disarmStall()
        if (recoveries >= MAX_RECOVERIES) {
            // Out of restarts: another source first, an error only when that is out of tries too
            if (!sourceFailed(positionNow(), true)) set { copy(reconnecting = false, fatal = PlayerError.Key(Msg.ConnectionLost)) }
            return
        }
        recoveries++
        restartAt(positionNow(), "stall")
    }

    // ---- adaptive quality (sidegrade first, then a lower cap) ----

    private fun beginStall(seek: Boolean) {
        if (!everPlayed || stall != null) return
        stall = Stall(now(), seek)
        longStallTimer?.cancel()
        longStallTimer = after(LONG_STALL_MS) {
            if (stall != null && (exo.playWhenReady || hold != null)) struggling()
        }
    }

    private fun endStall() {
        longStallTimer?.cancel()
        val st = stall
        stall = null
        if (st == null || st.seek) return
        val now = now()
        if (now - st.start < MIN_STALL_MS) return
        stallTimes.removeAll { now - it >= STALL_WINDOW_MS }
        stallTimes += now
        if (stallTimes.size >= 2) struggling()
    }

    /** Real stalls (two within a minute, or one over LONG_STALL_MS): a faster source, else a lower resolution. */
    private fun struggling() {
        stallTimes.clear()
        if (pendingSide != null) {
            usePending("stall")
            return
        }
        val quality = videoHeight
        scope.launch { if (!trySidegrade(quality, restart = true)) stepDown(quality) }
    }

    /** Another source at the current position (restarts the stream). */
    private fun switchTo(src: StreamSource, msg: Msg, reason: String) {
        lastSwitch = now()
        // The shown position when the player has none (between streams)
        val pos = positionNow().takeIf { it > 0 } ?: s.position
        openSource(src, pos, Origin.Fresh, reason)
        toast(msg)
    }

    /** A sidegrade offer measured clearly faster than the playing source. */
    private fun betterOffer(up: ResolvedSource): Boolean {
        val r = healthRatio(up.health)
        if (r == null || r < BETTER_RATIO) return false
        val cur = source?.ratio
        return cur == null || r > cur
    }

    /** Asks for a faster source: [restart] switches at once (stalled or not started yet), else it is held for a stall. */
    private suspend fun trySidegrade(quality: Int, restart: Boolean): Boolean {
        val src = source
        if (src == null || busy || now() - lastSidegrade < SIDEGRADE_EVERY_MS) return false
        val session = session
        busy = true
        try {
            val up = api.sidegrade(params(), src.hash, if (quality > 0) quality else releaseHeight(src.quality), "critical", slow)
            if (up == null || up.hash.equals(src.hash, ignoreCase = true) || stale(session)) return false
            lastSidegrade = now()
            val h = up.hash.lowercase()
            if (h in slow || !betterOffer(up)) {
                Log.i(TAG, "[sidegrade] offer ${h.take(8)} not better (ratio ${healthRatio(up.health)})")
                api.releaseTorrents(listOf(h))
                return false
            }
            val offer = up.toSource()
            if (restart) {
                slow += current
                switchTo(offer, Msg.SwitchedFaster, "sidegrade")
                return true
            }
            if (pendingSide == null) pendingSide = offer else api.releaseTorrents(listOf(h))
            return false
        } finally {
            busy = false
        }
    }

    // ---- buffer health: proactive sidegrade ----

    private fun sampleHealth() {
        if (destroyed) return
        val st = s
        if (st.phase != Phase.Ready || !hasMedia || !exo.isPlaying || seekTarget != null || st.buffering || st.ended ||
            st.fatal != null || !hasPlayed || !exact
        ) return
        val pos = positionNow()
        val end = pos + ahead()
        onHealth(health.push(BufferSample(at = now(), pos = pos, end = end, fetching = exo.isLoading, atEnd = st.duration > 0 && end >= st.duration - 1)))
    }

    private fun onHealth(h: BufferVerdict) {
        val pending = pendingSide
        if (pending != null) {
            // Recovered: the held source is not needed (a switch waits for a real stall)
            if (h.healthyMs >= RECOVERED_MS) {
                pendingSide = null
                api.releaseTorrents(listOf(pending.hash))
            }
            return
        }
        if (h.state == BufferState.Healthy) {
            sideNext = 0
            sideDelay = PROACTIVE_POLL_MS
            return
        }
        val now = now()
        if (now < sideNext) return
        sideNext = now + sideDelay
        sideDelay = min(PROACTIVE_POLL_MAX_MS, (sideDelay * 1.5).toLong())
        Log.i(TAG, "[health] ${h.state} ahead=${"%.1f".format(h.ahead)} growth=${h.growth?.let { "%.2f".format(it) } ?: "-"}")
        plog.note("health", mapOf("state" to h.state.name.lowercase(), "ahead" to h.ahead, "growth" to h.growth))
        askSidegrade(if (h.state == BufferState.Critical) "critical" else "draining")
    }

    /** The buffer drains: ask for a faster source; only a clearly faster one is held. */
    private fun askSidegrade(state: String) {
        val src = source ?: return
        val now = now()
        if (busy || pendingSide != null || now - lastSidegrade < SIDEGRADE_EVERY_MS || now - lastSwitch < SWITCH_EVERY_MS) return
        val session = session
        busy = true
        scope.launch {
            try {
                val up = api.sidegrade(params(), src.hash, if (videoHeight > 0) videoHeight else releaseHeight(src.quality), state, slow) ?: return@launch
                val h = up.hash.lowercase()
                if (h == src.hash.lowercase()) return@launch
                lastSidegrade = now()
                if (stale(session) || h in slow || !betterOffer(up)) {
                    Log.i(TAG, "[sidegrade] offer ${h.take(8)} not better (ratio ${healthRatio(up.health)})")
                    api.releaseTorrents(listOf(h))
                    return@launch
                }
                pendingSide = up.toSource()
            } finally {
                busy = false
            }
        }
    }

    /** Switches to the held faster source (after a real stall). */
    private fun usePending(reason: String) {
        val next = pendingSide ?: return
        pendingSide = null
        slow += current
        switchTo(next, Msg.SwitchedFaster, reason)
    }

    private fun stepDown(quality: Int) {
        val src = source ?: return
        val lower = lowerCap(if (quality > 0) quality else releaseHeight(src.quality)) ?: return
        if (busy || downgrades >= MAX_DOWNGRADES) return
        if (lower in triedCaps || (cap != null && lower.height > cap!!.height)) return
        triedCaps += lower
        val session = session
        busy = true
        scope.launch {
            try {
                val r = quiet { api.resolve(params(lower), refresh = false, avoid = avoid, t = 0.0) } ?: return@launch
                if (stale(session)) return@launch
                cap = lower
                if (r.health != null && !r.health.keepsUp) return@launch
                if (r.hash.equals(src.hash, ignoreCase = true) && r.index == src.index) return@launch
                downgrades++
                switchTo(r.toSource(), Msg.SwitchedHd, "stepdown")
            } finally {
                busy = false
            }
        }
    }

    /** A source the resolve measured as starving: look for a faster one early (also before the first frame at 0). */
    private fun scheduleSidegrade(session: Int, startAt: Double) {
        val started = now()
        lateinit var check: suspend () -> Unit
        check = {
            if (!stale(session) && now() - started <= SIDEGRADE_FOR_MS) {
                val switched = (positionNow() > 0 || startAt <= 0) && trySidegrade(videoHeight, restart = !everPlayed)
                if (!switched && !stale(session)) poll(SIDEGRADE_POLL_MS, check)
            }
        }
        poll(SIDEGRADE_FIRST_MS, check)
    }

    /**
     * Start low, switch up: below 4K on an HEVC TV, ask for the warmed higher source. Early in playback it restarts
     * on it at once; later it waits for the next seek (a restart anyway).
     */
    private fun scheduleUpgrade(session: Int) {
        val src = source ?: return
        if (!core.playbackCaps.current().hevc || upgraded || releaseHeight(src.quality) >= 2160) return
        val steppedDown = { downgrades > 0 || cap != null || triedCaps.isNotEmpty() }
        val started = now()
        lateinit var check: suspend () -> Unit
        check = check@{
            if (stale(session) || upgraded || now() - started > UPGRADE_FOR_MS) return@check
            val quality = if (videoHeight > 0) videoHeight else releaseHeight(src.quality)
            if (quality >= 2160) return@check
            if (quality > 0 && positionNow() > 0 && everPlayed) {
                val up = api.upgrade(params(null), src.hash, quality)
                if (stale(session)) return@check
                // After a step down (a slow swarm) only a TorBox source comes back up: it plays at line speed
                if (up != null && releaseHeight(up.quality) > quality && !upgraded && (!steppedDown() || up.debrid)) {
                    if (steppedDown()) {
                        cap = null
                        triedCaps.clear()
                    }
                    upgraded = true
                    val msg = if (up.quality == "2160p") Msg.SwitchedTo4k else Msg.SwitchedHigher
                    val secs = (now() - started) / 1000
                    if (now() - started < UPGRADE_RESTART_MS) {
                        Log.i(TAG, "[upgrade] ${up.hash.take(8)}${if (up.debrid) " (TorBox)" else ""} offered $secs s after the start: restarting on it")
                        lastSwitch = now()
                        openSource(up.toSource(), positionNow(), Origin.Fresh, "upgrade")
                        toast(msg)
                    } else {
                        Log.i(TAG, "[upgrade] ${up.hash.take(8)} offered $secs s after the start: waits for the next seek")
                        deferredUpgrade = up.toSource() to msg
                    }
                    return@check
                }
            }
            poll(UPGRADE_POLL_MS, check)
        }
        poll(UPGRADE_FIRST_MS, check)
    }

    // ---- controls ----

    fun togglePlay() {
        if (s.phase != Phase.Ready) return
        parkedAt?.let {
            restartAt(it, "resume")
            return
        }
        if (!hasMedia) return
        val h = hold
        if (h != null) {
            // The viewer decides: Play during the start buffer starts now, Pause during a stall stays paused
            if (h.kind == HoldKind.Start) play() else pause()
            return
        }
        if (exo.playWhenReady) videoPause() else videoPlay()
    }

    fun play() {
        parkedAt?.let {
            if (s.phase == Phase.Ready) restartAt(it, "resume")
            return
        }
        clearHold()
        if (!exo.playWhenReady && hasMedia) videoPlay()
    }

    fun pause() {
        if (hold != null) {
            clearHold()
            set { copy(playing = false) }
        }
        videoPause()
    }

    /** [why] names the seek in the playback log (user, skip-intro ...). */
    fun seekTo(t: Double, why: String = "user") {
        // While another source is looked up hash and duration still name the failed one
        if (!canSeek(s.phase, s.fatal != null, s.duration, hash)) return
        val target = max(0.0, min(s.duration - 1, t))
        val rel = target - offset
        // Already buffered: seek in place, no new stream
        if (!s.ended && rel >= 0 && inBuffer(rel)) {
            plog.mark(why)
            plog.onSeeking()
            seekTarget = target
            exo.seekTo((rel * 1000).toLong())
            set { copy(position = target) }
            return
        }
        deferredUpgrade?.let { (src, msg) ->
            deferredUpgrade = null
            lastSwitch = now()
            openSource(src, target, Origin.Fresh, "upgrade")
            toast(msg)
            return
        }
        restartAt(target, if (why == "user") "seek" else "seek $why")
    }

    /** Stream time [rel] is inside what Media3 holds (ahead, or in the back buffer). */
    private fun inBuffer(rel: Double): Boolean {
        if (!hasMedia || !hasPlayed || exo.playbackState == Player.STATE_IDLE || exo.playbackState == Player.STATE_ENDED) return false
        val cur = exo.currentPosition / 1000.0
        val end = exo.bufferedPosition / 1000.0
        return rel <= end - 0.5 && rel >= cur - (BACK_BUFFER_S - 2)
    }

    fun setAudio(n: Int) {
        if (n == s.audio) return
        val track = s.audioTracks.firstOrNull { it.index == n }
        set { copy(audio = n, audioBadge = audioBadge(track, source?.atmos == true)) }
        restartAt(positionNow(), "audio")
        val lang = langOf(track?.language)
        val keepOriginal = prefs().audioLang == "original" && lang == langOf(meta.originalLanguage)
        if (lang.isNotEmpty() && !keepOriginal) updatePrefs { it.copy(audioLang = lang) }
        subtitles.onAudioLanguage(track?.language)
    }

    fun setSubtitle(id: String?) = subtitles.select(id)

    /** Subtitle timing (+ shows them later), kept for the playing file; returns the value it took. */
    fun setSubDelay(seconds: Double): Double {
        val d = clampSubDelay(seconds)
        subtitles.setDelay(d)
        return d
    }

    /** The viewer chose the release that carries the profile language's subtitle: restart on it at the same position. */
    fun switchSubAlt() {
        val src = subAltSource ?: return
        subAltSource = null
        // Its list shows the language as the viewer's choice
        subtitles.preferLanguage(if (language == AppLanguage.Tr) "tr" else "en")
        set { copy(subAlt = false) }
        switchTo(src, Msg.SwitchedSubtitles, "subalt")
    }

    fun retry() {
        // The last shown position (positionNow keeps an old offset without a stream), else where the item was to start
        val at = if (s.position > 0) s.position else resumePos
        if (s.phase == Phase.Error) {
            startItem(refresh = true, at = at)
            return
        }
        set { copy(fatal = null) }
        recoveries = 0
        if (!sourceFailed(at, false)) restartAt(at, "retry")
    }

    /** Subtitle appearance of the profile (the panel's appearance view). */
    fun setSubtitleStyle(style: SubtitleStyle) = updatePrefs { it.copy(subtitleStyle = style) }

    /** Intro and end credits of the playing episode file. */
    suspend fun skipSegments(m: PlayRequest, file: PlayingFile, duration: Double): SkipSegments? =
        api.skipSegments(m, file.hash, file.index, duration)

    /** The frames around the position, made by the server in the background while the stream runs. */
    fun prefetchThumbs() {
        if (s.phase != Phase.Ready || hash.isEmpty() || !hasMedia) return
        val pos = positionNow()
        val ahead = ahead()
        val g = gridT(pos)
        val times = (-THUMB_BEHIND..THUMB_AHEAD).map { g + it * THUMB_GRID }.filter { it >= 0 && it <= s.duration && it <= pos + ahead }
        val key = "$hash:$index:$g:${(ahead / THUMB_GRID).toInt()}"
        if (key == lastThumbs) return
        lastThumbs = key
        thumbs.prefetch(hash, index, times)
    }

    /**
     * The app went to the background (Home button): pause, save and let go of the stream, the decoders and any
     * source lookup, so nothing plays or keeps the server busy while hidden. [onForeground] picks a lookup up again;
     * a stopped stream starts again on Play / OK.
     */
    fun onBackground() {
        pause()
        persist()
        plog.flush()
        if (background || destroyed) return
        background = true
        subtitles.stop()
        when {
            resolveJob != null -> {
                cancelResolve()
                resumeLoad = lastLoad
            }
            s.phase != Phase.Ready || s.fatal != null || s.ended || hash.isEmpty() -> Unit
            hasMedia -> {
                val pos = positionNow()
                session++
                stopStream()
                parkedAt = pos
                set { copy(position = pos, buffering = false) }
            }
            // Still probing the source
            else -> {
                session++
                stopStream()
                resumeLoad = lastLoad
            }
        }
    }

    fun onForeground() {
        if (!background || destroyed) return
        background = false
        resumeLoad?.let {
            resumeLoad = null
            it()
        }
    }

    // ---- subtitles ----

    private fun audioLanguage(): String? = s.audioTracks.firstOrNull { it.index == s.audio }?.language

    /** Every fresh open of a source lists the subtitles of its file again (the language choice stays). */
    private fun loadSubtitles(again: Boolean = false) {
        val src = source ?: return
        subAltSource = null
        set { copy(subAlt = false) }
        val title = src.torrentTitle?.takeIf { it.isNotEmpty() } ?: src.fileName
        val query = SubtitleQuery.of(meta, hash, index, language, title)
        // again: the same file after the background, its subtitle choice stays
        if (again) subtitles.restart(query, audioLanguage()) else subtitles.start(query, audioLanguage())
    }

    private fun onSubtitleEvent(e: SubtitleEvent) {
        if (destroyed) return
        when (e) {
            is SubtitleEvent.Missing -> findSubAlt(session, e.lang)
            is SubtitleEvent.Found -> {
                subAltSource = null
                set { copy(subAlt = false) }
                toast(Msg.SubtitleFound)
            }
        }
    }

    /** Asks the server for a release of the title whose subtitles include [lang] (it may scan in the background). */
    private fun findSubAlt(session: Int, lang: String) {
        val src = source ?: return
        if (subAltSource != null) return
        scope.launch {
            repeat(SUBALT_RETRY_MAX) {
                val (alt, retryAfter) = api.subAlt(params(), src.hash, if (videoHeight > 0) videoHeight else releaseHeight(src.quality), lang)
                if (stale(session)) return@launch
                val h = alt?.hash?.lowercase()
                if (alt != null && h != current && h !in slow) {
                    subAltSource = alt.toSource()
                    set { copy(subAlt = true) }
                    return@launch
                }
                if (retryAfter == null) return@launch
                delay(retryAfter * 1000L)
                if (stale(session)) return@launch
            }
        }
    }

    // ---- episodes ----

    private fun loadSeasons() {
        if (meta.type != MediaType.Tv) return
        if (seasons.isNotEmpty()) {
            seasonsReady()
            return
        }
        scope.launch {
            seasons = core.catalog.tvSeasons(meta.tmdbId)
            if (!destroyed) seasonsReady()
        }
    }

    private fun seasonsReady() {
        updateNext()
        fillEpisodeInfo()
    }

    private fun updateNext() {
        val m = meta
        val next = if (m.type == MediaType.Tv && m.season != null && m.episode != null) nextEpisodeOf(seasons, m.season!!, m.episode!!) else null
        set { copy(next = next) }
    }

    /** Episode title (and runtime for later resolves) when only the numbers are known. */
    private fun fillEpisodeInfo() {
        val m = meta
        if (m.type != MediaType.Tv || m.season == null || m.episode == null || (!m.episodeTitle.isNullOrEmpty() && m.runtimeMinutes != null)) return
        val season = seasons.firstOrNull { it.seasonNumber == m.season } ?: return
        scope.launch {
            val ep = core.catalog.seasonEpisodes(m.tmdbId, season).firstOrNull { it.episodeNumber == m.episode } ?: return@launch
            if (meta !== m || destroyed) return@launch
            meta = m.copy(
                episodeTitle = m.episodeTitle?.takeIf { it.isNotEmpty() } ?: ep.name.takeIf { ep.hasRealName },
                runtimeMinutes = m.runtimeMinutes ?: ep.runtime.takeIf { it > 0 },
            )
            set { copy(meta = meta) }
        }
    }

    /** [fromCredits]: leaving from the end credits, this episode counts as watched. */
    fun playNext(fromCredits: Boolean = false) {
        val next = s.next ?: return
        if (fromCredits && !s.ended) persist(finished = true)
        plog.note("next", mapOf("credits" to fromCredits))
        plog.flush()
        stopCountdown()
        deferredUpgrade?.let {
            api.releaseTorrents(listOf(it.first.hash))
            deferredUpgrade = null
        }
        // The finished episode is done; the next one was warmed for this moment
        core.warmup.release(params())
        nextWarm = null
        nextPretime?.cancel()
        nextPretime = null
        meta = meta.copy(season = next.first, episode = next.second, episodeTitle = null, runtimeMinutes = null, startAt = 0.0, source = null)
        startAt = null
        given = null
        autoRefreshes = 0
        avoid.clear()
        lengthRetries = 0
        itemPlayed = false
        // Each episode may switch up once
        upgraded = false
        resumePos = 0.0
        set { copy(meta = meta, ended = false, position = 0.0) }
        updateNext()
        fillEpisodeInfo()
        startItem()
    }

    /** The end credits started: count down to the next episode while they play. */
    fun creditsStarted() {
        if (s.next != null && !s.ended && countdownTimer == null && prefs().autoplayNext) startCountdown()
    }

    private fun startCountdown() {
        stopCountdown()
        set { copy(countdown = 5) }
        countdownTimer = every(1000) {
            // Paused during the credits: the countdown waits
            if (background || (!s.ended && !exo.playWhenReady)) return@every
            val n = (s.countdown ?: 1) - 1
            if (n <= 0) playNext(fromCredits = true) else set { copy(countdown = n) }
        }
    }

    fun stopCountdown() {
        countdownTimer?.cancel()
        countdownTimer = null
        if (s.countdown != null) set { copy(countdown = null) }
    }

    // ---- progress ----

    private fun persist(finished: Boolean = false) {
        val m = meta
        val duration = s.duration
        // duration is reset while a new source opens, so a late pause never saves a mix of two sources
        if (destroyed || pid == null || hash.isEmpty() || s.phase != Phase.Ready) return
        // Nothing of this item played yet: the saved progress stays (a failed start never overwrites it)
        val pos = progressToSave(finished, itemPlayed, duration, exact, positionNow(), offset) ?: return
        val tv = m.type == MediaType.Tv
        val progress = StreamProgress(
            profileId = pid, mediaType = m.type, tmdbId = m.tmdbId, title = m.title,
            posterPath = m.posterPath.orEmpty(), backdropPath = m.backdropPath.orEmpty(),
            season = if (tv) m.season ?: 0 else 0, episode = if (tv) m.episode ?: 0 else 0,
            episodeTitle = m.episodeTitle.orEmpty(), magnet = source?.magnet.orEmpty(), hash = hash, fileIndex = index,
            position = (pos * 10).roundToInt() / 10.0, duration = (duration * 10).roundToInt() / 10.0,
        )
        core.scope.launch { core.progress.save(progress) }
    }

    /** Saves progress, stops the stream and lets the server drop every torrent of this session. */
    fun destroy() {
        if (destroyed) return
        persist()
        subtitles.stop()
        plog.note("exit", mapOf("subdelay" to subtitles.state.value.takeIf { it.selectedId != null }?.delaySeconds))
        plog.destroy()
        destroyed = true
        resolveJob?.cancel()
        stopStream()
        core.warmup.release(params())
        nextWarm?.let { core.warmup.release(it) }
        deferredUpgrade?.let { played += it.first.hash.lowercase() }
        api.releaseTorrents(played)
        exo.removeListener(listener)
        exo.release()
        scope.cancel()
    }

    private enum class Origin { Given, Saved, Cached, Fresh }

    private class Previous(val src: StreamSource, val origin: Origin, val duration: Double)

    private class Stall(val start: Long, val seek: Boolean)

    private class Hold(val kind: HoldKind, var since: Long?, var ahead: Double = 0.0)

    private companion object {
        const val TAG = "NextflixPlayer"
        const val STALL_MS = 12_000L
        const val FIRST_START_MS = 30_000L
        // A torrent without a first frame this long after it opened hands over to a faster source
        const val SLOW_START_MS = 12_000L
        const val MAX_RECOVERIES = 3
        // A failed source is resolved again (without it) this many times per item before an error shows
        const val MAX_AUTO_REFRESH = 3
        // While the viewer pauses the server keeps the file ready; after a long pause a stall restarts quickly
        const val KEEPALIVE_MS = 15_000L
        const val LONG_PAUSE_MS = 10_000L
        const val RESUME_STALL_MS = 2500L
        const val LONG_STALL_MS = 8000L
        const val STALL_WINDOW_MS = 60_000L
        // A stall counts once playback waited this long; shorter ones are hiccups
        const val MIN_STALL_MS = 3000L
        // A wait mid-playback shows buffering and holds for a larger buffer only once it lasted this long
        const val STALL_CONFIRM_MS = 800L
        const val MAX_DOWNGRADES = 2
        // The first upgrade poll comes soon after the start (a TorBox 4K is often ready at once), then every 10 s
        const val UPGRADE_FIRST_MS = 5000L
        const val UPGRADE_POLL_MS = 10_000L
        const val UPGRADE_FOR_MS = 5 * 60 * 1000L
        // An upgrade this early in playback restarts on it at once; later it waits for the next seek
        const val UPGRADE_RESTART_MS = 90 * 1000L
        const val MAX_LENGTH_RETRIES = 2
        const val SIDEGRADE_FIRST_MS = 8000L
        const val SIDEGRADE_POLL_MS = 15_000L
        const val SIDEGRADE_FOR_MS = 3 * 60 * 1000L
        const val SIDEGRADE_EVERY_MS = 2 * 60 * 1000L
        // Proactive sidegrade: while the buffer drains the server is asked at once, then every 5 s backing off to
        // 10 s; an offer is held for a real stall and dropped once the stream was healthy again for RECOVERED_MS
        const val PROACTIVE_POLL_MS = 5000L
        const val PROACTIVE_POLL_MAX_MS = 10_000L
        const val SWITCH_EVERY_MS = 60_000L
        const val RECOVERED_MS = 15_000L
        const val BETTER_RATIO = 1.3
        const val TOAST_MS = 4000L
        // A /seekpoint that failed (its pieces were not in yet) is asked again after these delays
        val SEEKPOINT_RETRY_MS = listOf(3000L, 10_000L, 30_000L)
        const val SUBALT_RETRY_MAX = 4
        // The next episode's prefetched source is looked up this often, this many times (webOS player/pretime.ts)
        const val PRETIME_POLL_MS = 20_000L
        const val PRETIME_TRIES = 9
        // Trick play frames made ahead of a seek: grid steps behind and ahead of the position
        const val THUMB_BEHIND = 3
        const val THUMB_AHEAD = 3

        val IO_ERRORS = setOf(
            PlaybackException.ERROR_CODE_IO_UNSPECIFIED,
            PlaybackException.ERROR_CODE_IO_NETWORK_CONNECTION_FAILED,
            PlaybackException.ERROR_CODE_IO_NETWORK_CONNECTION_TIMEOUT,
            PlaybackException.ERROR_CODE_IO_BAD_HTTP_STATUS,
            PlaybackException.ERROR_CODE_IO_INVALID_HTTP_CONTENT_TYPE,
            PlaybackException.ERROR_CODE_PARSING_CONTAINER_MALFORMED,
        )
        val DECODER_ERRORS = setOf(
            PlaybackException.ERROR_CODE_DECODER_INIT_FAILED,
            PlaybackException.ERROR_CODE_DECODER_QUERY_FAILED,
            PlaybackException.ERROR_CODE_DECODING_FAILED,
            PlaybackException.ERROR_CODE_DECODING_FORMAT_UNSUPPORTED,
            PlaybackException.ERROR_CODE_DECODING_FORMAT_EXCEEDS_CAPABILITIES,
        )
    }
}
