package com.nextflix.tv.core.nav

import androidx.compose.runtime.Immutable
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.nextEpisodeOf
import kotlinx.serialization.Serializable

/** Every screen of the app. The app's NavHost maps each one to its feature entry point. */
@Immutable
@Serializable
sealed interface Route {
    /** First run, Settings > Server, Profiles > Change server: the server address. */
    @Serializable data object Setup : Route

    /** "Who's watching?": the start screen once a server is set. */
    @Serializable data object Profiles : Route

    /** New profile; [existing] = number of profiles now (the limit is Profile.MAX_PROFILES). */
    @Serializable data class AddProfile(val existing: Int) : Route

    @Serializable data object Home : Route
    @Serializable data object Movies : Route
    @Serializable data object TvShows : Route
    @Serializable data object Search : Route
    @Serializable data object Settings : Route

    /** One surprise title per press (opened from Home under the last row). */
    @Serializable data object Surprise : Route

    /** Detail page; [autoplay]: start playback once the page knows what Play starts (hero "Play", Surprise "Play"). */
    @Serializable data class Detail(val type: MediaType, val tmdbId: Int, val autoplay: Boolean = false) : Route

    /** Full-screen player. Back from the player returns to the screen that opened it, focus restored. */
    @Serializable data class Player(val request: PlayRequest) : Route
}

/** Main screens with the side menu, in menu order. */
enum class Tab(val route: Route) {
    Search(Route.Search),
    Home(Route.Home),
    TvShows(Route.TvShows),
    Movies(Route.Movies),
    Settings(Route.Settings),
}

/** The menu tab of this route, or null for screens without the side menu. */
val Route.tab: Tab? get() = Tab.entries.firstOrNull { it.route == this }

/** Torrent file a progress entry saved (reused by the player through /api/torrent-stream/reuse). */
@Immutable
@Serializable
data class SavedSource(
    /** 40-char info hash (lowercase) */
    val hash: String,
    /** file index inside the torrent */
    val index: Int,
    val magnet: String? = null,
)

/**
 * Everything the player needs to start a title (webOS lib/stream.ts PlayRequest + StreamMeta).
 *
 * Openers: the Detail page (Play / Resume / an episode), Continue Watching cards ([StreamProgress.toPlayRequest]),
 * Surprise (through Detail with autoplay). The player resolves a source itself unless [source] is set, saves
 * progress, and plays the next episode ([nextEpisode]) on its own.
 */
@Immutable
@Serializable
data class PlayRequest(
    val type: MediaType,
    val tmdbId: Int,
    /** title in the profile language; the server resolves with the English title of tmdbId anyway */
    val title: String,
    /** movies: release year (resolve hint) */
    val year: Int? = null,
    /** shows: both set */
    val season: Int? = null,
    val episode: Int? = null,
    /** "" / null for TMDB placeholder names */
    val episodeTitle: String? = null,
    /** movies: runtime in minutes (resolve hint and duration check) */
    val runtimeMinutes: Int? = null,
    val posterPath: String? = null,
    val backdropPath: String? = null,
    /** ISO 639-1 of the original language (audio track pick) */
    val originalLanguage: String? = null,
    /** seconds to start at (resume: 5 s before the saved position) */
    val startAt: Double = 0.0,
    /** a saved torrent file to try first (resume, Continue Watching); null: resolve */
    val source: SavedSource? = null,
    /** shows: the seasons for the next episode; empty: the player loads them (CatalogRepository.tvSeasons) */
    val seasons: List<SeasonInfo> = emptyList(),
    /** play a cinema recording of a film that is only in cinemas (resolve with cinema=1) */
    val cinema: Boolean = false,
) {
    val isEpisode: Boolean get() = type == MediaType.Tv && season != null && episode != null

    /** "tv-1396-s1e2" / "movie-27205" */
    val key: String get() = if (isEpisode) "${type.apiName}-$tmdbId-s${season}e$episode" else "${type.apiName}-$tmdbId"

    /**
     * The request for the episode after this one (from the start, resolved fresh), or null for movies and the
     * last episode. Uses [seasons] or the [seasons] argument when the player loaded them.
     */
    fun nextEpisode(seasons: List<SeasonInfo> = this.seasons): PlayRequest? {
        if (!isEpisode) return null
        val (s, e) = nextEpisodeOf(seasons, season!!, episode!!) ?: return null
        return copy(season = s, episode = e, episodeTitle = null, startAt = 0.0, source = null, seasons = seasons)
    }
}
