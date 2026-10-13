package com.nextflix.tv.feature.browse

import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.layout
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import androidx.tv.material3.Text
import com.nextflix.tv.core.LocalCore
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.CoreStrings
import com.nextflix.tv.core.i18n.Formats
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.FadeInImage
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.icons.filled
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlin.math.roundToInt

/** What a rows screen's hero shows: the focused card's title (webOS components/heroStore.ts). */
@Immutable
internal data class HeroItem(
    val key: String,
    val title: String,
    val overview: String,
    val backdrop: String,
    /** small facts under the title: match, year, "S1:E2" ... (the first one is green) */
    val facts: List<String>,
    val kicker: String? = null,
)

internal fun heroOf(t: Title, s: CoreStrings, kicker: String? = null): HeroItem {
    val facts = buildList {
        t.matchPercent.takeIf { it > 0 }?.let { add(s.match(it)) }
        t.year?.let { add(it.toString()) }
        add(if (t.type == MediaType.Tv) s.tvShows else s.movies)
    }
    return HeroItem(t.key, t.title, t.overview, t.backdrop, facts, kicker)
}

internal fun heroOf(p: StreamProgress, s: CoreStrings, language: AppLanguage): HeroItem {
    val facts = listOf(Formats.episode(language, p.season, p.episode), p.episodeTitle).filter { it.isNotEmpty() }.toMutableList()
    val left = maxOf(1, (p.remaining / 60).roundToInt())
    facts += s.timeLeft(Formats.duration(language, left))
    return HeroItem("cw-${p.key}", p.title, "", p.backdropPath, facts)
}

/**
 * The focused title, applied once focus rests (350 ms): holding a direction key never repaints the hero. Only
 * the hero reads it, so a focus move does not recompose the rows.
 */
@Stable
internal class HeroState(private val scope: CoroutineScope) {
    var item by mutableStateOf<HeroItem?>(null)
        private set
    private var job: Job? = null

    fun show(next: HeroItem?, immediate: Boolean = false) {
        job?.cancel()
        if (immediate) {
            apply(next)
        } else {
            job = scope.launch {
                delay(350)
                apply(next)
            }
        }
    }

    private fun apply(next: HeroItem?) {
        if (item?.key != next?.key) item = next
    }
}

private val FactGreen = Color(0xFF46D369)

/** Facts in a row, the first one green (webOS .hero-facts). */
@Composable
internal fun Facts(facts: List<String>, modifier: Modifier = Modifier, fontSize: Float = 12.8f) {
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(11.dp)) {
        facts.forEachIndexed { i, f ->
            Text(
                f,
                style = NfType.Fact.copy(fontSize = fontSize.sp),
                color = if (i == 0) FactGreen else NfColors.Muted,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

/** Measured but takes no space while [collapsed] (the hero buttons stay focusable for Up from the rows). */
private fun Modifier.collapsed(collapsed: Boolean): Modifier = layout { measurable, constraints ->
    val p = measurable.measure(constraints)
    layout(p.width, if (collapsed) 0 else p.height) { p.place(0, 0) }
}

/**
 * Hero of the rows screens: backdrop at the top right, title, facts and overview; the billboard buttons (Play /
 * More Info of [featured]) while the hero has focus. In the rows it follows [state] and is a little smaller.
 */
@Composable
internal fun Hero(
    featured: Title?,
    kicker: String?,
    inRows: Boolean,
    state: HeroState,
    playFocus: FocusRequester,
    infoFocus: FocusRequester,
    onOpen: (Title, Boolean) -> Unit,
    onButtonFocus: (info: Boolean) -> Unit,
) {
    val s = strings()
    val featuredItem = remember(featured, kicker, s) { featured?.let { heroOf(it, s, kicker) } }
    val item = if (inRows) state.item ?: featuredItem else featuredItem
    val lift by animateDpAsState(if (inRows) (-8).dp else 0.dp, tween(300), label = "hero-lift")

    Box(Modifier.fillMaxSize()) {
        HeroImage(
            item?.backdrop.orEmpty(),
            Modifier.align(Alignment.TopEnd).fillMaxWidth(0.78f).fillMaxHeight(0.78f),
        )
        Box(
            Modifier
                .fillMaxSize()
                .background(
                    Brush.horizontalGradient(
                        0.22f to NfColors.Background,
                        0.40f to NfColors.Background.copy(alpha = 0.75f),
                        0.68f to Color.Transparent,
                    ),
                ),
        )
        Box(Modifier.fillMaxSize().background(Brush.verticalGradient(0.40f to Color.Transparent, 0.75f to NfColors.Background)))

        Column(
            Modifier
                .padding(start = Dimens.ScreenPadH, top = 56.dp)
                .width(416.dp)
                .offset(y = lift),
        ) {
            if (item != null) {
                if (!item.kicker.isNullOrEmpty() && !inRows) {
                    Text(
                        item.kicker,
                        style = NfType.Caption.copy(fontSize = 11.sp, fontWeight = FontWeight.ExtraBold, letterSpacing = 0.3.em),
                        color = NfColors.Red,
                        modifier = Modifier.padding(bottom = 8.dp),
                    )
                }
                Text(
                    item.title,
                    style = NfType.Display.copy(
                        fontSize = if (inRows) 30.sp else 38.sp,
                        lineHeight = if (inRows) 35.sp else 44.sp,
                    ),
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
                if (item.facts.isNotEmpty()) Facts(item.facts, Modifier.padding(top = 10.dp))
                if (item.overview.isNotEmpty()) {
                    Text(
                        item.overview,
                        style = NfType.Body.copy(fontSize = 12.8.sp, lineHeight = 18.5.sp),
                        color = NfColors.TextSoft,
                        maxLines = 3,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.padding(top = 11.dp),
                    )
                }
            }
            if (featured != null) {
                Row(
                    Modifier
                        .collapsed(inRows)
                        .alpha(if (inRows) 0f else 1f)
                        .padding(top = 18.dp),
                    horizontalArrangement = Arrangement.spacedBy(11.dp),
                ) {
                    NfButton(
                        s.play,
                        onClick = { onOpen(featured, true) },
                        modifier = Modifier
                            .restorableFocus(KEY_HERO_PLAY)
                            .focusRequester(playFocus)
                            .onFocusChanged { if (it.isFocused) onButtonFocus(false) },
                        icon = PlayFilled,
                    )
                    NfButton(
                        s.moreInfo,
                        onClick = { onOpen(featured, false) },
                        modifier = Modifier
                            .restorableFocus(KEY_HERO_INFO)
                            .focusRequester(infoFocus)
                            .onFocusChanged { if (it.isFocused) onButtonFocus(true) },
                        icon = Lucide.Info,
                        style = ButtonStyle.Secondary,
                    )
                }
            }
        }
    }
}

internal const val KEY_HERO_PLAY = "hero-play"
private const val KEY_HERO_INFO = "hero-info"
internal val PlayFilled by lazy { Lucide.Play.filled() }

/** The hero backdrop of a TMDB image path (core FadeInImage: loaded first, then faded in). */
@Composable
internal fun HeroImage(path: String, modifier: Modifier = Modifier, size: String = TmdbImages.W780) {
    FadeInImage(LocalCore.current.imageUrl(path, size), modifier)
}
