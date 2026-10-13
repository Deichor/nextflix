package com.nextflix.tv.feature.detail

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.model.Availability
import com.nextflix.tv.core.model.Details
import com.nextflix.tv.core.model.EpisodeInfo
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.Rating
import com.nextflix.tv.core.model.RatingTarget
import com.nextflix.tv.core.model.RatingValue
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.placeholderEpisodes
import com.nextflix.tv.core.stream.ResolveParams
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

internal sealed interface DetailLoad {
    data object Loading : DetailLoad
    data object Failed : DetailLoad
    data class Ready(val details: Details) : DetailLoad
}

/** Episodes of one season; [failed]: numbered placeholders, asked again on the next visit. */
internal data class SeasonEpisodes(val items: List<EpisodeInfo>, val failed: Boolean = false)

internal class DetailViewModel(private val core: Core, private val type: MediaType, private val id: Int) : ViewModel() {
    private val pid = core.session.profileId ?: 0

    private val _load = MutableStateFlow<DetailLoad>(DetailLoad.Loading)
    val load: StateFlow<DetailLoad> = _load.asStateFlow()

    private val _logo = MutableStateFlow("")
    val logo: StateFlow<String> = _logo.asStateFlow()

    private val _related = MutableStateFlow<List<Title>>(emptyList())
    val related: StateFlow<List<Title>> = _related.asStateFlow()

    /** Every entry of the title, newest first; null until loaded. */
    private val _progress = MutableStateFlow<List<StreamProgress>?>(null)
    val progress: StateFlow<List<StreamProgress>?> = _progress.asStateFlow()

    private val _rating = MutableStateFlow(Rating())
    val rating: StateFlow<Rating> = _rating.asStateFlow()

    /** Availability of what Play starts; null while unknown. */
    private val _availability = MutableStateFlow<Availability?>(null)
    val availability: StateFlow<Availability?> = _availability.asStateFlow()

    /** Season picked on the page; 0 follows the play plan. */
    private val _season = MutableStateFlow(0)
    val season: StateFlow<Int> = _season.asStateFlow()

    private val _episodes = MutableStateFlow<Map<Int, SeasonEpisodes>>(emptyMap())
    val episodes: StateFlow<Map<Int, SeasonEpisodes>> = _episodes.asStateFlow()

    val plan: StateFlow<PlayPlan?> = combine(_load, _progress) { load, entries ->
        if (load is DetailLoad.Ready && entries != null) planOf(load.details, entries) else null
    }.stateIn(viewModelScope, SharingStarted.Eagerly, null)

    /** Route.Detail.autoplay ran (not again after Back from the player). */
    var autoplayed = false

    private val details: Details? get() = (_load.value as? DetailLoad.Ready)?.details
    private var detailsJob: Job? = null
    private var progressVersion = -1
    private val episodeJobs = HashMap<Int, Job>()
    private var warmed: ResolveParams? = null

    init {
        loadDetails()
        loadProgress()
        if (pid != 0) viewModelScope.launch { _rating.value = core.ratings.get(pid, type, id) }
        watchAvailability()
    }

    fun retry() {
        if (_load.value is DetailLoad.Failed) loadDetails()
    }

    private fun loadDetails() {
        if (detailsJob?.isActive == true) return
        _load.value = DetailLoad.Loading
        detailsJob = viewModelScope.launch {
            val d = try {
                core.catalog.details(id, type)
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                _load.value = DetailLoad.Failed
                return@launch
            }
            _load.value = DetailLoad.Ready(d)
            if (d.type == MediaType.Movie) launch { _logo.value = core.catalog.movieLogo(id, core.session.language.code) }
            launch {
                _related.value = try {
                    core.catalog.related(id, type)
                } catch (e: CancellationException) {
                    throw e
                } catch (_: Exception) {
                    emptyList()
                }
            }
        }
    }

    private fun loadProgress() {
        progressVersion = core.progress.version.value
        viewModelScope.launch { _progress.value = core.progress.forTitle(pid, type, id) }
    }

    /** Back from the player (or anything that saved progress): reload the bars and the play plan. */
    fun refreshProgressIfStale() {
        if (core.progress.version.value != progressVersion) loadProgress()
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    private fun watchAvailability() {
        viewModelScope.launch {
            plan.filterNotNull()
                .map { it.ep }
                .distinctUntilChanged()
                .collectLatest { ep -> _availability.value = core.catalog.availability(type, id, ep?.season, ep?.episode) }
        }
    }

    // ---- Seasons ----

    fun selectSeason(season: Int) {
        _season.value = season
    }

    fun ensureEpisodes(season: SeasonInfo) {
        val have = _episodes.value[season.seasonNumber]
        if (have != null && !have.failed) return
        if (episodeJobs[season.seasonNumber]?.isActive == true) return
        episodeJobs[season.seasonNumber] = viewModelScope.launch {
            val result = try {
                SeasonEpisodes(core.catalog.loadSeasonEpisodes(id, season))
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                SeasonEpisodes(season.placeholderEpisodes(), failed = true)
            }
            _episodes.update { it + (season.seasonNumber to result) }
        }
    }

    // ---- Rating and favorite ----

    fun toggleFavorite() {
        val favorite = !_rating.value.favorite
        save(_rating.value.copy(favorite = favorite)) { core.ratings.setFavorite(pid, it, favorite) }
    }

    fun rate(value: RatingValue) {
        val next = if (_rating.value.rating == value) null else value
        save(_rating.value.copy(rating = next)) { core.ratings.setRating(pid, it, next) }
    }

    private fun save(optimistic: Rating, send: suspend (RatingTarget) -> Rating?) {
        val d = details ?: return
        if (pid == 0) return
        val before = _rating.value
        _rating.value = optimistic
        viewModelScope.launch {
            _rating.value = send(RatingTarget(d.type, d.id, d.title, d.poster, d.backdrop)) ?: before
        }
    }

    // ---- Warm-up ----

    /** Warms what Play starts (the intent to watch); a changed target releases the previous one after a grace. */
    fun warm(d: Details, plan: PlayPlan) {
        val target = d.warmTarget(plan) ?: return
        val prev = warmed
        if (prev == target) return
        if (prev != null) core.warmup.release(prev, LEAVE_GRACE_S)
        warmed = target
        core.warmup.prefetch(target)
    }

    /** The player takes over the warmed source (it releases what it leaves). */
    fun onOpenPlayer() {
        warmed = null
    }

    /** The page is left (Back, another detail page): the server may drop the warmed source after a grace. */
    fun leave() {
        warmed?.let { core.warmup.release(it, LEAVE_GRACE_S) }
        warmed = null
    }

    override fun onCleared() {
        leave()
    }

    private companion object {
        // Seconds a warmed source survives leaving the page (web: LEAVE_GRACE_S)
        const val LEAVE_GRACE_S = 60
    }
}
