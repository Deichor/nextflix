package com.nextflix.tv.feature.player

import android.os.SystemClock
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.key
import androidx.compose.ui.layout.ContentScale
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.LifecycleEventEffect
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.media3.common.util.UnstableApi
import androidx.media3.ui.compose.ContentFrame
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.core
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.prefs.SubtitleStyle
import com.nextflix.tv.core.prefs.subtitleStyleStrings
import com.nextflix.tv.core.ui.focus.Keys
import com.nextflix.tv.core.ui.focus.onPreviewKeyDown
import com.nextflix.tv.core.ui.focus.repeatCount
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.feature.subtitles.Cue
import com.nextflix.tv.feature.subtitles.SubtitleOverlay
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Full-screen player (webOS player/: engine.ts, Player.tsx, AudioSubtitlePanel.tsx, TrickPlay.tsx, PauseScreen.tsx).
 * Public entry point of :feature:player; the signature is fixed. The app keeps the screen on while it shows.
 *
 * Remote: OK play / pause, Left / Right seek 10 s (held: 30 s, then 60 s steps; a second press or a held key
 * scrubs with trick-play frames until OK), Up shows the buttons, Back closes the panel or scrub, then the player.
 * Media keys work everywhere.
 */
@Composable
fun PlayerScreen(request: PlayRequest, nav: Navigator) {
    val core = core()
    val vm = viewModel { PlayerViewModel(core, request) }
    PlayerContent(vm.engine, onClose = { nav.back() })
}

internal class PlayerViewModel(core: Core, request: PlayRequest) : ViewModel() {
    val engine = PlayerEngine(core, request)

    override fun onCleared() = engine.destroy()
}

internal enum class Zone { Timeline, Buttons, Skip }

internal enum class Overlay { Resolving, Error, Unreleased, Fatal, Countdown }

/** UI state of the player screen and the remote's behaviour (webOS Player.tsx). */
@Stable
internal class PlayerUi(private val engine: PlayerEngine, private val scope: CoroutineScope) {
    var visible by mutableStateOf(true)
    var zone by mutableStateOf(Zone.Timeline)
    var panel by mutableStateOf(false)
    /** the subtitle appearance view is open (live preview) */
    var styling by mutableStateOf(false)
    /** seek target shown while scrubbing (s) */
    var preview by mutableStateOf<Double?>(null)
    var scrubMode by mutableStateOf(false)
    var pauseScreen by mutableStateOf(false)
    var revealTick by mutableIntStateOf(0)
    var lastInput = 0L
    /** a Back that only woke the pause screen */
    var swallowBack = false
    var creditsDismissed = false
    private var repeats = 0
    private var commitJob: Job? = null
    private var idleJob: Job? = null

    val timelineFocus = FocusRequester()
    val playPauseFocus = FocusRequester()
    val audioFocus = FocusRequester()
    val skipFocus = FocusRequester()
    val overlayFocus = FocusRequester()
    val backFocus = FocusRequester()
    /** the screen itself: keys still arrive while nothing else can take the focus */
    val rootFocus = FocusRequester()

    /** The timeline, else the screen. */
    fun focusTimeline() {
        if (!timelineFocus.tryFocus()) rootFocus.tryFocus()
    }

    fun reveal() {
        lastInput = SystemClock.elapsedRealtime()
        pauseScreen = false
        visible = true
        revealTick++
    }

    fun endScrub() {
        commitJob?.cancel()
        idleJob?.cancel()
        scrubMode = false
        preview = null
        engine.thumbs.clearWanted()
    }

    fun commit() {
        val p = preview
        endScrub()
        if (p != null) engine.seekTo(p)
    }

    /** Back, Up / Down or no key for SCRUB_IDLE_MS: playback goes on where it is. */
    fun cancelScrub() {
        endScrub()
        reveal()
    }

    fun scrub(sign: Int, repeat: Boolean) {
        val duration = engine.state.value.duration
        if (duration <= 0) return
        repeats = if (repeat) repeats + 1 else 0
        val from = preview ?: engine.positionNow()
        val next = (from + sign * scrubStep(repeats)).coerceIn(0.0, duration - 1)
        // A single press seeks after COMMIT_MS; a second press within it, or a held key, starts scrub mode
        if (!scrubMode && (repeat || preview != null)) scrubMode = true
        preview = next
        commitJob?.cancel()
        idleJob?.cancel()
        if (scrubMode) {
            idleJob = scope.launch {
                delay(SCRUB_IDLE_MS)
                cancelScrub()
            }
        } else {
            commitJob = scope.launch {
                delay(COMMIT_MS)
                commit()
            }
        }
    }

    fun openPanel() {
        panel = true
    }

    fun closePanel() {
        panel = false
        styling = false
        reveal()
        scope.launch {
            // The buttons can take the focus again once the panel is gone
            repeat(2) { withFrameNanos { } }
            audioFocus.tryFocus()
        }
    }

    companion object {
        const val HIDE_MS = 4500L
        /** paused this long without a key: the pause screen replaces the controls */
        const val PAUSE_SCREEN_MS = 6000L
        const val COMMIT_MS = 800L
        const val SCRUB_IDLE_MS = 15_000L
    }
}

@androidx.annotation.OptIn(UnstableApi::class)
@Composable
private fun PlayerContent(engine: PlayerEngine, onClose: () -> Unit) {
    val core = core()
    val s by engine.state.collectAsStateWithLifecycle()
    val subs by engine.subtitles.state.collectAsStateWithLifecycle()
    val prefs by core.playerPrefs.all.collectAsStateWithLifecycle()
    val style = prefs[core.session.profileId ?: 0]?.subtitleStyle ?: SubtitleStyle()
    val scope = rememberCoroutineScope()
    val ui = remember(engine) { PlayerUi(engine, scope) }
    var rootHasFocus by remember { mutableStateOf(false) }

    // The TV's Home button sends the app to the background: pause, save and stop the stream
    LifecycleEventEffect(Lifecycle.Event.ON_STOP) { engine.onBackground() }
    LifecycleEventEffect(Lifecycle.Event.ON_START) { engine.onForeground() }

    LaunchedEffect(ui.revealTick) {
        delay(PlayerUi.HIDE_MS)
        if (ui.panel || ui.scrubMode || !engine.state.value.playing) return@LaunchedEffect
        ui.visible = false
        // Hidden controls: OK plays / pauses again
        if (ui.zone == Zone.Buttons) ui.focusTimeline()
    }
    // Playback (re)started: the controls fade out after a while
    LaunchedEffect(s.playing) { if (s.playing) ui.reveal() }

    val playable = s.playable
    val overlay = when {
        s.phase == Phase.Resolving -> Overlay.Resolving
        s.phase == Phase.Error -> Overlay.Error
        s.phase == Phase.Unreleased -> Overlay.Unreleased
        s.fatal != null -> Overlay.Fatal
        s.countdown != null && s.ended -> Overlay.Countdown
        else -> null
    }

    // Focus follows what is on screen: an overlay's buttons, else the timeline
    LaunchedEffect(overlay, ui.panel, playable) {
        if (ui.panel) return@LaunchedEffect
        withFrameNanos { }
        if (overlay == Overlay.Resolving) {
            // Nothing to press while a source is looked up: Back closes, OK does nothing
            ui.rootFocus.tryFocus()
        } else if (overlay != null) {
            if (!ui.overlayFocus.tryFocus() && !ui.backFocus.tryFocus()) ui.rootFocus.tryFocus()
        } else if (ui.zone == Zone.Timeline || !rootHasFocus) {
            ui.focusTimeline()
        }
    }
    // The source went away (another one is looked up, or a fatal error): nothing left to seek in
    LaunchedEffect(playable) { if (!playable) ui.endScrub() }

    // Skip intro / end credits (series only, once per episode and file)
    val m = s.meta
    val file = s.file
    val segKey = if (m.type == MediaType.Tv && m.season != null && m.episode != null && playable && file != null) {
        "${m.tmdbId}:${m.season}:${m.episode}:${file.hash}:${file.index}"
    } else {
        ""
    }
    var segs by remember { mutableStateOf<Pair<String, SkipSegments>?>(null) }
    LaunchedEffect(segKey) {
        if (segKey.isEmpty() || file == null) return@LaunchedEffect
        engine.skipSegments(m, file, s.duration)?.let { segs = segKey to it }
    }
    val skipSegs = segs?.takeIf { it.first == segKey }?.second
    val skip = if (playable && ui.preview == null) skipPhase(skipSegs, s.position, s.duration) else null
    // During the credits: a countdown card, or a Next button with autoplay off or after "Watch credits"
    val creditsCard = s.next != null && !s.ended && (s.countdown != null || skip == SkipPhase.Credits)
    val skipShown = when {
        overlay != null -> null
        skip == SkipPhase.Intro -> SkipPhase.Intro
        creditsCard -> SkipPhase.Credits
        else -> null
    }
    LaunchedEffect(skip, s.playing, s.ended) {
        if (skip != SkipPhase.Credits) {
            ui.creditsDismissed = false
            // Seeked back out of the credits (or away from the end): no next episode countdown
            if (!s.ended) engine.stopCountdown()
            return@LaunchedEffect
        }
        if (!ui.creditsDismissed && s.playing) engine.creditsStarted()
    }
    // The Skip / Next button takes the focus when it appears and gives it back when it goes
    LaunchedEffect(skipShown, ui.panel, overlay) {
        if (ui.panel) return@LaunchedEffect
        withFrameNanos { }
        if (skipShown != null) ui.skipFocus.tryFocus()
        else if (ui.zone == Zone.Skip && overlay == null) ui.focusTimeline()
    }

    // Paused by the viewer with nothing else on screen: the pause screen after PAUSE_SCREEN_MS without a key
    val idlePaused = playable && overlay == null && !s.playing && !s.buffering && !s.ended && s.countdown == null &&
        !ui.panel && ui.preview == null && skipShown == null
    LaunchedEffect(idlePaused) {
        if (!idlePaused) {
            ui.pauseScreen = false
            return@LaunchedEffect
        }
        ui.lastInput = SystemClock.elapsedRealtime()
        while (true) {
            delay(500)
            if (SystemClock.elapsedRealtime() - ui.lastInput >= PlayerUi.PAUSE_SCREEN_MS) ui.pauseScreen = true
        }
    }

    TrickPlayPrefetch(engine, playing = s.phase == Phase.Ready && s.playing, duration = s.duration)

    BackHandler {
        when {
            ui.swallowBack -> ui.swallowBack = false
            ui.scrubMode -> ui.cancelScrub()
            ui.panel -> ui.closePanel()
            else -> onClose()
        }
    }

    val controls = playable && !ui.pauseScreen && (ui.visible || !s.playing || ui.panel || ui.preview != null)

    Box(
        Modifier
            .fillMaxSize()
            .background(Color.Black)
            .onFocusChanged { rootHasFocus = it.hasFocus }
            .onPreviewKeyDown { e -> rootKey(e, ui, engine, onClose) }
            .focusRequester(ui.rootFocus)
            .focusable(),
    ) {
        ContentFrame(
            player = engine.exo,
            modifier = Modifier.fillMaxSize(),
            contentScale = ContentScale.Fit,
            keepContentOnReset = true,
        )

        if (playable && ((subs.selectedId != null && subs.cues.isNotEmpty()) || ui.styling)) {
            if (ui.styling) {
                val text = subtitleStyleStrings.current().sample
                val sample = remember(text) { listOf(Cue(0.0, Double.MAX_VALUE, text)) }
                SubtitleOverlay(sample, position = { 1.0 }, style = style, modifier = Modifier.fillMaxSize())
            } else {
                SubtitleOverlay(
                    cues = subs.cues,
                    position = engine::positionNow,
                    style = style,
                    modifier = Modifier.fillMaxSize(),
                    delaySeconds = subs.delaySeconds,
                    raised = controls,
                )
            }
        }

        if (playable) PauseScreen(m, shown = ui.pauseScreen)

        PlayerOverlays(
            engine = engine,
            s = s,
            subs = subs,
            ui = ui,
            overlay = overlay,
            controls = controls,
            skipShown = skipShown,
            skipSegs = skipSegs,
            onClose = onClose,
        )

        if (ui.panel && playable) {
            AudioSubtitlePanel(
                s = s,
                subs = subs,
                style = style,
                onAudio = engine::setAudio,
                onSubtitle = engine::setSubtitle,
                onSubDelay = engine::setSubDelay,
                onSubAlt = engine::switchSubAlt,
                onStyle = { next -> engine.setSubtitleStyle(next) },
                onClose = ui::closePanel,
                onStyling = { ui.styling = it },
            )
        }
    }
}

/** Keys every part of the player sees first (webOS Player.tsx key handler). */
private fun rootKey(e: androidx.compose.ui.input.key.KeyEvent, ui: PlayerUi, engine: PlayerEngine, onClose: () -> Unit): Boolean {
    val st = engine.state.value
    val canPlay = st.playable
    val woke = ui.pauseScreen
    ui.reveal()
    val key = e.key
    val media = key in Keys.Stop || key in Keys.Play || key in Keys.Pause || key in Keys.PlayPause ||
        key in Keys.FastForward || key in Keys.Rewind
    // The first key on the pause screen only brings the controls back (media keys still act)
    if (woke && !media) {
        if (key in Keys.Back) ui.swallowBack = true
        return key !in Keys.Back
    }
    when (key) {
        in Keys.Stop -> onClose()
        // A held key repeats: only its first press acts
        in Keys.Play -> if (e.repeatCount == 0) engine.play()
        in Keys.Pause -> if (e.repeatCount == 0) engine.pause()
        in Keys.PlayPause -> if (e.repeatCount == 0) engine.togglePlay()
        in Keys.FastForward, in Keys.Rewind -> if (canPlay) ui.scrub(if (key in Keys.FastForward) 1 else -1, e.repeatCount > 0)
        else -> {
            if (ui.scrubMode) {
                if (key in Keys.Select) {
                    ui.commit()
                    return true
                }
                if (key == Key.DirectionUp || key == Key.DirectionDown) {
                    ui.cancelScrub()
                    return true
                }
            }
            return false
        }
    }
    return true
}
