package com.nextflix.tv.feature.browse

import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.LocalCore
import com.nextflix.tv.core.data.ProfileSnapshot
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.model.HomeData
import com.nextflix.tv.core.model.HomeGenres
import com.nextflix.tv.core.model.RecommendationRow
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.nav.Navigator
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.joinAll
import kotlinx.coroutines.launch

/**
 * Genre rows of Home by GET /api/home id (params: [HomeGenres], the server's tmdb.HomeGenreRows); keep in sync with
 * GENRE_ROWS in web/src/features/home/HomeScreen.tsx (the server warms their images).
 */
internal class GenreRow(val id: String, val title: (BrowseStrings) -> String) {
    val params: Map<String, String> get() = HomeGenres.getValue(id)
}

internal val GENRE_ROWS = listOf(
    GenreRow("action_adventure") { it.genreActionAdventure },
    GenreRow("comedies") { it.genreComedies },
    GenreRow("scifi_fantasy") { it.genreSciFiFantasy },
    GenreRow("horror") { it.genreHorror },
    GenreRow("acclaimed") { it.genreAcclaimedMovies },
)

/** Home's rows and whether the catalog part is in (focus can be restored). */
@Immutable
internal data class HomeRows(val rows: List<RowDef>, val ready: Boolean)

/**
 * Home's loads (webOS screens/HomeScreen.tsx): the catalog rows in one call (GET /api/home, shared by every
 * profile), Continue Watching, recommendations and favorites. The last loaded Home is kept on disk and painted
 * first on a cold start. Continue Watching, recommendations and favorites are cached briefly under keys that carry
 * the progress / ratings versions, so a change made on another screen (player, detail page) loads them again when
 * Home shows up.
 */
internal class HomeViewModel(private val core: Core) : LoadingViewModel() {
    val hero = HeroState(viewModelScope)

    private val _home = query<HomeData>()
    private val _cw = query<List<StreamProgress>>()
    private val _recs = query<List<RecommendationRow>>()
    private val _favs = query<List<Title>>()
    private val _featured = MutableStateFlow<Title?>(null)

    /** The billboard title; stays the same while the screen lives (a snapshot's one too, while still a candidate) */
    val featured: StateFlow<Title?> = _featured

    private var shown = false

    init {
        // Cached answers (Profiles' prefetch) land at once; the snapshot fills whatever has nothing yet
        refresh()
        applySnapshot()
    }

    /** Built off the main thread from the four loads; a cached Home has its rows from the first frame. */
    val rows: StateFlow<HomeRows> = combine(
        core.session.state.map { (it?.language ?: AppLanguage.En) to it?.profile?.name.orEmpty() }.distinctUntilChanged(),
        _home, _cw, _recs, _favs,
    ) { (language, name), home, cw, recs, favs ->
        HomeRows(homeRows(browseStrings[language], name, home, cw, recs, favs), ready = !home.pending)
    }
        .flowOn(Dispatchers.Default)
        .distinctUntilChanged()
        .stateIn(viewModelScope, SharingStarted.Eagerly, currentRows())

    private fun currentRows(): HomeRows {
        val home = _home.value
        val rows = homeRows(browseStrings[core.session.language], core.session.profile?.name.orEmpty(), home, _cw.value, _recs.value, _favs.value)
        return HomeRows(rows, ready = !home.pending)
    }

    /** The screen is composed (again): answers that expired or depend on a newer version load again. */
    fun onShown() {
        if (shown) refresh() else shown = true
    }

    fun refresh() {
        val server = core.session.server
        val language = core.session.language
        val pid = core.session.profileId ?: 0
        val pv = core.progress.version.value
        val rv = core.ratings.version.value
        val before = listOf(_home.value.data, _cw.value.data, _recs.value.data, _favs.value.data)
        val fresh = BooleanArray(4)
        var homeComplete = false
        val jobs = listOf(
            // The billboard is picked before the rows show, so focus restore sees both at once
            _home.load {
                core.catalog.home().also {
                    fresh[0] = true
                    homeComplete = it.complete
                }.over(_home.value.data).also(::pickBillboard)
            },
            _cw.load { core.progress.continueWatchingCached(pid).also { fresh[1] = true } },
            _recs.load { core.catalog.homeRecommendations(pid, pv, rv).also { fresh[2] = true } },
            _favs.load { core.catalog.homeFavorites(pid, rv).also { fresh[3] = true } },
        )
        if (server == null) return
        viewModelScope.launch {
            jobs.joinAll()
            // Saved when a load brought something new (not again for an answer from the memory cache)
            fun changed(i: Int, now: Any?) = fresh[i] && now != null && now !== before[i]
            val home = _home.value.data
            // Only a complete answer: one merged over older sections would save them as new
            if (home != null && homeComplete && changed(0, home)) core.homeSnapshots.writeHome(server, language, home)
            val profile = ProfileSnapshot(_cw.value.data, _recs.value.data, _favs.value.data)
            if (pid != 0 && (changed(1, profile.cw) || changed(2, profile.recs) || changed(3, profile.favs))) {
                core.homeSnapshots.writeProfile(server, pid, language, profile)
            }
        }
    }

    /** The last Home on disk, for the loads that have no data yet. */
    private fun applySnapshot() {
        val server = core.session.server ?: return
        val language = core.session.language
        val pid = core.session.profileId
        val queries = listOf(_home, _cw, _recs, _favs)
        if (queries.all { it.value.data != null }) return
        viewModelScope.launch {
            val home = if (_home.value.data == null) core.homeSnapshots.readHome(server, language) else null
            val profile = pid?.let { core.homeSnapshots.readProfile(server, it, language) }
            if (home != null && _home.value.data == null) {
                pickBillboard(home)
                _home.value = Query(home)
            }
            if (profile == null) return@launch
            profile.cw?.let { if (_cw.value.data == null) _cw.value = Query(it) }
            profile.recs?.let { if (_recs.value.data == null) _recs.value = Query(it) }
            profile.favs?.let { if (_favs.value.data == null) _favs.value = Query(it) }
        }
    }

    private fun pickBillboard(home: HomeData) {
        val f = home.feeds ?: return
        _featured.value = pickFeatured(f.trendingMovies + f.trendingTv, _featured.value?.key)
    }
}

/** Home's rows in webOS order. A section the server could not give hides its row. */
internal fun homeRows(
    s: BrowseStrings,
    profileName: String,
    home: Query<HomeData>,
    cw: Query<List<StreamProgress>>,
    recs: Query<List<RecommendationRow>>,
    favs: Query<List<Title>>,
): List<RowDef> = buildList {
    val recRows = recs.data.orEmpty()
    val serverTrending = recRows.any { it.id == "trending" || it.reason == "popular" }
    add(ContinueRow("cw", s.continueWatching(profileName), cw.items { it }))
    if (recs.pending) add(TitleRow("recs-loading", "", null))
    recRows.forEach { add(TitleRow("rec-${it.id}", recommendationTitle(s, it), it.items)) }
    if (!serverTrending) {
        add(TitleRow("trending", s.trendingNow, home.items { h -> h.feeds?.let { interleave(it.trendingMovies, it.trendingTv) }.orEmpty() }))
    }
    add(TitleRow("top10-tr-movie", s.top10TrMovies, home.items { it.top10Movie.orEmpty() }, rank = true))
    add(TitleRow("favorites", s.favorites, favs.items { it }))
    add(TitleRow("popular-movies", s.popularMovies, home.items { it.popular.orEmpty() }))
    add(TitleRow("top10-tr-tv", s.top10TrTv, home.items { it.top10Tv.orEmpty() }, rank = true))
    add(TitleRow("popular-tv", s.popularTv, home.items { it.feeds?.popularTv.orEmpty() }))
    add(TitleRow("coming-soon", s.comingSoon, home.items { it.feeds?.upcomingMovies.orEmpty() }))
    add(TitleRow("new-episodes", s.newEpisodes, home.items { it.feeds?.onTheAirTv.orEmpty() }))
    GENRE_ROWS.forEach { g -> add(TitleRow("genre-${g.id}", g.title(s), home.items { it.genres[g.id].orEmpty() })) }
}

@Composable
internal fun HomeRoute(nav: Navigator) {
    val core = LocalCore.current
    val vm = viewModel { HomeViewModel(core) }
    LaunchedEffect(vm) { vm.onShown() }

    val rows by vm.rows.collectAsStateWithLifecycle()
    val featured by vm.featured.collectAsStateWithLifecycle()

    RowsScreen(
        featured = featured,
        rows = rows.rows,
        ready = rows.ready,
        hero = vm.hero,
        nav = nav,
        onRetry = vm::refresh,
        surprise = true,
    )
}
