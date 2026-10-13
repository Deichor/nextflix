package com.nextflix.tv.feature.player

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Shadow
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.Text
import com.nextflix.tv.core.ui.theme.NfType
import kotlin.math.abs
import kotlin.math.roundToInt

private val SLOTS = listOf(-2, -1, 0, 1, 2)
// Frame heights of the center, near and far slots; widths follow the video's aspect ratio
private val HEIGHTS = listOf(144.dp, 94.dp, 72.dp)
private val TOPS = listOf(0.dp, 27.dp, 38.dp)
private val ALPHAS = listOf(1f, 0.7f, 0.4f)
private val FrameShape = RoundedCornerShape(5.dp)

/** Step and direction of the scrub, kept between recompositions. */
private class ScrubTrack(var last: Double, var step: Int = THUMB_GRID, var dir: Int = 1)

/**
 * Netflix-style scrub strip (webOS player/TrickPlay.tsx): the frame at the target time large in the middle, the
 * frames one and two steps away (the current scrub step) smaller at the sides, the target time under it. Frames not
 * loaded yet show their time; seeking never waits for them.
 */
@Composable
internal fun TrickPlay(thumbs: Thumbs, hash: String, index: Int, target: Double, duration: Double, aspect: Float, modifier: Modifier = Modifier) {
    val track = remember(hash, index) { ScrubTrack(target) }
    if (target != track.last) {
        val d = target - track.last
        // Steps are 10, 30 or 60 s; a step shortened at either end of the file keeps the last one
        if (abs(d) >= THUMB_GRID) track.step = (abs(d) / THUMB_GRID).roundToInt() * THUMB_GRID
        track.dir = if (d > 0) 1 else -1
        track.last = target
    }
    val step = track.step
    fun inside(t: Double) = t >= 0 && t <= duration

    LaunchedEffect(target, hash, index, duration) {
        // The center first, then the next frame in the scrub direction, then the rest
        val d = track.dir
        thumbs.want(hash, index, listOf(0, d, -d, 2 * d, -2 * d).map { target + it * step }.filter(::inside))
    }

    // Read so a loaded frame redraws the strip
    thumbs.version.intValue
    val ratio = aspect.coerceIn(4f / 3f, 2.2f)
    Row(modifier, horizontalArrangement = Arrangement.Center, verticalAlignment = Alignment.Top) {
        SLOTS.forEach { o ->
            val t = target + o * step
            val ok = inside(t)
            val slot = abs(o)
            Column(
                Modifier.padding(horizontal = 6.dp).padding(top = TOPS[slot]).alpha(if (ok) ALPHAS[slot] else 0f),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                val height = HEIGHTS[slot]
                val frame = if (ok) thumbs.frame(hash, index, t) else null
                Box(
                    Modifier
                        .height(height)
                        .width(height * ratio)
                        .then(if (o == 0) Modifier.shadow(12.dp, FrameShape).border(2.5.dp, Color.White, FrameShape) else Modifier)
                        .clip(FrameShape)
                        .background(Color(0xFF262626)),
                    contentAlignment = Alignment.Center,
                ) {
                    if (ok) Text(formatTime(t), style = NfType.Body.copy(fontSize = 13.sp), color = Color(0xFF8C8C8C))
                    if (frame != null) {
                        Image(frame, null, Modifier.fillMaxSize().background(Color.Black), contentScale = ContentScale.Fit)
                    }
                }
                if (o == 0) {
                    Text(
                        formatTime(target),
                        modifier = Modifier.padding(top = 8.dp),
                        style = NfType.Body.copy(
                            fontSize = 21.sp,
                            fontWeight = FontWeight.Bold,
                            shadow = Shadow(Color.Black.copy(alpha = 0.9f), blurRadius = 8f),
                        ),
                    )
                }
            }
        }
    }
}
