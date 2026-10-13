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
import com.nextflix.tv.core.data.CatalogRepository
import com.nextflix.tv.core.model.HomeFeeds
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.nav.Navigator
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.time.LocalDate

private typealias ListLoader = suspend (CatalogRepository) -> List<Title>

private fun loader(block: ListLoader): ListLoader = block

/**
 * Movies and TV Shows tabs (webOS screens/MoviesScreen.tsx, TvShowsScreen.tsx): the home feeds plus their own
 * lists, each row loaded on its own (a failed list hides its row).
 */
internal class CatalogViewModel(private val core: Core, private val kind: Kind) : LoadingViewModel() {
    enum class Kind { Movies, Tv }

    val hero = HeroState(viewModelScope)

    private val loaders: Map<String, ListLoader> = when (kind) {
        Kind.Movies -> mapOf(
            "popular" to loader { c -> c.popularMovies() },
            "acclaimed" to loader { c -> c.discoverMovies(mapOf("sort_by" to "vote_average.desc", "vote_count.gte" to "3000")) },
            "recent" to loader { c ->
                c.discoverMovies(mapOf("primary_release_date.gte" to "${LocalDate.now().year - 1}-01-01", "vote_count.gte" to "100"))
            },
            "action" to loader { c -> c.discoverMovies(GENRE_ROWS[0].params) },
            "comedy" to loader { c -> c.discoverMovies(GENRE_ROWS[1].params) },
            "scifi" to loader { c -> c.discoverMovies(GENRE_ROWS[2].params) },
            "horror" to loader { c -> c.discoverMovies(GENRE_ROWS[3].params) },
        )
        Kind.Tv -> mapOf(
            "netflix" to loader { c -> c.providerTv(PROVIDER_NETFLIX) },
            "disney" to loader { c -> c.providerTv(PROVIDER_DISNEY) },
            "max" to loader { c -> c.providerTv(PROVIDER_MAX) },
            "acclaimed" to loader { c -> c.providerTv(PROVIDER_NETFLIX, "vote_average.desc") },
        )
    }

    private val _feeds = query<HomeFeeds>()
    private val _featured = MutableStateFlow<Title?>(null)
    private val _lists = loaders.mapValues { query<List<Title>>() }

    val feeds: StateFlow<Query<HomeFeeds>> = _feeds
    val featured: StateFlow<Title?> = _featured
    /** The tab's own lists by id; a failed one is [] */
    val lists: Map<String, StateFlow<Query<List<Title>>>> = _lists

    private var shown = false

    init {
        refresh()
    }

    fun onShown() {
        if (shown) refresh() else shown = true
    }

    fun refresh() {
        _feeds.load {
            core.catalog.homeFeeds().also { f ->
                val pool = if (kind == Kind.Movies) f.trendingMovies else f.trendingTv
                _featured.value = pickFeatured(pool, _featured.value?.key)
            }
        }
        _lists.forEach { (id, target) ->
            val load = loaders.getValue(id)
            target.load { listOrEmpty { load(core.catalog) } }
        }
    }

    private companion object {
        const val PROVIDER_NETFLIX = 8
        const val PROVIDER_DISNEY = 337
        const val PROVIDER_MAX = 1899
    }
}

/** Movies tab rows in webOS order. */
internal fun movieRows(s: BrowseStrings, feeds: Query<HomeFeeds>, lists: Map<String, Query<List<Title>>>): List<RowDef> {
    fun list(id: String) = lists[id]?.items { it }
    return listOf(
        TitleRow("top10", s.top10Movies, feeds.items { ranked(it.trendingMovies) }, rank = true),
        TitleRow("trending", s.trendingNow, feeds.items { unique(it.trendingWeekMovies, it.trendingMovies) }),
        TitleRow("popular", s.popularOnNextflix, list("popular")),
        TitleRow("new", s.newReleases, list("recent")),
        TitleRow("theaters", s.nowPlaying, feeds.items { it.nowPlaying }),
        TitleRow("acclaimed", s.genreAcclaimedMovies, list("acclaimed")),
        TitleRow("action", s.genreActionAdventure, list("action")),
        TitleRow("comedy", s.genreComedies, list("comedy")),
        TitleRow("coming", s.comingSoon, feeds.items { it.upcomingMovies }),
        TitleRow("scifi", s.genreSciFiFantasy, list("scifi")),
        TitleRow("horror", s.genreHorror, list("horror")),
    )
}

/** TV Shows tab rows in webOS order. */
internal fun tvRows(s: BrowseStrings, feeds: Query<HomeFeeds>, lists: Map<String, Query<List<Title>>>): List<RowDef> {
    fun list(id: String) = lists[id]?.items { it }
    return listOf(
        TitleRow("top10", s.top10Tv, feeds.items { ranked(it.trendingTv) }, rank = true),
        TitleRow("trending", s.trendingNow, feeds.items { unique(it.popularTv, it.trendingTv) }),
        TitleRow("netflix", s.providerNetflix, list("netflix")),
        TitleRow("new", s.newEpisodes, feeds.items { it.airingToday }),
        TitleRow("airing", s.currentlyAiring, feeds.items { it.onTheAirTv }),
        TitleRow("disney", s.providerDisney, list("disney")),
        TitleRow("max", s.providerMax, list("max")),
        TitleRow("acclaimed", s.acclaimedTv, list("acclaimed")),
    )
}

@Composable
internal fun CatalogRoute(nav: Navigator, kind: CatalogViewModel.Kind) {
    val core = LocalCore.current
    val vm = viewModel(key = kind.name) { CatalogViewModel(core, kind) }
    LaunchedEffect(vm) { vm.onShown() }

    val feeds by vm.feeds.collectAsStateWithLifecycle()
    val featured by vm.featured.collectAsStateWithLifecycle()
    val lists = vm.lists.mapValues { (_, flow) -> flow.collectAsStateWithLifecycle().value }

    val s = browseStrings.current()
    val rows = remember(s, feeds, lists) {
        if (kind == CatalogViewModel.Kind.Movies) movieRows(s, feeds, lists) else tvRows(s, feeds, lists)
    }

    RowsScreen(
        featured = featured,
        rows = rows,
        ready = !feeds.pending,
        hero = vm.hero,
        nav = nav,
        onRetry = vm::refresh,
        kicker = if (kind == CatalogViewModel.Kind.Movies) s.kickerFilm else s.kickerSeries,
    )
}
