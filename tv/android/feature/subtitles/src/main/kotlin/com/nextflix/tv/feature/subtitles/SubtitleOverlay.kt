package com.nextflix.tv.feature.subtitles

import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Shadow
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.DpSize
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.nextflix.tv.core.prefs.SubtitleBackground
import com.nextflix.tv.core.prefs.SubtitleDimming
import com.nextflix.tv.core.prefs.SubtitleEdge
import com.nextflix.tv.core.prefs.SubtitlePosition
import com.nextflix.tv.core.prefs.SubtitleStyle
import com.nextflix.tv.core.ui.components.SubtitleSampleRenderer
import com.nextflix.tv.core.ui.theme.Inter
import kotlinx.coroutines.delay

// Sizes of webOS styles.css (.subtitles, .subtitle-scrim): 1rem = 8 dp
private val BASE_FONT = 23.2.sp
private const val SIDE_FRACTION = 0.06f
private const val BOTTOM_FRACTION = 0.07f
private const val RAISED_POS_FRACTION = 0.16f
private val ABOVE_CONTROLS = 136.dp
private const val SCRIM_HOLD_MS = 1500L

/**
 * Draws the cues active at `position() - delaySeconds` over the video (webOS player/SubtitleOverlay.tsx).
 *
 * - [position] is read in a timer loop (not per recomposition): the overlay checks again right after the next cue
 *   boundary (at most every 100 ms), so cues appear within a few ms of their time.
 * - [style]: size, background box per line, dimming band (bottom 40 %, fades in while cues show, stays through
 *   gaps under 1.5 s), edge (shadow / outline / none), color, position. [raised]: the player's controls are
 *   visible, move the text up above them.
 * - Cue text keeps <i>, <b>, <u> only; line breaks kept. Nothing else is interpreted as markup.
 * - [sample]: shown while no cue does (live preview of the appearance panel); [endInset] keeps the text clear of
 *   a panel on the right meanwhile.
 *
 * Fills its parent (the player's video area).
 */
@Composable
fun SubtitleOverlay(
    cues: List<Cue>,
    position: () -> Double,
    style: SubtitleStyle,
    modifier: Modifier = Modifier,
    delaySeconds: Double = 0.0,
    raised: Boolean = false,
    sample: String? = null,
    endInset: Dp = 0.dp,
) {
    val currentPosition by rememberUpdatedState(position)
    var shown by remember { mutableStateOf(emptyList<Cue>()) }
    LaunchedEffect(cues, delaySeconds) {
        if (cues.isEmpty()) {
            shown = emptyList()
            return@LaunchedEffect
        }
        while (true) {
            val t = currentPosition() - delaySeconds
            val now = CueLookup.active(cues, t)
            if (now != shown) shown = now
            delay(CueLookup.checkDelayMs(cues, t))
        }
    }

    val texts = if (shown.isNotEmpty()) shown.map { it.text } else listOfNotNull(sample)
    val on = texts.isNotEmpty()

    // The dimmed band stays through short gaps between cues instead of blinking
    var scrim by remember { mutableStateOf(false) }
    LaunchedEffect(on) {
        if (on) {
            scrim = true
        } else {
            delay(SCRIM_HOLD_MS)
            scrim = false
        }
    }
    val scrimAlpha by animateFloatAsState(if (scrim) 1f else 0f, tween(400), label = "scrim")

    BoxWithConstraints(modifier.fillMaxSize()) {
        val bottom by animateDpAsState(
            when {
                raised -> ABOVE_CONTROLS
                style.position == SubtitlePosition.Raised -> maxHeight * RAISED_POS_FRACTION
                else -> maxHeight * BOTTOM_FRACTION
            },
            tween(300),
            label = "subtitleBottom",
        )
        if (style.dimming != SubtitleDimming.None) {
            Box(
                Modifier
                    .align(Alignment.BottomCenter)
                    .fillMaxWidth()
                    .fillMaxHeight(0.4f)
                    .graphicsLayer { alpha = scrimAlpha }
                    .background(dimmingBrush(style.dimming)),
            )
        }
        if (on) {
            val side = maxWidth * SIDE_FRACTION
            val width = (maxWidth - side * 2 - endInset).coerceAtLeast(0.dp)
            CueText(
                texts = texts,
                style = style,
                fontSize = BASE_FONT * style.size.scale,
                modifier = Modifier
                    .align(Alignment.BottomStart)
                    .padding(start = side, bottom = bottom)
                    .width(width),
            )
        }
    }
}

/**
 * One sample line in [style] (Settings preview, the player's appearance panel). With a bounded size it is a small
 * screen: the dimming band and the text at the bottom, the text scaled to the width (webOS .sub-preview, about
 * twice the real size). Without a bounded height only the line is drawn.
 */
@Composable
fun SubtitleSample(text: String, style: SubtitleStyle, modifier: Modifier = Modifier) {
    BoxWithConstraints(modifier) {
        if (constraints.hasBoundedWidth && constraints.hasBoundedHeight) {
            val fontSize = (maxWidth.value * PREVIEW_FONT_PER_WIDTH).sp
            if (style.dimming != SubtitleDimming.None) {
                Box(
                    Modifier
                        .align(Alignment.BottomCenter)
                        .fillMaxWidth()
                        .fillMaxHeight(0.4f)
                        .background(dimmingBrush(style.dimming)),
                )
            }
            val bottom = maxHeight * if (style.position == SubtitlePosition.Raised) 0.2f else 0.08f
            CueText(
                texts = listOf(text),
                style = style,
                fontSize = fontSize * style.size.scale,
                modifier = Modifier
                    .align(Alignment.BottomStart)
                    .padding(start = maxWidth * 0.04f, bottom = bottom)
                    .width(maxWidth * 0.92f),
            )
        } else {
            CueText(listOf(text), style, BASE_FONT * style.size.scale, Modifier.fillMaxWidth())
        }
    }
}

// .sub-preview: 1.3rem text in a 27rem wide box
private const val PREVIEW_FONT_PER_WIDTH = 1.3f / 27f

/** For the app to provide as LocalSubtitleSample. */
val SubtitleSampleRendererImpl = SubtitleSampleRenderer { text, style, modifier -> SubtitleSample(text, style, modifier) }

private fun dimmingBrush(dimming: SubtitleDimming): Brush = when (dimming) {
    SubtitleDimming.Dark -> Brush.verticalGradient(
        0f to Color.Transparent,
        0.5f to Color.Black.copy(alpha = 0.7f),
        1f to Color.Black.copy(alpha = 0.9f),
    )
    else -> Brush.verticalGradient(
        0f to Color.Transparent,
        0.55f to Color.Black.copy(alpha = 0.35f),
        1f to Color.Black.copy(alpha = 0.55f),
    )
}

internal fun cueAnnotatedString(texts: List<String>): AnnotatedString = buildAnnotatedString {
    texts.forEachIndexed { i, text ->
        if (i > 0) append('\n')
        for (run in CueMarkup.runs(text)) {
            if (!run.italic && !run.bold && !run.underline) {
                append(run.text)
                continue
            }
            val span = SpanStyle(
                fontStyle = if (run.italic) FontStyle.Italic else null,
                fontWeight = if (run.bold) FontWeight.ExtraBold else null,
                textDecoration = if (run.underline) TextDecoration.Underline else null,
            )
            withStyle(span) { append(run.text) }
        }
    }
}

/** Centered cue lines: one background box per line, the edge drawn under the fill. */
@Composable
private fun CueText(texts: List<String>, style: SubtitleStyle, fontSize: TextUnit, modifier: Modifier) {
    val text = remember(texts) { cueAnnotatedString(texts) }
    val measurer = rememberTextMeasurer(cacheSize = 4)
    val density = LocalDensity.current
    BoxWithConstraints(modifier) {
        val textStyle = TextStyle(
            color = style.color.color,
            fontFamily = Inter,
            fontWeight = FontWeight.SemiBold,
            fontSize = fontSize,
            lineHeight = fontSize * 1.25f,
            textAlign = TextAlign.Center,
            lineHeightStyle = LineHeightStyle(LineHeightStyle.Alignment.Center, LineHeightStyle.Trim.None),
        )
        val maxWidthPx = constraints.maxWidth
        val layout = remember(text, textStyle, maxWidthPx, density) {
            measurer.measure(
                text,
                textStyle,
                constraints = if (maxWidthPx == Constraints.Infinity) Constraints() else Constraints(maxWidth = maxWidthPx),
                density = density,
            )
        }
        val em = with(density) { fontSize.toPx() }
        val size = with(density) { DpSize(layout.size.width.toDp(), layout.size.height.toDp()) }
        Canvas(Modifier.align(Alignment.BottomCenter).size(size)) {
            drawCue(layout, style, em)
        }
    }
}

private fun DrawScope.drawCue(layout: TextLayoutResult, style: SubtitleStyle, em: Float) {
    if (style.background != SubtitleBackground.None) {
        val padX = 0.3f * em
        for (i in 0 until layout.lineCount) {
            val left = layout.getLineLeft(i)
            val right = layout.getLineRight(i)
            if (right - left < 1f) continue
            val top = layout.getLineTop(i)
            drawRect(
                style.background.boxColor,
                topLeft = Offset(left - padX, top),
                size = Size(right - left + padX * 2, layout.getLineBottom(i) - top),
            )
        }
    }
    when (style.edge) {
        SubtitleEdge.Outline -> {
            // Eight hard 0.05 em shadows in CSS: a 0.1 em stroke centered on the outline
            drawText(layout, color = Color.Black, drawStyle = Stroke(width = 0.1f * em, join = StrokeJoin.Round))
            drawText(layout)
        }
        SubtitleEdge.Shadow -> {
            drawText(layout, shadow = Shadow(Color.Black.copy(alpha = 0.95f), Offset.Zero, 0.086f * em))
            drawText(layout, shadow = Shadow(Color.Black.copy(alpha = 0.8f), Offset(0f, 0.041f * em), 0.086f * em))
        }
        SubtitleEdge.None -> drawText(layout)
    }
}
