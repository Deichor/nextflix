package com.nextflix.tv.core.ui.components

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animate
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.State
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.Canvas
import androidx.compose.ui.graphics.drawscope.Fill
import androidx.tv.material3.Border
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import coil3.compose.AsyncImage
import coil3.request.ImageRequest
import com.nextflix.tv.core.LocalCore
import com.nextflix.tv.core.data.InstantItem
import com.nextflix.tv.core.i18n.Formats
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.icons.filled
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.Inter
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.launch

private val CardShape = RoundedCornerShape(Dimens.CardRadius)
private val CardRing = Border(BorderStroke(Dimens.FocusBorder, NfColors.Focus), shape = CardShape)
private val Bolt by lazy { Lucide.Zap.filled() }

/** Image of a card: cropped to fill, retried twice after a failure (the server may have timed out on TMDB). */
@Composable
fun CardImage(url: String, modifier: Modifier = Modifier, contentScale: ContentScale = ContentScale.Crop) {
    if (url.isEmpty()) return
    var attempt by remember(url) { mutableIntStateOf(0) }
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val request = remember(url, attempt) {
        ImageRequest.Builder(context).data(if (attempt > 0) "$url?retry=$attempt" else url).build()
    }
    AsyncImage(
        model = request,
        contentDescription = null,
        contentScale = contentScale,
        modifier = modifier,
        onError = {
            if (attempt < 2) {
                scope.launch {
                    delay(1500L * (attempt + 1))
                    attempt++
                }
            }
        },
    )
}

private object FullAlpha : State<Float> {
    override val value: Float = 1f
}

/** Alpha of the skeleton cards' pulse. A screen provides one [rememberSkeletonPulse]; the default does not pulse. */
val LocalSkeletonPulse = staticCompositionLocalOf<State<Float>> { FullAlpha }

/**
 * One pulse for every skeleton of a screen, running only while [active] (something loads). The State stays the
 * same object, so providing it never recomposes the screen; the cards read it while drawing.
 */
@Composable
fun rememberSkeletonPulse(active: Boolean): State<Float> {
    val pulse = remember { mutableFloatStateOf(1f) }
    LaunchedEffect(active) {
        if (active) {
            animate(1f, 0.55f, animationSpec = infiniteRepeatable(tween(700), RepeatMode.Reverse)) { value, _ -> pulse.floatValue = value }
        } else {
            pulse.floatValue = 1f
        }
    }
    return pulse
}

/** Placeholder card while a row loads (pulses with [LocalSkeletonPulse], redrawn without recomposing). */
@Composable
fun CardSkeleton(width: Dp = Dimens.BackdropCardWidth, height: Dp = Dimens.BackdropCardHeight, modifier: Modifier = Modifier) {
    val pulse = LocalSkeletonPulse.current
    Box(modifier.size(width, height).graphicsLayer { alpha = pulse.value }.background(NfColors.Skeleton, CardShape))
}

/** True when the title plays at once from the TorBox cache (asked only while [enabled]). */
@Composable
fun rememberInstant(item: InstantItem, enabled: Boolean = true): Boolean {
    val repo = LocalCore.current.instant
    val flow = remember(item.key, enabled) { if (enabled) repo.observe(item) else flowOf(repo.peek(item.key)) }
    val value by flow.collectAsState(initial = repo.peek(item.key))
    return value == true
}

/** Lightning bolt in the card corner: plays at once (TorBox cached). */
@Composable
fun InstantBadge(item: InstantItem, modifier: Modifier = Modifier) {
    if (!rememberInstant(item)) return
    Box(
        modifier
            .size(19.dp)
            .background(Color(0xB8000000), CircleShape),
        contentAlignment = Alignment.Center,
    ) {
        Icon(Bolt, contentDescription = strings().instant, tint = NfColors.TextSoft, modifier = Modifier.size(11.dp))
    }
}

/** Base of every card: rounded, scales to 1.08 with a white ring when focused. [onFocused] runs when it gets focus. */
@Composable
fun FocusCard(
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    width: Dp = Dimens.BackdropCardWidth,
    height: Dp = Dimens.BackdropCardHeight,
    onFocused: () -> Unit = {},
    focusedScale: Float = 1.08f,
    content: @Composable () -> Unit,
) {
    Surface(
        onClick = onClick,
        modifier = modifier
            .size(width, height)
            .onFocusChanged { if (it.isFocused) onFocused() },
        shape = ClickableSurfaceDefaults.shape(CardShape),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = NfColors.Card,
            focusedContainerColor = NfColors.Card,
            pressedContainerColor = NfColors.Card,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = focusedScale),
        border = ClickableSurfaceDefaults.border(focusedBorder = CardRing),
    ) {
        content()
    }
}

/** 16:9 card with the backdrop, the title over a gradient and the instant bolt (rows). */
@Composable
fun BackdropCard(title: Title, onClick: () -> Unit, modifier: Modifier = Modifier, onFocused: () -> Unit = {}) {
    val core = LocalCore.current
    FocusCard(onClick = onClick, modifier = modifier, onFocused = onFocused) {
        Box(Modifier.fillMaxSize()) {
            CardImage(core.imageUrl(title.backdrop.ifEmpty { title.poster }, TmdbImages.W500), Modifier.fillMaxSize())
            Caption(title.title, sub = null, Modifier.align(Alignment.BottomStart))
            InstantBadge(InstantItem(title.type, title.id, title.title), Modifier.align(Alignment.TopEnd).padding(6.dp))
        }
    }
}

/** 2:3 poster card (search results, rank rows). */
@Composable
fun PosterCard(title: Title, onClick: () -> Unit, modifier: Modifier = Modifier, onFocused: () -> Unit = {}) {
    val core = LocalCore.current
    FocusCard(onClick = onClick, modifier = modifier, width = Dimens.PosterCardWidth, height = Dimens.PosterCardHeight, onFocused = onFocused, focusedScale = 1.06f) {
        Box(Modifier.fillMaxSize()) {
            CardImage(core.imageUrl(title.poster.ifEmpty { title.backdrop }, TmdbImages.W342), Modifier.fillMaxSize())
            InstantBadge(InstantItem(title.type, title.id, title.title), Modifier.align(Alignment.TopEnd).padding(6.dp))
        }
    }
}

/** Top 10 card: the big outlined rank number with the poster overlapping it. */
@Composable
fun RankCard(title: Title, rank: Int, onClick: () -> Unit, modifier: Modifier = Modifier, onFocused: () -> Unit = {}) {
    var focused by remember { mutableStateOf(false) }
    val measurer = rememberTextMeasurer()
    Row(modifier.height(Dimens.PosterCardHeight), verticalAlignment = Alignment.Bottom) {
        val numberStyle = TextStyle(fontFamily = Inter, fontWeight = FontWeight.ExtraBold, fontSize = 136.sp, letterSpacing = (-0.06).em)
        Canvas(Modifier.width(68.dp).height(Dimens.PosterCardHeight).offset(x = 12.dp)) {
            val layout = measurer.measure(rank.toString(), numberStyle)
            val x = size.width - layout.size.width
            val y = size.height - layout.size.height * 0.92f
            drawText(layout, color = NfColors.Background, topLeft = Offset(x, y), drawStyle = Fill)
            drawText(
                layout,
                color = if (focused) Color.White else Color(0xFF595959),
                topLeft = Offset(x, y),
                drawStyle = Stroke(width = 2.8.dp.toPx()),
            )
        }
        PosterCard(title, onClick, Modifier.onFocusChanged { focused = it.isFocused }, onFocused = onFocused)
    }
}

/**
 * Continue Watching card: backdrop, title and "S1:E2", a red progress bar. [upcoming] ("New episode Thursday")
 * replaces the episode label and the bar for an episode not out yet.
 */
@Composable
fun ContinueCard(
    progress: StreamProgress,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    onFocused: () -> Unit = {},
    upcoming: String? = null,
) {
    val core = LocalCore.current
    val language = LocalAppLanguage.current
    FocusCard(onClick = onClick, modifier = modifier, onFocused = onFocused) {
        Box(Modifier.fillMaxSize()) {
            CardImage(core.imageUrl(progress.backdropPath.ifEmpty { progress.posterPath }, TmdbImages.W500), Modifier.fillMaxSize())
            val label = upcoming ?: Formats.episode(language, progress.season, progress.episode)
            Caption(progress.title, sub = label.ifEmpty { null }, Modifier.align(Alignment.BottomStart).padding(bottom = 4.dp))
            if (upcoming == null) {
                InstantBadge(
                    InstantItem(progress.mediaType, progress.tmdbId, progress.title, progress.season.takeIf { it > 0 }, progress.episode.takeIf { it > 0 }),
                    Modifier.align(Alignment.TopEnd).padding(6.dp),
                )
                ProgressBar(progress.fraction, Modifier.align(Alignment.BottomStart).fillMaxWidth())
            }
        }
    }
}

/** Thin red progress bar on a grey track (cards, episode stills). */
@Composable
fun ProgressBar(fraction: Float, modifier: Modifier = Modifier, height: Dp = 4.dp) {
    Box(modifier.height(height).background(NfColors.ProgressTrack)) {
        Box(Modifier.fillMaxWidth(fraction.coerceIn(0f, 1f)).height(height).background(NfColors.Progress))
    }
}

@Composable
private fun Caption(text: String, sub: String?, modifier: Modifier = Modifier) {
    Column(
        modifier
            .fillMaxWidth()
            .background(Brush.verticalGradient(listOf(Color.Transparent, Color(0xD9000000))))
            .padding(start = 10.dp, end = 10.dp, top = 24.dp, bottom = 8.dp),
    ) {
        Text(text, style = NfType.Caption, color = Color.White, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (sub != null) Text(sub, style = NfType.Caption.copy(fontSize = 10.sp), color = NfColors.Muted, maxLines = 1)
    }
}
