package com.nextflix.tv.feature.account.intro

import android.app.ActivityManager
import android.content.Context
import android.provider.Settings
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.withTransform
import androidx.compose.ui.graphics.drawscope.translate
import androidx.compose.ui.graphics.drawscope.scale
import androidx.compose.ui.graphics.drawscope.rotate
import androidx.compose.ui.graphics.drawscope.drawIntoCanvas
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.core
import com.nextflix.tv.core.ui.focus.tryFocus
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import kotlin.math.sqrt
import kotlin.math.tan

private enum class Phase { Waiting, Playing, Skipping, Done }

private class Sting(val pcm: ShortArray, val sampleRate: Int)

/** Opening intro (webOS components/Intro.tsx): drawn over the first screen; a key-catching window above it. */
@Composable
internal fun IntroContent(onDone: () -> Unit, onZoom: (Boolean) -> Unit) {
    val core = core()
    val context = LocalContext.current
    val density = LocalDensity.current
    val rem = with(density) { 8.dp.toPx() }
    val done by rememberUpdatedState(onDone)
    val zoom by rememberUpdatedState(onZoom)
    val lite = remember { liteIntro(context) }
    val ribbons = remember { IntroTracks.ribbons(if (lite) 6 else 14) }
    val nTrack = remember { IntroTracks.n(lite) }
    var phase by remember { mutableStateOf(Phase.Waiting) }
    var art by remember { mutableStateOf<NArt?>(null) }
    val clock = remember { mutableFloatStateOf(0f) }
    val fade = remember { Animatable(1f) }
    val player = remember { StingPlayer(core.scope) }
    val scope = rememberCoroutineScope()

    fun finish() {
        if (phase == Phase.Done) return
        phase = Phase.Done
        zoom(false)
        done()
    }

    fun skip() {
        if (phase == Phase.Done || phase == Phase.Skipping) return
        phase = Phase.Skipping
        zoom(false)
        player.stop(Intro.SKIP_MS)
        scope.launch {
            fade.animateTo(0f, tween(Intro.SKIP_MS.toInt(), easing = LinearEasing))
            delay(20)
            finish()
        }
    }

    LaunchedEffect(Unit) {
        val prefs = core.devicePrefs.current()
        val animate = prefs.introAnimation && !reducedMotion(context)
        if (!animate) {
            if (prefs.introSound) playStingAlone(core)
            finish()
            return@LaunchedEffect
        }
        val sting: Deferred<Sting?>? = if (prefs.introSound) async(Dispatchers.Default) { renderSting() } else null
        art = withContext(Dispatchers.Default) { runCatching { drawN(context, rem, lite) }.getOrNull() }
        whenOnScreen()
        if (phase != Phase.Waiting) return@LaunchedEffect

        phase = Phase.Playing
        if (!lite) zoom(true)
        val start = withFrameNanos { it }
        if (sting != null) {
            launch {
                val s = sting.await() ?: return@launch
                if (phase == Phase.Playing) player.play(s.pcm, s.sampleRate, elapsedSince(start))
            }
        }
        while (phase == Phase.Playing) {
            val now = withFrameNanos { it }
            val t = (now - start) / 1e9f
            clock.floatValue = t
            if (t >= Intro.DURATION + 0.06f) finish()
        }
    }

    // Home button or another app: end at once
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle) {
        val observer = LifecycleEventObserver { _, event -> if (event == Lifecycle.Event.ON_STOP) skip() }
        lifecycle.addObserver(observer)
        onDispose {
            lifecycle.removeObserver(observer)
            player.stop(Intro.SKIP_MS)
        }
    }

    if (phase == Phase.Done) return

    Canvas(
        Modifier
            .fillMaxSize()
            .graphicsLayer { alpha = IntroTracks.stage.at(clock.floatValue, 0) * fade.value },
    ) {
        drawRect(Color.Black)
        if (phase == Phase.Waiting) return@Canvas
        drawStage(clock.floatValue, rem, ribbons, nTrack, art)
    }

    KeyCatcher(onKey = ::skip)
}

/**
 * An invisible window above the app while the intro shows: it takes every key (any key skips, Back too), so the
 * screen loading below never sees them, like the webOS capture listener.
 */
@Composable
private fun KeyCatcher(onKey: () -> Unit) {
    Dialog(
        onDismissRequest = onKey,
        properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false),
    ) {
        val window = (LocalView.current.parent as? DialogWindowProvider)?.window
        SideEffect {
            window?.setDimAmount(0f)
            window?.setWindowAnimations(0)
        }
        val focus = remember { FocusRequester() }
        Box(
            Modifier
                .fillMaxSize()
                .focusRequester(focus)
                .onPreviewKeyEvent {
                    onKey()
                    true
                }
                .focusable(),
        )
        LaunchedEffect(Unit) { focus.tryFocus() }
    }
}

private fun DrawScope.drawStage(t: Float, rem: Float, ribbons: List<Ribbon>, n: Track, art: NArt?) {
    val c = center

    radial(c, 90f * rem, IntroTracks.glow.at(t, 0), IntroTracks.glow.at(t, 1), GlowStops)

    for (r in ribbons) {
        val op = r.track.at(t, 5)
        if (op <= 0.001f) continue
        val w = r.width * rem
        val h = 72f * rem
        withTransform({
            translate(c.x + r.track.at(t, 0) * rem, c.y + r.track.at(t, 1) * rem)
            rotate(r.track.at(t, 2), pivot = Offset.Zero)
            scale(r.track.at(t, 3), r.track.at(t, 4), pivot = Offset.Zero)
        }) {
            drawRect(
                Brush.verticalGradient(*RibbonTones[r.tone], startY = -h / 2, endY = h / 2),
                topLeft = Offset(-w / 2, -h / 2),
                size = Size(w, h),
                alpha = op,
            )
        }
    }

    radial(c, 44f * rem, IntroTracks.flash.at(t, 0), IntroTracks.flash.at(t, 1), FlashStops)

    if (art != null) {
        val s = n.at(t, 0)
        val op = n.at(t, 1)
        if (op > 0.001f) {
            val faceTopLeft = Offset(c.x - art.faceWidth / 2, c.y - art.faceHeight / 2)
            scale(s, s, pivot = c) {
                drawImage(
                    art.face,
                    dstOffset = IntOffset(faceTopLeft.x.toInt(), faceTopLeft.y.toInt()),
                    dstSize = IntSize(art.faceWidth.toInt(), art.faceHeight.toInt()),
                    alpha = op,
                )
                val hot = IntroTracks.hot.at(t, 0) * op
                if (hot > 0.001f) {
                    drawImage(
                        art.hot,
                        dstOffset = IntOffset((faceTopLeft.x + art.hotOffset).toInt(), (faceTopLeft.y + art.hotOffset).toInt()),
                        dstSize = IntSize(art.hotWidth.toInt(), art.hotHeight.toInt()),
                        alpha = hot,
                    )
                }
            }
        }
    }

    val sweepOp = IntroTracks.sweep.at(t, 1)
    if (sweepOp > 0.001f) {
        val w = 24f * rem
        val h = 90f * rem
        translate(c.x + IntroTracks.sweep.at(t, 0) * rem, c.y) {
            drawIntoCanvas { it.nativeCanvas.skew(SweepSkew, 0f) }
            drawRect(
                Brush.horizontalGradient(SweepStops, startX = -w / 2, endX = w / 2),
                topLeft = Offset(-w / 2, -h / 2),
                size = Size(w, h),
                alpha = sweepOp,
            )
        }
    }
}

/** CSS `radial-gradient(circle, …)` of a square box of [side], scaled about the center. */
private fun DrawScope.radial(c: Offset, side: Float, zoom: Float, alpha: Float, stops: Array<Pair<Float, Color>>) {
    if (alpha <= 0.001f) return
    scale(zoom, zoom, pivot = c) {
        drawRect(
            Brush.radialGradient(*stops, center = c, radius = side / sqrt(2f)),
            topLeft = Offset(c.x - side / 2, c.y - side / 2),
            size = Size(side, side),
            alpha = alpha,
        )
    }
}

private val SweepSkew = tan(Math.toRadians(-18.0)).toFloat()

private val GlowStops = arrayOf(0f to Color(229, 9, 20).copy(alpha = 0.55f), 0.32f to Color(150, 0, 18).copy(alpha = 0.25f), 0.62f to Color.Transparent)
private val FlashStops = arrayOf(0f to Color(255, 236, 236).copy(alpha = 0.9f), 0.26f to Color(255, 80, 92).copy(alpha = 0.45f), 0.62f to Color(255, 80, 92).copy(alpha = 0f))
private val SweepStops = listOf(Color(255, 90, 100).copy(alpha = 0f), Color(255, 90, 100).copy(alpha = 0.2f), Color(255, 90, 100).copy(alpha = 0f))

private val RibbonTones = listOf(
    ribbonStops(Color(178, 7, 16), 0.6f, 0.32f, Color(236, 22, 34).copy(alpha = 0.95f)),
    ribbonStops(Color(255, 70, 82), 0.5f, 0.38f, Color(255, 140, 146).copy(alpha = 0.9f)),
    ribbonStops(Color(128, 0, 20), 0.75f, 0.4f, Color(176, 6, 22).copy(alpha = 0.9f)),
)

/** Transparent ends, [edge] at [at] and 1 - [at], [mid] in the middle (styles.css .intro-ribbon-*). */
private fun ribbonStops(edge: Color, edgeAlpha: Float, at: Float, mid: Color) = arrayOf(
    0f to edge.copy(alpha = 0f),
    at to edge.copy(alpha = edgeAlpha),
    0.5f to mid,
    1f - at to edge.copy(alpha = edgeAlpha),
    1f to edge.copy(alpha = 0f),
)

private fun elapsedSince(startNanos: Long): Double = (System.nanoTime() - startNanos) / 1e9

private fun renderSting(): Sting? = runCatching {
    val rate = StingPlayer.outputSampleRate()
    Sting(StingSynth.renderPcm(rate), rate)
}.getOrNull()

/** Sound without the animation: it plays on the app scope, the intro itself is over at once. */
private fun playStingAlone(core: Core) {
    val player = StingPlayer(core.scope)
    core.scope.launch(Dispatchers.Default) {
        val s = renderSting() ?: return@launch
        player.play(s.pcm, s.sampleRate)
    }
}

/** Held on the black first frame until frames come steadily (the first ones after launch can stall). */
private suspend fun whenOnScreen() {
    withTimeoutOrNull(8_000) {
        var last = 0L
        var steady = 0
        while (steady < 4) {
            val now = withFrameNanos { it }
            steady = if (last != 0L && now - last <= 100_000_000L) steady + 1 else 0
            last = now
        }
    }
}

/** "Remove animations" in the system settings. */
private fun reducedMotion(context: Context): Boolean =
    Settings.Global.getFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f

/** Few cores or a low-RAM device: fewer ribbons, a smaller zoom. */
private fun liteIntro(context: Context): Boolean {
    val am = context.getSystemService(ActivityManager::class.java)
    return Runtime.getRuntime().availableProcessors() <= 2 || am?.isLowRamDevice == true
}
