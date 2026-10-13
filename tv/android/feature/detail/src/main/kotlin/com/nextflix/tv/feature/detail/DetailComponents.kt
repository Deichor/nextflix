package com.nextflix.tv.feature.detail

import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.BringIntoViewSpec
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInParent
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.Border
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.LocalCore
import com.nextflix.tv.core.i18n.Formats
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.EpisodeInfo
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.ui.components.CardImage
import com.nextflix.tv.core.ui.components.CardSkeleton
import com.nextflix.tv.core.ui.components.FocusCard
import com.nextflix.tv.core.ui.components.ProgressBar
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import kotlin.math.min
import kotlin.math.roundToInt

internal val EpisodeStillWidth = 240.dp
internal val EpisodeStillHeight = 135.dp

/** The page's own vertical scrolling: focus anywhere in a section shows the whole section (webOS scroll anchors). */
@OptIn(ExperimentalFoundationApi::class)
internal val NoBringIntoView = object : BringIntoViewSpec {
    override fun calculateScrollDistance(offset: Float, size: Float, containerSize: Float): Float = 0f
}

/** Scrolls only when the focused element is closer than [padding] to an edge (webOS "edge" rows). */
@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun rememberEdgeBringIntoView(padding: Dp): BringIntoViewSpec {
    val pad = with(LocalDensity.current) { padding.toPx() }
    return remember(pad) {
        object : BringIntoViewSpec {
            override fun calculateScrollDistance(offset: Float, size: Float, containerSize: Float): Float = when {
                offset < pad -> offset - pad
                offset + size > containerSize - pad -> min(offset + size - containerSize + pad, offset - pad)
                else -> 0f
            }
        }
    }
}

/** "edge" scrolling of the page with sections as anchors and [padPx] kept free at the top and bottom. */
@Stable
internal class SectionScroller(private val state: ScrollState, private val scope: CoroutineScope, private val padPx: Float) {
    var viewport = 0

    fun reveal(top: Float, size: Float) {
        val off = state.value.toFloat()
        val rel = top - off
        val target = when {
            rel < padPx -> top - padPx
            rel + size > viewport - padPx -> off + min(rel + size - viewport + padPx, rel - padPx)
            else -> off
        }
        val to = target.roundToInt().coerceIn(0, state.maxValue)
        if (to != state.value) scope.launch { state.animateScrollTo(to, tween(220)) }
    }
}

@Composable
internal fun rememberSectionScroller(state: ScrollState, padding: Dp): SectionScroller {
    val scope = rememberCoroutineScope()
    val pad = with(LocalDensity.current) { padding.toPx() }
    return remember(state, scope, pad) { SectionScroller(state, scope, pad) }
}

private class Bounds(var top: Float = 0f, var height: Float = 0f)

/** Marks a direct child of the scrolled column as a section. */
@Composable
internal fun Modifier.section(scroller: SectionScroller): Modifier {
    val bounds = remember { Bounds() }
    return this
        .onGloballyPositioned {
            bounds.top = it.positionInParent().y
            bounds.height = it.size.height.toFloat()
        }
        .onFocusChanged { if (it.hasFocus) scroller.reveal(bounds.top, bounds.height) }
}

/** Backdrop at the top right with the page's shades (webOS .detail-bg). */
@Composable
internal fun DetailBackground(url: String) {
    BoxWithConstraints(Modifier.fillMaxSize()) {
        if (url.isNotEmpty()) {
            CardImage(url, Modifier.align(Alignment.TopEnd).size(maxWidth * 0.8f, maxHeight * 0.8f))
        }
        val bg = NfColors.Background
        Box(
            Modifier
                .fillMaxSize()
                .background(Brush.horizontalGradient(0.25f to bg, 0.45f to bg.copy(alpha = 0.8f), 0.75f to bg.copy(alpha = 0.1f))),
        )
        Box(Modifier.fillMaxSize().background(Brush.verticalGradient(0.45f to Color.Transparent, 0.78f to bg)))
    }
}

private val ButtonShape = RoundedCornerShape(4.dp)
private val FocusRing = Border(BorderStroke(2.dp, NfColors.Focus), inset = (-3).dp, shape = ButtonShape)

internal enum class ActionKind { Primary, Secondary, Disabled }

/**
 * The page's wide buttons (core NfButton look) with an extra bold [detail] after the label ("Resume S1:E2").
 * Disabled buttons stay focusable, like the webOS app's date button.
 */
@Composable
internal fun ActionButton(
    text: String,
    icon: ImageVector,
    kind: ActionKind,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    detail: String = "",
) {
    val container = when (kind) {
        ActionKind.Primary -> Color.White
        ActionKind.Secondary -> NfColors.Secondary
        ActionKind.Disabled -> Color.White.copy(alpha = 0.22f)
    }
    val content = when (kind) {
        ActionKind.Primary -> Color.Black
        ActionKind.Secondary -> Color.White
        ActionKind.Disabled -> Color.White.copy(alpha = 0.85f)
    }
    Surface(
        onClick = onClick,
        modifier = modifier.height(Dimens.ButtonHeight),
        shape = ClickableSurfaceDefaults.shape(ButtonShape),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = container,
            contentColor = content,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
            pressedContainerColor = Color.White,
            pressedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.08f),
        border = ClickableSurfaceDefaults.border(focusedBorder = FocusRing),
    ) {
        Row(Modifier.padding(horizontal = 20.dp).align(Alignment.Center), verticalAlignment = Alignment.CenterVertically) {
            Icon(icon, contentDescription = null, modifier = Modifier.size(17.dp))
            Spacer(Modifier.width(7.dp))
            val label = if (detail.isEmpty()) {
                buildAnnotatedString { append(text) }
            } else {
                buildAnnotatedString {
                    append(text)
                    append("  ")
                    withStyle(SpanStyle(fontWeight = FontWeight.ExtraBold)) { append(detail) }
                }
            }
            Text(label, style = NfType.Button, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

/** One episode: the still (focusable, progress bar), name, length or air date, and two lines of overview. */
@Composable
internal fun EpisodeCard(
    episode: EpisodeInfo,
    fallbackImage: String,
    progress: StreamProgress?,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val core = LocalCore.current
    val language = LocalAppLanguage.current
    val s = strings()
    Column(Modifier.width(EpisodeStillWidth).heightIn(min = 212.dp)) {
        FocusCard(onClick = onClick, modifier = modifier, width = EpisodeStillWidth, height = EpisodeStillHeight) {
            Box(Modifier.fillMaxSize()) {
                CardImage(core.imageUrl(episode.stillPath.ifEmpty { fallbackImage }, TmdbImages.W300), Modifier.fillMaxSize())
                if (progress != null && progress.position > 0) {
                    ProgressBar(progress.fraction, Modifier.align(Alignment.BottomStart).fillMaxWidth())
                }
            }
        }
        Text(
            if (episode.hasRealName) "${episode.episodeNumber}. ${episode.name}" else s.episodeN(episode.episodeNumber),
            style = NfType.Body.copy(fontSize = 12.8.sp, fontWeight = FontWeight.SemiBold),
            color = Color.White,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(top = 10.dp),
        )
        val meta = when {
            isFuture(episode.airDate) -> s.airsOn(Formats.date(language, episode.airDate))
            episode.runtime > 0 -> Formats.duration(language, episode.runtime)
            else -> ""
        }
        Text(meta, style = NfType.BodySmall.copy(fontSize = 10.4.sp), color = NfColors.Dim, maxLines = 1, modifier = Modifier.padding(top = 2.dp))
        if (episode.overview.isNotEmpty()) {
            Text(
                episode.overview,
                style = NfType.BodySmall.copy(fontSize = 10.4.sp, lineHeight = 14.5.sp),
                color = NfColors.Muted,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(top = 4.dp),
            )
        }
    }
}

@Composable
internal fun EpisodeSkeleton() {
    CardSkeleton(width = EpisodeStillWidth, height = EpisodeStillHeight)
}
