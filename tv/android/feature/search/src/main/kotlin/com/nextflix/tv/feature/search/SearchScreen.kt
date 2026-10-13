package com.nextflix.tv.feature.search

import android.os.SystemClock
import android.view.KeyEvent as NativeKeyEvent
import androidx.activity.compose.BackHandler
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.focusGroup
import androidx.compose.foundation.gestures.BringIntoViewSpec
import androidx.compose.foundation.gestures.LocalBringIntoViewSpec
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.itemsIndexed
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.focusRestorer
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.key
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.tv.material3.Border
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.core
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.coreStrings
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.ui.components.CardImage
import com.nextflix.tv.core.ui.components.FocusCard
import com.nextflix.tv.core.ui.components.KeyboardLayout
import com.nextflix.tv.core.ui.components.NfChip
import com.nextflix.tv.core.ui.components.OnScreenKeyboard
import com.nextflix.tv.core.ui.components.Spinner
import com.nextflix.tv.core.ui.focus.FocusRestore
import com.nextflix.tv.core.ui.focus.onKeyDown
import com.nextflix.tv.core.ui.focus.onPreviewKeyDown
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import java.text.Collator

// Layout of webOS .search-left / .search-results (1 rem = 8 dp)
private val LeftWidth = 272.dp
private val ResultsStart = 360.dp
private val ScreenTop = 48.dp
private val CardHeight = 75.dp
private val GridGap = 9.6.dp
private val FilterHeight = 29.dp
private val FilterGap = 4.8.dp
private val FilterShape = RoundedCornerShape(4.dp)
private val FilterRing = Border(BorderStroke(2.dp, NfColors.Focus), inset = (-2).dp, shape = FilterShape)

// Load the next browse page when focus is this close to the end of the grid
private const val MORE_AHEAD = 12
private const val MAX_COMPLETIONS = 5

/**
 * Search tab (webOS screens/SearchScreen.tsx, lib/search.ts, lib/discover.ts). Public entry point of
 * :feature:search; the signature is fixed. Drawn under the app's side menu rail.
 *
 * Left column: the field, the on-screen keyboard (with voice search), title completions and the filters
 * (All / Movies / TV Shows, Anime, genres). Right: a grid of results (ranked search with "did you mean"), ranked
 * browsing by filters without text (next page near the end), or popular titles. The state lives in the
 * ViewModel, so Back from Detail shows the same results with the same card focused.
 */
@OptIn(ExperimentalComposeUiApi::class)
@Composable
fun SearchScreen(nav: Navigator) {
    val core = core()
    val vm: SearchViewModel = viewModel { SearchViewModel(core) }
    val state by vm.state.collectAsStateWithLifecycle()
    val voice = rememberVoiceSearch(onResult = vm::setQuery)
    val firstKey = remember { FocusRequester() }
    val leftGroup = remember { FocusRequester() }
    var resultsFocused by remember { mutableStateOf(false) }

    BackHandler(enabled = voice.listening) { voice.cancel() }

    val pending by core.pendingSearch.pending.collectAsStateWithLifecycle()
    LaunchedEffect(pending) {
        if (pending != null) core.pendingSearch.take()?.let(vm::setQuery)
    }
    LaunchedEffect(state.query) { voice.message = null }

    Box(
        Modifier
            .fillMaxSize()
            .onPreviewKeyDown { e ->
                // The remote's search / mic button, when the system passes it to the app
                val code = e.nativeKeyEvent.keyCode
                if (code == NativeKeyEvent.KEYCODE_SEARCH || code == NativeKeyEvent.KEYCODE_VOICE_ASSIST) {
                    voice.toggle()
                    true
                } else {
                    false
                }
            }
            .onKeyDown { e ->
                typeFromHardware(e, state.query) { q ->
                    // The typed text replaces the results: a focused card would go with them
                    if (resultsFocused && !leftGroup.tryFocus()) firstKey.tryFocus()
                    vm.setQuery(q)
                }
            },
    ) {
        LeftColumn(
            state = state,
            vm = vm,
            voice = voice,
            firstKey = firstKey,
            leftGroup = leftGroup,
            modifier = Modifier
                .padding(start = Dimens.ScreenPadH, top = ScreenTop)
                .width(LeftWidth)
                .fillMaxHeight(),
        )
        Results(
            state = state,
            onOpen = { nav.push(Route.Detail(it.type, it.id)) },
            onLoadMore = vm::loadMore,
            onDidYouMean = {
                vm.setQuery(it)
                // The chip goes away while the corrected search loads
                if (!leftGroup.tryFocus()) firstKey.tryFocus()
            },
            modifier = Modifier
                .padding(start = ResultsStart)
                .onFocusChanged { resultsFocused = it.hasFocus }
                .fillMaxSize(),
        )
    }

    FocusRestore(ready = true, fallback = firstKey)
}

/** Hardware keyboard or the remote's number keys while the focus is outside the on-screen keyboard. */
private fun typeFromHardware(e: androidx.compose.ui.input.key.KeyEvent, query: String, set: (String) -> Unit): Boolean {
    if (e.nativeKeyEvent.isCtrlPressed || e.nativeKeyEvent.isAltPressed || e.nativeKeyEvent.isMetaPressed) return false
    if (e.key == Key.Backspace || e.key == Key.Delete) {
        if (query.isEmpty()) return false
        set(query.dropLast(1))
        return true
    }
    val ch = e.nativeKeyEvent.unicodeChar.takeIf { it > 31 }?.toChar() ?: return false
    if (query.length < MAX_QUERY) set(query + ch.lowercaseChar())
    return true
}

@OptIn(ExperimentalComposeUiApi::class, ExperimentalFoundationApi::class)
@Composable
private fun LeftColumn(
    state: SearchUiState,
    vm: SearchViewModel,
    voice: VoiceSearch,
    firstKey: FocusRequester,
    leftGroup: FocusRequester,
    modifier: Modifier = Modifier,
) {
    val s = searchStrings.current()
    val cs = strings()
    val language = LocalAppLanguage.current
    val scroll = rememberScrollState()
    val lastMove = remember { longArrayOf(0L) }

    // Completion clicked: it leaves the list (the new text is the query), so focus stays at its place
    var refocusCompletion by remember { mutableStateOf<Int?>(null) }
    val completionFocus = remember { List(MAX_COMPLETIONS) { FocusRequester() } }
    val typeAllFocus = remember { FocusRequester() }
    LaunchedEffect(state.completions) {
        val index = refocusCompletion ?: return@LaunchedEffect
        refocusCompletion = null
        withFrameNanos { }
        val n = state.completions.size.coerceAtMost(MAX_COMPLETIONS)
        if (n > 0) completionFocus[index.coerceAtMost(n - 1)].tryFocus() else typeAllFocus.tryFocus()
    }

    val chips = remember(language, state.filters.type) {
        val collator = Collator.getInstance(language.locale)
        GenreChips.filter { it.fits(state.filters.type) }
            .map { it.id to coreStrings[language].genre(it.id) }
            .sortedWith { a, b -> collator.compare(a.second, b.second) }
    }

    Column(modifier) {
        SearchField(state.query, voice, s)
        Spacer(Modifier.height(16.dp))
        Box(Modifier.weight(1f).fillMaxWidth()) {
            CompositionLocalProvider(LocalBringIntoViewSpec provides rememberPaddedBringIntoView(36.dp)) {
                Column(
                    Modifier
                        .fillMaxSize()
                        .focusRequester(leftGroup)
                        .focusRestorer(firstKey)
                        .focusGroup()
                        .onPreviewKeyDown { e ->
                            // Held Up / Down: at most one move per 70 ms, so the column keeps up
                            if (e.key != Key.DirectionUp && e.key != Key.DirectionDown) return@onPreviewKeyDown false
                            val now = SystemClock.uptimeMillis()
                            if (e.nativeKeyEvent.repeatCount > 0 && now - lastMove[0] < 70) return@onPreviewKeyDown true
                            lastMove[0] = now
                            false
                        }
                        .verticalScroll(scroll)
                        .padding(top = 5.dp, bottom = 16.dp),
                ) {
                    voice.message?.let {
                        Text(it, style = NfType.BodySmall, color = NfColors.Muted, modifier = Modifier.padding(bottom = 8.dp))
                    }
                    OnScreenKeyboard(
                        layout = KeyboardLayout.Search,
                        value = state.query,
                        onValueChange = vm::setQuery,
                        width = LeftWidth,
                        max = MAX_QUERY,
                        onVoice = voice::toggle,
                        firstKey = firstKey,
                        keyHeight = 27.dp,
                    )
                    if (state.completions.isNotEmpty()) {
                        Column(Modifier.padding(top = 8.dp)) {
                            state.completions.take(MAX_COMPLETIONS).forEachIndexed { i, text ->
                                Completion(
                                    text = text,
                                    onClick = {
                                        refocusCompletion = i
                                        vm.setQuery(text)
                                    },
                                    modifier = Modifier.focusRequester(completionFocus[i]),
                                )
                            }
                        }
                    }
                    Box(
                        Modifier
                            .padding(top = 12.8.dp)
                            .fillMaxWidth()
                            .height(1.dp)
                            .background(Color.White.copy(alpha = 0.15f)),
                    )
                    Row(Modifier.padding(top = 12.8.dp).fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(FilterGap)) {
                        TypeFilter.entries.forEach { type ->
                            val label = when (type) {
                                TypeFilter.All -> cs.all
                                TypeFilter.Movie -> cs.movies
                                TypeFilter.Tv -> cs.tvShows
                            }
                            FilterButton(
                                text = label,
                                active = state.filters.type == type,
                                onClick = { vm.setType(type) },
                                modifier = Modifier
                                    .weight(1f)
                                    .then(if (type == TypeFilter.All) Modifier.focusRequester(typeAllFocus) else Modifier),
                            )
                        }
                    }
                    val genreButtons = listOf(0 to s.anime) + chips
                    Column(Modifier.padding(top = 9.6.dp), verticalArrangement = Arrangement.spacedBy(FilterGap)) {
                        genreButtons.chunked(3).forEach { row ->
                            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(FilterGap)) {
                                row.forEach { (id, label) ->
                                    val on = if (id == 0) state.filters.anime else id in state.filters.genres
                                    FilterButton(
                                        text = label,
                                        active = on,
                                        check = true,
                                        onClick = { if (id == 0) vm.toggleAnime() else vm.toggleGenre(id) },
                                        modifier = Modifier.weight(1f),
                                    )
                                }
                                repeat(3 - row.size) { Spacer(Modifier.weight(1f)) }
                            }
                        }
                    }
                }
            }
            // Fade under the column while part of it is below the visible area
            if (scroll.canScrollForward) {
                Box(
                    Modifier
                        .align(Alignment.BottomCenter)
                        .fillMaxWidth()
                        .height(40.dp)
                        .background(Brush.verticalGradient(listOf(NfColors.Background.copy(alpha = 0f), NfColors.Background))),
                )
            }
        }
    }
}

@Composable
private fun SearchField(query: String, voice: VoiceSearch, s: SearchStrings) {
    val caret by rememberInfiniteTransition(label = "caret").animateFloat(
        initialValue = 1f,
        targetValue = 0f,
        animationSpec = infiniteRepeatable(tween(500), RepeatMode.Reverse),
        label = "blink",
    )
    val textStyle = NfType.Body.copy(fontSize = 16.sp, lineHeight = 20.sp)
    Row(
        Modifier
            .fillMaxWidth()
            .height(40.dp)
            .drawBehind {
                val h = 1.6.dp.toPx()
                drawRect(Color.White.copy(alpha = 0.4f), topLeft = Offset(0f, size.height - h), size = androidx.compose.ui.geometry.Size(size.width, h))
            },
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (voice.listening) {
            Icon(Lucide.Mic, contentDescription = null, tint = NfColors.Red, modifier = Modifier.size(17.6.dp).alpha(0.5f + caret * 0.5f))
            Spacer(Modifier.width(8.dp))
            if (voice.partial.isEmpty()) {
                Text(s.listening, style = textStyle, color = NfColors.Dim, maxLines = 1)
            } else {
                Text(voice.partial, style = textStyle, maxLines = 1, overflow = TextOverflow.StartEllipsis)
            }
        } else {
            Icon(Lucide.Search, contentDescription = null, tint = NfColors.Muted, modifier = Modifier.size(17.6.dp))
            Spacer(Modifier.width(8.dp))
            if (query.isEmpty()) {
                Text(s.placeholder, style = textStyle, color = NfColors.Dim, maxLines = 1, modifier = Modifier.weight(1f, fill = false))
            } else {
                Text(query, style = textStyle, maxLines = 1, overflow = TextOverflow.StartEllipsis, modifier = Modifier.weight(1f, fill = false))
            }
            Box(Modifier.padding(start = 1.dp).width(1.2.dp).height(20.dp).alpha(caret).background(Color.White))
        }
    }
}

@Composable
private fun Completion(text: String, onClick: () -> Unit, modifier: Modifier = Modifier) {
    Surface(
        onClick = onClick,
        modifier = modifier.fillMaxWidth().height(FilterHeight),
        shape = ClickableSurfaceDefaults.shape(FilterShape),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = Color.Transparent,
            contentColor = NfColors.Muted,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
            pressedContainerColor = Color.White,
            pressedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1f),
    ) {
        Row(Modifier.fillMaxSize().padding(horizontal = 9.6.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(Lucide.Search, contentDescription = null, modifier = Modifier.size(12.8.dp))
            Spacer(Modifier.width(8.dp))
            Text(text, style = NfType.Body.copy(fontSize = 12.sp), maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

/** Type and genre buttons: chosen ones are white-filled (genres with a check), focused ones scale with a ring. */
@Composable
private fun FilterButton(
    text: String,
    active: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    check: Boolean = false,
) {
    Surface(
        onClick = onClick,
        modifier = modifier.height(FilterHeight),
        shape = ClickableSurfaceDefaults.shape(FilterShape),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = if (active) Color(0xFFE8E8E8) else Color.White.copy(alpha = 0.1f),
            contentColor = if (active) Color.Black else NfColors.Muted,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
            pressedContainerColor = Color.White,
            pressedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.06f),
        border = ClickableSurfaceDefaults.border(focusedBorder = FilterRing),
    ) {
        Row(
            Modifier.fillMaxSize().padding(horizontal = 4.8.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.Center,
        ) {
            if (check && active) {
                Icon(Lucide.Check, contentDescription = null, modifier = Modifier.size(10.4.dp))
                Spacer(Modifier.width(3.2.dp))
            }
            Text(text, style = NfType.Caption.copy(fontSize = 10.8.sp), maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

@OptIn(ExperimentalComposeUiApi::class, ExperimentalFoundationApi::class)
@Composable
private fun Results(
    state: SearchUiState,
    onOpen: (Title) -> Unit,
    onLoadMore: () -> Unit,
    onDidYouMean: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    val s = searchStrings.current()
    val core = core()
    val grid = rememberLazyGridState()
    var gridFocused by remember { mutableStateOf(false) }
    val q = state.q
    val filtered = state.filters.active
    val shown = state.shown
    val heading = when {
        q.isNotEmpty() -> s.results
        filtered -> s.browse
        else -> s.popular
    }

    // New results start at the top unless the focus is in them (loading the next page)
    val shownFor = if (q.isNotEmpty() || filtered) "r:${state.resultsFor}" else "popular"
    var scrolledFor by rememberSaveable { mutableStateOf(shownFor) }
    LaunchedEffect(shownFor) {
        if (shownFor == scrolledFor) return@LaunchedEffect
        scrolledFor = shownFor
        if (!gridFocused) grid.scrollToItem(0)
    }

    CompositionLocalProvider(LocalBringIntoViewSpec provides rememberPaddedBringIntoView(24.dp)) {
        LazyVerticalGrid(
            columns = GridCells.Fixed(4),
            state = grid,
            modifier = modifier
                .onFocusChanged { gridFocused = it.hasFocus }
                .focusRestorer(),
            contentPadding = PaddingValues(start = 8.dp, end = 24.dp, top = ScreenTop, bottom = 120.dp),
            horizontalArrangement = Arrangement.spacedBy(GridGap),
            verticalArrangement = Arrangement.spacedBy(11.2.dp),
        ) {
            if (q.isNotEmpty() && state.didYouMean.isNotEmpty() && !state.loading) {
                item(key = "did-you-mean", span = { GridItemSpan(maxLineSpan) }) {
                    Box(Modifier.padding(bottom = 4.dp), contentAlignment = Alignment.CenterStart) {
                        NfChip(text = s.didYouMean(state.didYouMean), selected = false, onClick = { onDidYouMean(state.didYouMean) })
                    }
                }
            }
            item(key = "heading", span = { GridItemSpan(maxLineSpan) }) {
                Text(heading, style = NfType.RowTitle, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (state.loading || shown == null) {
                item(key = "spinner", span = { GridItemSpan(maxLineSpan) }) {
                    Box(Modifier.fillMaxWidth().padding(top = 48.dp), contentAlignment = Alignment.TopCenter) { Spinner() }
                }
            }
            if (shown != null && shown.isEmpty() && !state.loading && (q.isNotEmpty() || filtered)) {
                item(key = "empty", span = { GridItemSpan(maxLineSpan) }) {
                    Column(Modifier.padding(top = 32.dp).widthIn(max = 560.dp)) {
                        Text(
                            if (q.isNotEmpty()) s.noMatches(q) else s.noFilterMatches,
                            style = NfType.Body.copy(fontSize = 17.6.sp, lineHeight = 24.sp),
                            color = Color.White,
                        )
                        Spacer(Modifier.height(8.dp))
                        Text(
                            if (filtered) s.noFilterMatchesHint else s.noMatchesHint,
                            style = NfType.Body.copy(fontSize = 13.6.sp, lineHeight = 20.sp),
                            color = NfColors.Muted,
                        )
                    }
                }
            }
            if (shown != null) {
                itemsIndexed(shown, key = { _, t -> t.key }) { i, title ->
                    GridCard(
                        title = title,
                        imageUrl = core.imageUrl(title.backdrop.ifEmpty { title.poster }, TmdbImages.W300),
                        onClick = { onOpen(title) },
                        onFocused = { if (i >= shown.size - MORE_AHEAD) onLoadMore() },
                    )
                }
            }
        }
    }
}

@Composable
private fun GridCard(title: Title, imageUrl: String, onClick: () -> Unit, onFocused: () -> Unit) {
    FocusCard(
        onClick = onClick,
        modifier = Modifier.restorableFocus("search:${title.key}").fillMaxWidth(),
        height = CardHeight,
        onFocused = onFocused,
    ) {
        Box(Modifier.fillMaxSize()) {
            CardImage(imageUrl, Modifier.fillMaxSize())
            Text(
                title.title,
                style = NfType.Caption,
                color = Color.White,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier
                    .align(Alignment.BottomStart)
                    .fillMaxWidth()
                    .background(Brush.verticalGradient(listOf(Color.Transparent, Color(0xD9000000))))
                    .padding(start = 9.6.dp, end = 9.6.dp, top = 24.dp, bottom = 8.dp),
            )
        }
    }
}

/** Scrolls a focused child just enough to keep it [padding] inside the visible area (webOS data-scroll-pad). */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun rememberPaddedBringIntoView(padding: Dp): BringIntoViewSpec {
    val px = with(LocalDensity.current) { padding.toPx() }
    return remember(px) {
        object : BringIntoViewSpec {
            override fun calculateScrollDistance(offset: Float, size: Float, containerSize: Float): Float {
                val leading = offset - px
                val trailing = offset + size + px - containerSize
                return when {
                    leading < 0f -> leading
                    trailing > 0f -> minOf(trailing, leading)
                    else -> 0f
                }
            }
        }
    }
}
