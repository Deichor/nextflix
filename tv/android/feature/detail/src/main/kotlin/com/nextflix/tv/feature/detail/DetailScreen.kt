package com.nextflix.tv.feature.detail

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.LocalBringIntoViewSpec
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.focusRestorer
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.tv.material3.Icon
import androidx.tv.material3.LocalContentColor
import androidx.tv.material3.Text
import coil3.compose.AsyncImage
import coil3.request.ImageRequest
import com.nextflix.tv.core.core
import com.nextflix.tv.core.i18n.Formats
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.AvailabilityStatus
import com.nextflix.tv.core.model.Details
import com.nextflix.tv.core.model.EpisodeInfo
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.RatingValue
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.find
import com.nextflix.tv.core.model.matchPercent
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.ui.components.BackdropCard
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.ContentRow
import com.nextflix.tv.core.ui.components.LoadingBox
import com.nextflix.tv.core.ui.components.LocalSkeletonPulse
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.components.NfChip
import com.nextflix.tv.core.ui.components.ProgressBar
import com.nextflix.tv.core.ui.components.RoundIconButton
import com.nextflix.tv.core.ui.components.rememberSkeletonPulse
import com.nextflix.tv.core.ui.components.rememberStartAlignedBringIntoView
import com.nextflix.tv.core.ui.focus.FocusRestore
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.icons.DoubleThumbsUp
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.icons.filled
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

private const val KEY_PLAY = "detail-play"
private const val KEY_RETRY = "detail-retry"

private val MatchGreen = Color(0xFF46D369)
private val TopTextWidth = 560.dp
private val PlayIcon by lazy { Lucide.Play.filled() }
private val HeartIcon by lazy { Lucide.Heart.filled() }

/**
 * Detail page (webOS screens/DetailScreen.tsx). Public entry point of :feature:detail; the signature is fixed.
 *
 * Title info over the backdrop, Play / Resume (the play plan, see [planOf]), thumbs and Favorites, seasons and
 * episodes with progress, More Like This. What Play starts is warmed up while the page shows and released after
 * a grace when it is left. [Route.Detail.autoplay] starts playback once the plan is known.
 */
@Composable
fun DetailScreen(route: Route.Detail, nav: Navigator) {
    val core = core()
    val vm: DetailViewModel = viewModel { DetailViewModel(core, route.type, route.tmdbId) }
    val load by vm.load.collectAsStateWithLifecycle()
    val playFocus = remember { FocusRequester() }

    DisposableEffect(vm) { onDispose { vm.leave() } }
    LaunchedEffect(vm) { vm.refreshProgressIfStale() }

    Box(Modifier.fillMaxSize().background(NfColors.Background)) {
        when (val l = load) {
            DetailLoad.Loading -> LoadingBox()
            DetailLoad.Failed -> DetailError(onRetry = vm::retry, onBack = { nav.back() })
            is DetailLoad.Ready -> DetailContent(l.details, vm, nav, route.autoplay, playFocus)
        }
    }

    FocusRestore(ready = load != DetailLoad.Loading, fallbackKey = if (load is DetailLoad.Ready) KEY_PLAY else KEY_RETRY)

    // A retry that succeeds lands on Play
    var hadFailed by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(load) {
        if (load == DetailLoad.Failed) {
            hadFailed = true
        } else if (load is DetailLoad.Ready && hadFailed) {
            hadFailed = false
            repeat(5) {
                withFrameNanos { }
                if (playFocus.tryFocus()) return@LaunchedEffect
            }
        }
    }
}

@Composable
private fun DetailError(onRetry: () -> Unit, onBack: () -> Unit) {
    val s = detailStrings.current()
    val cs = strings()
    Column(
        Modifier.fillMaxSize(),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        Text(s.notFound, style = NfType.Body.copy(fontSize = 17.6.sp, lineHeight = 24.sp), color = Color.White)
        Spacer(Modifier.height(8.dp))
        Text(s.loadFailed, style = NfType.Body.copy(fontSize = 13.6.sp), color = NfColors.Muted)
        Spacer(Modifier.height(16.dp))
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            NfButton(cs.retry, onRetry, Modifier.restorableFocus(KEY_RETRY))
            NfButton(cs.back, onBack, Modifier.restorableFocus("detail-back"), style = ButtonStyle.Secondary)
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun DetailContent(d: Details, vm: DetailViewModel, nav: Navigator, autoplay: Boolean, playFocus: FocusRequester) {
    val core = core()
    val language = LocalAppLanguage.current
    val cs = strings()

    val entries by vm.progress.collectAsStateWithLifecycle()
    val plan by vm.plan.collectAsStateWithLifecycle()
    val availability by vm.availability.collectAsStateWithLifecycle()
    val related by vm.related.collectAsStateWithLifecycle()
    val episodes by vm.episodes.collectAsStateWithLifecycle()
    val pickedSeason by vm.season.collectAsStateWithLifecycle()

    val unavailable = unavailableText(availability, cs, language)
    val activeSeason = d.seasons.firstOrNull { it.seasonNumber == (pickedSeason.takeIf { it > 0 } ?: plan?.ep?.season) }
        ?: d.seasons.firstOrNull()

    val open: (PlayRequest) -> Unit = { request ->
        vm.onOpenPlayer()
        nav.push(Route.Player(request))
    }
    val playMain = rememberUpdatedState {
        val p = plan
        if (p != null) open(d.mainRequest(p, entries.orEmpty(), p.ep?.let { episodes[it.season]?.items }))
    }

    LaunchedEffect(plan) { plan?.let { vm.warm(d, it) } }

    // Billboard / Surprise "Play" opens the page with autoplay: once, not again after Back from the player
    LaunchedEffect(autoplay, plan, availability, unavailable) {
        if (!autoplay || vm.autoplayed || plan == null || unavailable != null) return@LaunchedEffect
        // Wait a moment for the availability answer, so an unreleased title shows its date instead
        if (availability == null) delay(800)
        vm.autoplayed = true
        playMain.value()
    }

    LaunchedEffect(activeSeason) { activeSeason?.let(vm::ensureEpisodes) }

    DetailBackground(core.imageUrl(d.backdrop, TmdbImages.W1280))

    val scroll = rememberScrollState()
    val scroller = rememberSectionScroller(scroll, 32.dp)
    CompositionLocalProvider(LocalBringIntoViewSpec provides NoBringIntoView) {
        Column(
            Modifier
                .fillMaxSize()
                .onSizeChanged { scroller.viewport = it.height }
                .verticalScroll(scroll),
        ) {
            TopSection(
                d = d,
                vm = vm,
                unavailable = unavailable,
                inTheaters = d.type == MediaType.Movie && availability?.status == AvailabilityStatus.InTheaters,
                onPlay = { playMain.value() },
                onCinema = { open(d.cinemaRequest()) },
                playFocus = playFocus,
                modifier = Modifier.section(scroller),
            )
            if (d.type == MediaType.Tv && d.seasons.isNotEmpty() && activeSeason != null) {
                EpisodesSection(
                    d = d,
                    activeSeason = activeSeason.seasonNumber,
                    episodes = episodes[activeSeason.seasonNumber]?.items,
                    entries = entries.orEmpty(),
                    onSeason = vm::selectSeason,
                    onEpisode = { open(d.episodeRequest(it, entries.orEmpty())) },
                    modifier = Modifier.section(scroller),
                )
            }
            CompositionLocalProvider(LocalSkeletonPulse provides rememberSkeletonPulse(related == null)) {
                ContentRow(
                    title = detailStrings.current().moreLikeThis,
                    items = related,
                    rowKey = "related",
                    key = { it.key },
                    modifier = Modifier.section(scroller),
                ) { _, item, itemModifier ->
                    BackdropCard(item, onClick = { nav.push(Route.Detail(item.type, item.id)) }, modifier = itemModifier)
                }
            }
            Spacer(Modifier.height(320.dp))
        }
    }
}

@Composable
private fun TopSection(
    d: Details,
    vm: DetailViewModel,
    unavailable: String?,
    inTheaters: Boolean,
    onPlay: () -> Unit,
    onCinema: () -> Unit,
    playFocus: FocusRequester,
    modifier: Modifier = Modifier,
) {
    val language = LocalAppLanguage.current
    val s = detailStrings.current()
    val cs = strings()
    val logo by vm.logo.collectAsStateWithLifecycle()
    val entries by vm.progress.collectAsStateWithLifecycle()
    val plan by vm.plan.collectAsStateWithLifecycle()
    val rating by vm.rating.collectAsStateWithLifecycle()

    var caption by remember { mutableStateOf("") }
    // "Watch the cinema recording anyway": the first press warns, the second plays
    var cinemaAsk by remember { mutableStateOf(false) }

    Column(modifier.padding(start = Dimens.ScreenPadH, top = 64.dp, bottom = 8.dp).heightIn(min = 464.dp)) {
        TitleOrLogo(d, logo)

        val facts = buildList {
            d.year?.let { add(it.toString()) }
            if (d.type == MediaType.Movie && d.runtime > 0) add(Formats.duration(language, d.runtime))
            if (d.type == MediaType.Tv && d.numberOfSeasons > 0) add(cs.seasons(d.numberOfSeasons))
        }
        val match = matchPercent(d.vote)
        Row(Modifier.widthIn(max = TopTextWidth), horizontalArrangement = Arrangement.spacedBy(13.dp)) {
            val factStyle = NfType.Fact.copy(fontSize = 13.6.sp)
            if (match > 0) Text(cs.match(match), style = factStyle, color = MatchGreen)
            facts.forEach { Text(it, style = factStyle, color = NfColors.Muted) }
        }
        if (d.tagline.isNotEmpty()) {
            Text(
                d.tagline,
                style = NfType.Body.copy(fontSize = 13.6.sp, fontStyle = FontStyle.Italic),
                color = NfColors.TextSoft,
                modifier = Modifier.padding(top = 11.dp).widthIn(max = TopTextWidth),
            )
        }
        Text(
            d.overview.ifEmpty { s.noDescription },
            style = NfType.Body.copy(fontSize = 13.6.sp, lineHeight = 20.sp),
            color = NfColors.TextSoft,
            maxLines = 4,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(top = 11.dp).widthIn(max = TopTextWidth),
        )
        Column(Modifier.padding(top = 10.dp).widthIn(max = TopTextWidth)) {
            Credit(s.cast, d.cast)
            Credit(s.genres, d.genres)
            Credit(s.director, d.directors)
            Credit(s.createdBy, d.creators)
        }

        val resume = plan?.resume
        val resumeLabel = if (resume != null && d.type == MediaType.Tv) Formats.episode(language, resume.season, resume.episode) else ""
        val nextLabel = plan?.ep
            ?.takeIf { resume == null && d.type == MediaType.Tv && !entries.isNullOrEmpty() }
            ?.let { Formats.episode(language, it.season, it.episode) }
            .orEmpty()
        if (resume != null) {
            Row(Modifier.padding(top = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                ProgressBar(resume.fraction, Modifier.width(192.dp), height = 3.dp)
                Spacer(Modifier.width(11.dp))
                Text(
                    listOf(resumeLabel, cs.timeLeft(Formats.duration(language, minutesLeft(resume)))).filter { it.isNotEmpty() }.joinToString(" · "),
                    style = NfType.Body.copy(fontSize = 12.sp),
                    color = NfColors.Muted,
                )
            }
        }

        Row(Modifier.padding(top = 21.dp), verticalAlignment = Alignment.CenterVertically) {
            // One slot for Play and the date button, so focus stays when availability arrives
            ActionButton(
                text = unavailable ?: if (resume != null) cs.resume else cs.play,
                icon = if (unavailable != null) Lucide.CalendarClock else PlayIcon,
                kind = if (unavailable != null) ActionKind.Disabled else ActionKind.Primary,
                onClick = { if (unavailable == null) onPlay() },
                detail = if (unavailable != null) "" else resumeLabel.ifEmpty { nextLabel },
                modifier = Modifier
                    .restorableFocus(KEY_PLAY)
                    .focusRequester(playFocus)
                    .onFocusChanged { if (it.isFocused) caption = "" },
            )
            if (inTheaters) {
                Spacer(Modifier.width(10.dp))
                ActionButton(
                    text = if (cinemaAsk) s.cinemaPlay else s.cinemaWatchAnyway,
                    icon = Lucide.Clapperboard,
                    kind = ActionKind.Secondary,
                    onClick = {
                        if (!cinemaAsk) {
                            cinemaAsk = true
                            caption = s.cinemaConfirm
                        } else {
                            cinemaAsk = false
                            onCinema()
                        }
                    },
                    modifier = Modifier
                        .restorableFocus("detail-cinema")
                        .onFocusChanged {
                            if (it.isFocused) {
                                caption = if (cinemaAsk) s.cinemaConfirm else ""
                            } else {
                                cinemaAsk = false
                            }
                        },
                )
            }
            Spacer(Modifier.width(10.dp))
            RatingButton("favorite", if (rating.favorite) s.unfavorite else s.favorite, rating.favorite, { caption = it }, vm::toggleFavorite) {
                Icon(if (rating.favorite) HeartIcon else Lucide.Heart, contentDescription = null, modifier = Modifier.size(17.dp))
            }
            RatingButton("dislike", s.dislike, rating.rating == RatingValue.Dislike, { caption = it }, { vm.rate(RatingValue.Dislike) }) {
                Icon(Lucide.ThumbsDown, contentDescription = null, modifier = Modifier.size(17.dp))
            }
            RatingButton("like", s.like, rating.rating == RatingValue.Like, { caption = it }, { vm.rate(RatingValue.Like) }) {
                Icon(Lucide.ThumbsUp, contentDescription = null, modifier = Modifier.size(17.dp))
            }
            RatingButton("love", s.love, rating.rating == RatingValue.Love, { caption = it }, { vm.rate(RatingValue.Love) }) {
                DoubleThumbsUp(LocalContentColor.current, Modifier.size(17.dp))
            }
        }
        Text(
            caption,
            style = NfType.Body.copy(fontSize = 12.sp, lineHeight = 16.sp),
            color = NfColors.Muted,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(top = 11.dp).height(19.dp),
        )
    }
}

@Composable
private fun RatingButton(
    key: String,
    label: String,
    active: Boolean,
    onCaption: (String) -> Unit,
    onClick: () -> Unit,
    icon: @Composable () -> Unit,
) {
    RoundIconButton(
        onClick = onClick,
        contentDescription = label,
        active = active,
        modifier = Modifier
            .padding(end = 8.dp)
            .restorableFocus("detail-$key")
            .onFocusChanged { if (it.isFocused) onCaption(label) },
        content = icon,
    )
}

@Composable
private fun TitleOrLogo(d: Details, logo: String) {
    val core = core()
    val context = LocalContext.current
    var failed by remember(logo) { mutableStateOf(false) }
    if (logo.isNotEmpty() && !failed) {
        val request = remember(logo) { ImageRequest.Builder(context).data(core.imageUrl(logo, TmdbImages.W500)).build() }
        AsyncImage(
            model = request,
            contentDescription = d.title,
            contentScale = ContentScale.Fit,
            alignment = Alignment.BottomStart,
            onError = { failed = true },
            modifier = Modifier.padding(bottom = 16.dp).size(352.dp, 112.dp),
        )
    } else {
        Text(
            d.title,
            style = NfType.Display.copy(fontSize = 40.sp, lineHeight = 42.sp),
            maxLines = 3,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(bottom = 13.dp).widthIn(max = TopTextWidth),
        )
    }
}

@Composable
private fun Credit(label: String, names: List<String>) {
    if (names.isEmpty()) return
    Text(
        buildAnnotatedString {
            withStyle(SpanStyle(color = NfColors.Muted, fontWeight = FontWeight.SemiBold)) { append("$label: ") }
            append(names.joinToString(", "))
        },
        style = NfType.BodySmall.copy(fontSize = 11.2.sp, lineHeight = 18.sp),
        color = NfColors.Dim,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
    )
}

@OptIn(ExperimentalFoundationApi::class, ExperimentalComposeUiApi::class)
@Composable
private fun EpisodesSection(
    d: Details,
    activeSeason: Int,
    episodes: List<EpisodeInfo>?,
    entries: List<StreamProgress>,
    onSeason: (Int) -> Unit,
    onEpisode: (EpisodeInfo) -> Unit,
    modifier: Modifier = Modifier,
) {
    val s = detailStrings.current()
    val scope = rememberCoroutineScope()
    var seasonTimer by remember { mutableStateOf<Job?>(null) }
    val activeChip = remember { FocusRequester() }
    val chips = rememberLazyListState()
    // The active chip is the row's entry point: keep it composed
    val activeIndex = d.seasons.indexOfFirst { it.seasonNumber == activeSeason }.coerceAtLeast(0)
    LaunchedEffect(activeIndex) {
        val visible = snapshotFlow { chips.layoutInfo.visibleItemsInfo }.first { it.isNotEmpty() }
        if (visible.none { it.index == activeIndex }) chips.scrollToItem(activeIndex)
    }

    Column(modifier.padding(top = 16.dp)) {
        Text(s.episodes, style = NfType.RowTitle, modifier = Modifier.padding(start = Dimens.ScreenPadH, bottom = Dimens.RowTitleGap))
        CompositionLocalProvider(LocalBringIntoViewSpec provides rememberEdgeBringIntoView(16.dp)) {
            LazyRow(
                state = chips,
                modifier = Modifier.focusRestorer(activeChip),
                contentPadding = PaddingValues(start = Dimens.ScreenPadH, end = 160.dp, top = 6.dp, bottom = 3.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                items(d.seasons, key = { it.seasonNumber }) { season ->
                    val active = season.seasonNumber == activeSeason
                    NfChip(
                        text = s.season(season.seasonNumber),
                        selected = active,
                        onClick = {
                            seasonTimer?.cancel()
                            onSeason(season.seasonNumber)
                        },
                        modifier = Modifier
                            .restorableFocus("season-${season.seasonNumber}")
                            .then(if (active) Modifier.focusRequester(activeChip) else Modifier)
                            .onFocusChanged {
                                if (it.isFocused) {
                                    seasonTimer?.cancel()
                                    seasonTimer = scope.launch {
                                        delay(300)
                                        onSeason(season.seasonNumber)
                                    }
                                }
                            },
                    )
                }
            }
        }
        key(activeSeason) {
            val state = rememberLazyListState()
            CompositionLocalProvider(
                LocalBringIntoViewSpec provides rememberStartAlignedBringIntoView(Dimens.ScreenPadH),
                LocalSkeletonPulse provides rememberSkeletonPulse(episodes == null),
            ) {
                LazyRow(
                    state = state,
                    modifier = Modifier.focusRestorer(),
                    contentPadding = PaddingValues(start = Dimens.ScreenPadH, end = 480.dp, top = 10.dp, bottom = 12.dp),
                    horizontalArrangement = Arrangement.spacedBy(13.dp),
                    verticalAlignment = Alignment.Top,
                ) {
                    if (episodes == null) {
                        items(4) { EpisodeSkeleton() }
                    } else {
                        items(episodes, key = { it.episodeNumber }) { ep ->
                            EpisodeCard(
                                episode = ep,
                                fallbackImage = d.backdrop,
                                progress = entries.find(ep.seasonNumber, ep.episodeNumber),
                                onClick = { onEpisode(ep) },
                                modifier = Modifier.restorableFocus("ep-${ep.seasonNumber}-${ep.episodeNumber}"),
                            )
                        }
                    }
                }
            }
        }
    }
}
