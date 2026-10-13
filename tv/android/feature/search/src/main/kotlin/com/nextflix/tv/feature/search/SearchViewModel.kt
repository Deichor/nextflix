package com.nextflix.tv.feature.search

import androidx.compose.runtime.Immutable
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.model.Title
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

internal const val MAX_QUERY = 60

@Immutable
internal data class SearchUiState(
    val query: String = "",
    val filters: SearchFilters = SearchFilters.None,
    /** Results of [resultsFor]; null before the first answer */
    val results: List<Title>? = null,
    val resultsFor: String = "",
    val page: Int = 1,
    val totalPages: Int = 1,
    val didYouMean: String = "",
    val completions: List<String> = emptyList(),
    val loading: Boolean = false,
    /** Trending titles shown without query and filters; null while loading */
    val popular: List<Title>? = null,
) {
    val q: String get() = query.trim()
    val wanted: String get() = wantedKey(q, filters)
    val browsing: Boolean get() = q.isEmpty() && filters.active

    /** What the grid shows: results with a query or filters, else the popular titles. */
    val shown: List<Title>? get() = if (q.isNotEmpty() || filters.active) results else popular
}

private fun wantedKey(q: String, filters: SearchFilters) = "$q#${filters.key}"

/** Search state (webOS screens/SearchScreen.tsx); lives while the screen is on the stack, so Back shows the same results. */
internal class SearchViewModel(core: Core) : ViewModel() {
    private val api = SearchApi(core.api)
    private val catalog = core.catalog

    private val _state = MutableStateFlow(SearchUiState())
    val state: StateFlow<SearchUiState> = _state.asStateFlow()

    private var searchJob: Job? = null
    private var suggestJob: Job? = null
    private var moreJob: Job? = null

    init {
        viewModelScope.launch {
            val popular = try {
                val f = catalog.homeFeeds()
                (f.trendingMovies + f.trendingTv + f.trendingWeekMovies + f.popularTv).distinctBy { it.key }.take(24)
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                emptyList()
            }
            _state.update { it.copy(popular = popular) }
        }
    }

    fun setQuery(value: String) {
        val v = value.take(MAX_QUERY)
        if (v == _state.value.query) return
        val oldQ = _state.value.q
        _state.update { it.copy(query = v) }
        if (_state.value.q != oldQ) {
            reload()
            suggest()
        }
    }

    fun setType(type: TypeFilter) = setFilters(_state.value.filters.withType(type))

    fun toggleGenre(id: Int) = setFilters(_state.value.filters.toggleGenre(id))

    fun toggleAnime() = setFilters(_state.value.filters.let { it.copy(anime = !it.anime) })

    private fun setFilters(filters: SearchFilters) {
        if (filters == _state.value.filters) return
        _state.update { it.copy(filters = filters) }
        reload()
    }

    private fun reload() {
        // A pending next page belongs to the old query
        moreJob?.cancel()
        moreJob = null
        searchJob?.cancel()
        val s = _state.value
        val q = s.q
        val filters = s.filters
        val wanted = s.wanted
        if (q.isEmpty() && !filters.active) {
            _state.update { it.copy(results = null, didYouMean = "", loading = false) }
            return
        }
        if (wanted == s.resultsFor && s.results != null) {
            _state.update { it.copy(loading = false) }
            return
        }
        searchJob = viewModelScope.launch {
            delay(if (q.isNotEmpty()) 400 else 150)
            _state.update { it.copy(loading = true) }
            try {
                val answer = when {
                    q.isEmpty() -> api.discover(filters, 1).also { p ->
                        _state.update { it.copy(page = 1, totalPages = p.totalPages) }
                    }.let { SearchAnswer(it.items, "") }
                    filters.active -> api.filtered(filters, q)
                    else -> api.ranked(q)
                }
                _state.update { it.copy(results = answer.items, didYouMean = answer.didYouMean, resultsFor = wanted, loading = false) }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                _state.update { it.copy(results = emptyList(), loading = false) }
            }
        }
    }

    /** Title completions while typing (server typo index, debounced 150 ms). */
    private fun suggest() {
        suggestJob?.cancel()
        val q = _state.value.q
        if (q.length < 2) {
            _state.update { it.copy(completions = emptyList()) }
            return
        }
        suggestJob = viewModelScope.launch {
            delay(150)
            val list = try {
                api.suggest(q).filterNot { it.equals(q, ignoreCase = true) }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                emptyList()
            }
            _state.update { it.copy(completions = list) }
        }
    }

    /** Next browse page; called when focus comes near the end of the grid. */
    fun loadMore() {
        val s = _state.value
        val results = s.results
        if (!s.browsing || moreJob?.isActive == true || results == null || s.page >= s.totalPages || s.resultsFor != s.wanted) return
        val next = s.page + 1
        val forWanted = s.wanted
        val filters = s.filters
        moreJob = viewModelScope.launch {
            try {
                val page = api.discover(filters, next)
                _state.update { cur ->
                    // A page of older filters must not land in the new results
                    if (cur.wanted != forWanted || cur.results == null) return@update cur
                    val seen = cur.results.mapTo(HashSet()) { it.key }
                    cur.copy(results = cur.results + page.items.filter { seen.add(it.key) }, page = next, totalPages = page.totalPages)
                }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
            }
        }
    }
}
