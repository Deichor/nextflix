package com.nextflix.tv.feature.browse

import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.BringIntoViewSpec
import androidx.compose.foundation.gestures.LocalBringIntoViewSpec
import androidx.compose.foundation.gestures.animateScrollBy
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.key
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.toPlayRequest
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.ui.components.BackdropCard
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.ContentRow
import com.nextflix.tv.core.ui.components.ContinueCard
import com.nextflix.tv.core.ui.components.ErrorMessage
import com.nextflix.tv.core.ui.components.LocalSkeletonPulse
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.components.RankCard
import com.nextflix.tv.core.ui.components.rememberSkeletonPulse
import com.nextflix.tv.core.ui.focus.FocusRestore
import com.nextflix.tv.core.ui.focus.onPreviewKeyDown
import com.nextflix.tv.core.ui.focus.rescueFocus
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.NfColors
import kotlinx.coroutines.launch

/** One row of a rows screen (webOS components/Row.tsx RowDef). Items null: loading; empty: hidden. */
@Immutable
internal sealed interface RowDef {
    val id: String
    val title: String
    val items: List<*>?

    /** Restore key of the first card (FocusRestore fallback when there is no billboard). */
    val firstCardKey: String?
}

@Immutable
internal data class TitleRow(
    override val id: String,
    override val title: String,
    override val items: List<Title>?,
    val rank: Boolean = false,
) : RowDef {
    override val firstCardKey get() = items?.firstOrNull()?.let { "$id:${it.key}" }
}

@Immutable
internal data class ContinueRow(
    override val id: String,
    override val title: String,
    override val items: List<StreamProgress>?,
) : RowDef {
    override val firstCardKey get() = items?.firstOrNull()?.let { "$id:${it.key}" }
}

private const val FOOTER_ID = "surprise-footer"
private const val KEY_SURPRISE = "home-surprise"

// Layout of the webOS rows screen (1rem = 8 dp): rows start at 30rem, 10rem lower while the hero has focus
private val RowsTop = 240.dp
private val RowsDrop = 80.dp
private val RowsEnd = 320.dp
private val ScrollEasing = CubicBezierEasing(0.25f, 0.1f, 0.25f, 1f)

// The rows column scrolls itself (focused row to the top); the cards' rows keep their own start alignment
@OptIn(ExperimentalFoundationApi::class)
private val NoAutoScroll = object : BringIntoViewSpec {
    override fun calculateScrollDistance(offset: Float, size: Float, containerSize: Float): Float = 0f
}

/** Brings item [index] to the top of the list (smooth when it is laid out, else a jump). */
private suspend fun LazyListState.scrollToTop(index: Int) {
    val info = layoutInfo.visibleItemsInfo.firstOrNull { it.index == index }
    if (info == null) {
        scrollToItem(index)
    } else if (info.offset != 0) {
        animateScrollBy(info.offset.toFloat(), tween(220, easing = ScrollEasing))
    }
}

/**
 * Home, Movies and TV Shows (webOS components/RowsScreen.tsx): a hero with the focused title on top, rows of
 * cards below. While the billboard buttons have focus the rows sit lower; Up from the first row returns to them.
 *
 * @param ready the first data are in: focus can be given (restored)
 * @param surprise "Surprise me" under the last row (Home)
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun RowsScreen(
    featured: Title?,
    rows: List<RowDef>,
    ready: Boolean,
    hero: HeroState,
    nav: Navigator,
    onRetry: () -> Unit,
    kicker: String? = null,
    surprise: Boolean = false,
) {
    val s = strings()
    val bs = browseStrings.current()
    val language = LocalAppLanguage.current
    val shown = remember(rows) { rows.filter { it.items?.isEmpty() != true } }
    var inRows by rememberSaveable { mutableStateOf(false) }
    var focusedRowId by remember { mutableStateOf<String?>(null) }
    var infoLast by remember { mutableStateOf(false) }
    val playFocus = remember { FocusRequester() }
    val infoFocus = remember { FocusRequester() }
    val listState = rememberLazyListState()
    val scope = rememberCoroutineScope()

    val openTitle = remember(nav) { { t: Title, autoplay: Boolean -> nav.push(Route.Detail(t.type, t.id, autoplay)) } }
    val playProgress = remember(nav) { { p: StreamProgress -> nav.push(Route.Player(p.toPlayRequest())) } }

    val firstCard = shown.firstNotNullOfOrNull { it.firstCardKey }
    val loading = shown.any { it.items == null }
    val nothing = ready && featured == null && firstCard == null && !loading

    FocusRestore(
        ready = ready && (featured != null || firstCard != null || !loading),
        fallbackKey = if (featured != null) KEY_HERO_PLAY else firstCard,
    )

    Box(
        Modifier
            .fillMaxSize()
            .background(NfColors.Background)
            .rescueFocus { lost ->
                // The same row first, then the rows below, then above, then the billboard
                val at = rows.indexOfFirst { lost.startsWith("${it.id}:") }.coerceAtLeast(0)
                (rows.drop(at) + rows.take(at).reversed()).map { it.firstCardKey } + KEY_HERO_PLAY
            },
    ) {
        Hero(
            featured = featured,
            kicker = kicker,
            inRows = inRows,
            state = hero,
            playFocus = playFocus,
            infoFocus = infoFocus,
            onOpen = openTitle,
            onButtonFocus = { info ->
                inRows = false
                infoLast = info
            },
        )

        val drop by animateDpAsState(if (inRows || featured == null) 0.dp else RowsDrop, tween(300), label = "rows-drop")
        if (nothing) {
            ErrorMessage(s.errorNetwork, onRetry, Modifier.padding(top = RowsTop))
        } else CompositionLocalProvider(
            LocalBringIntoViewSpec provides NoAutoScroll,
            // One pulse for every skeleton card, running only while a row loads
            LocalSkeletonPulse provides rememberSkeletonPulse(loading),
        ) {
            LazyColumn(
                state = listState,
                modifier = Modifier
                    .fillMaxSize()
                    .padding(top = RowsTop)
                    .offset { IntOffset(0, drop.roundToPx()) }
                    .onFocusChanged { if (it.hasFocus) inRows = true }
                    .onPreviewKeyDown { e ->
                        val first = focusedRowId != null && shown.firstOrNull()?.id == focusedRowId
                        if (e.key == Key.DirectionUp && first && featured != null) {
                            (if (infoLast) infoFocus else playFocus).tryFocus()
                        } else {
                            false
                        }
                    },
            ) {
                itemsIndexed(
                    shown,
                    key = { _, row -> row.id },
                    contentType = { _, row ->
                        when (row) {
                            is ContinueRow -> "continue"
                            is TitleRow -> if (row.rank) "rank" else "titles"
                        }
                    },
                ) { index, row ->
                    val rowModifier = Modifier
                        .padding(bottom = 8.dp)
                        .onFocusChanged {
                            if (it.hasFocus) {
                                focusedRowId = row.id
                                scope.launch { listState.scrollToTop(index) }
                            }
                        }
                    when (row) {
                        is TitleRow -> ContentRow(row.title, row.items, row.id, key = { it.key }, modifier = rowModifier) { i, t, m ->
                            val onFocused = { hero.show(heroOf(t, s)) }
                            if (row.rank) {
                                RankCard(t, t.rank ?: (i + 1), onClick = { openTitle(t, false) }, modifier = m, onFocused = onFocused)
                            } else {
                                BackdropCard(t, onClick = { openTitle(t, false) }, modifier = m, onFocused = onFocused)
                            }
                        }
                        is ContinueRow -> ContentRow(row.title, row.items, row.id, key = { it.key }, modifier = rowModifier) { _, p, m ->
                            ContinueCard(p, onClick = { playProgress(p) }, modifier = m, onFocused = { hero.show(heroOf(p, s, language)) })
                        }
                    }
                }
                if (surprise) {
                    item(key = FOOTER_ID, contentType = "footer") {
                        Box(
                            Modifier
                                .fillMaxWidth()
                                .padding(top = 24.dp, bottom = 16.dp)
                                .onFocusChanged {
                                    if (it.hasFocus) {
                                        focusedRowId = FOOTER_ID
                                        // Keeps the last row visible above the button
                                        scope.launch { listState.scrollToTop(maxOf(0, shown.lastIndex)) }
                                    }
                                },
                            contentAlignment = Alignment.Center,
                        ) {
                            NfButton(
                                text = s.navSurprise,
                                icon = Lucide.Shuffle,
                                style = ButtonStyle.Secondary,
                                shape = RoundedCornerShape(50),
                                padding = 27.dp,
                                onClick = { nav.push(Route.Surprise) },
                                modifier = Modifier
                                    .restorableFocus(KEY_SURPRISE)
                                    .onFocusChanged {
                                        if (it.isFocused) {
                                            hero.show(HeroItem("surprise", s.navSurprise, bs.surpriseHint, "", emptyList()), immediate = true)
                                        }
                                    },
                            )
                        }
                    }
                }
                item(key = "rows-end", contentType = "rows-end") { Spacer(Modifier.height(RowsEnd)) }
            }
        }
    }
}

