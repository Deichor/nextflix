package com.nextflix.tv.feature.browse

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.LocalCore
import com.nextflix.tv.core.model.HomeFeeds
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.RecommendationRow
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.Top10Region
import com.nextflix.tv.core.nav.Navigator
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlin.time.Duration.Companion.minutes
import kotlin.time.Duration.Companion.seconds

/** Genre rows of Home; keep in sync with GENRE_ROWS in web/src/features/home/HomeScreen.tsx (the server warms their images). */
internal class GenreRow(val title: (BrowseStrings) -> String, val params: Map<String, String>)

internal val GENRE_ROWS = listOf(
    GenreRow({ it.genreActionAdventure }, mapOf("with_genres" to "28|12", "vote_count.gte" to "300")),
    GenreRow({ it.genreComedies }, mapOf("with_genres" to "35", "vote_count.gte" to "300")),
    GenreRow({ it.genreSciFiFantasy }, mapOf("with_genres" to "878|14", "vote_count.gte" to "300")),
    GenreRow({ it.genreHorror }, mapOf("with_genres" to "27", "vote_count.gte" to "200")),
    GenreRow({ it.genreAcclaimedMovies }, mapOf("sort_by" to "vote_average.desc", "vote_count.gte" to "5000")),
)

/**
 * Home's loads (webOS screens/HomeScreen.tsx). Continue Watching, recommendations and favorites are cached
 * briefly under keys that carry the progress / ratings versions, so a change made on another screen (player,
 * detail page) loads them again when Home shows up.
 */
internal class HomeViewModel(private val core: Core) : LoadingViewModel() {
    val hero = HeroState(viewModelScope)

    private val _feeds = query<HomeFeeds>()
    private val _cw = query<List<StreamProgress>>()
    private val _recs = query<List<RecommendationRow>>()
    private val _favs = query<List<Title>>()
    private val _trMovies = query<List<Title>>()
    private val _trTv = query<List<Title>>()
    private val _popular = query<List<Title>>()
    private val _genres = query<List<List<Title>>>()
    private val _featured = MutableStateFlow<Title?>(null)

    val feeds: StateFlow<Query<HomeFeeds>> = _feeds
    val cw: StateFlow<Query<List<StreamProgress>>> = _cw
    val recs: StateFlow<Query<List<RecommendationRow>>> = _recs
    val favs: StateFlow<Query<List<Title>>> = _favs
    val trMovies: StateFlow<Query<List<Title>>> = _trMovies
    val trTv: StateFlow<Query<List<Title>>> = _trTv
    val popular: StateFlow<Query<List<Title>>> = _popular
    val genres: StateFlow<Query<List<List<Title>>>> = _genres
    /** The billboard title; stays the same while the screen lives */
    val featured: StateFlow<Title?> = _featured

    private var shown = false

    init {
        refresh()
    }

    /** The screen is composed (again): answers that expired or depend on a newer version load again. */
    fun onShown() {
        if (shown) refresh() else shown = true
    }

    fun refresh() {
        val pid = core.session.profileId ?: 0
        val pv = core.progress.version.value
        val rv = core.ratings.version.value
        val cache = core.cache
        // The billboard is picked before the feeds show, so focus restore sees both at once
        _feeds.load {
            core.catalog.homeFeeds().also { f -> _featured.value = pickFeatured(f.trendingMovies + f.trendingTv, _featured.value?.key) }
        }
        _cw.load { cache.get(cache.scoped("browse-cw-p$pv"), 15.seconds) { core.progress.continueWatching(pid) } }
        _recs.load { cache.get(cache.scoped("browse-recs-p$pv-r$rv"), 5.minutes) { core.catalog.recommendations(pid) } }
        _favs.load { cache.get(cache.scoped("browse-favs-r$rv"), 1.minutes) { core.catalog.favorites(pid) } }
        _trMovies.load { core.catalog.top10(Top10Region.Turkey, MediaType.Movie) }
        _trTv.load { core.catalog.top10(Top10Region.Turkey, MediaType.Tv) }
        _popular.load { core.catalog.popularMovies() }
        _genres.load {
            coroutineScope {
                GENRE_ROWS.map { g -> async { listOrEmpty { core.catalog.discoverMovies(g.params) } } }.awaitAll()
            }
        }
    }
}

/** Home's rows in webOS order. */
internal fun homeRows(
    s: BrowseStrings,
    profileName: String,
    feeds: Query<HomeFeeds>,
    cw: Query<List<StreamProgress>>,
    recs: Query<List<RecommendationRow>>,
    favs: Query<List<Title>>,
    trMovies: Query<List<Title>>,
    trTv: Query<List<Title>>,
    popular: Query<List<Title>>,
    genres: Query<List<List<Title>>>,
): List<RowDef> = buildList {
    val recRows = recs.data.orEmpty()
    val serverTrending = recRows.any { it.id == "trending" || it.reason == "popular" }
    add(ContinueRow("cw", s.continueWatching(profileName), cw.items { it }))
    if (recs.pending) add(TitleRow("recs-loading", "", null))
    recRows.forEach { add(TitleRow("rec-${it.id}", recommendationTitle(s, it), it.items)) }
    if (!serverTrending) add(TitleRow("trending", s.trendingNow, feeds.items { interleave(it.trendingMovies, it.trendingTv) }))
    add(TitleRow("top10-tr-movie", s.top10TrMovies, trMovies.items { it }, rank = true))
    add(TitleRow("favorites", s.favorites, favs.items { it }))
    add(TitleRow("popular-movies", s.popularMovies, popular.items { it }))
    add(TitleRow("top10-tr-tv", s.top10TrTv, trTv.items { it }, rank = true))
    add(TitleRow("popular-tv", s.popularTv, feeds.items { it.popularTv }))
    add(TitleRow("coming-soon", s.comingSoon, feeds.items { it.upcomingMovies }))
    add(TitleRow("new-episodes", s.newEpisodes, feeds.items { it.onTheAirTv }))
    GENRE_ROWS.forEachIndexed { i, g -> add(TitleRow("genre-$i", g.title(s), genres.items { it.getOrNull(i) })) }
}

@Composable
internal fun HomeRoute(nav: Navigator) {
    val core = LocalCore.current
    val vm = viewModel { HomeViewModel(core) }
    LaunchedEffect(vm) { vm.onShown() }

    val feeds by vm.feeds.collectAsStateWithLifecycle()
    val cw by vm.cw.collectAsStateWithLifecycle()
    val recs by vm.recs.collectAsStateWithLifecycle()
    val favs by vm.favs.collectAsStateWithLifecycle()
    val trMovies by vm.trMovies.collectAsStateWithLifecycle()
    val trTv by vm.trTv.collectAsStateWithLifecycle()
    val popular by vm.popular.collectAsStateWithLifecycle()
    val genres by vm.genres.collectAsStateWithLifecycle()
    val featured by vm.featured.collectAsStateWithLifecycle()

    val s = browseStrings.current()
    val name = core.session.profile?.name.orEmpty()
    val rows = remember(s, name, feeds, cw, recs, favs, trMovies, trTv, popular, genres) {
        homeRows(s, name, feeds, cw, recs, favs, trMovies, trTv, popular, genres)
    }

    RowsScreen(
        featured = featured,
        rows = rows,
        ready = !feeds.pending,
        hero = vm.hero,
        nav = nav,
        onRetry = vm::refresh,
        surprise = true,
    )
}
