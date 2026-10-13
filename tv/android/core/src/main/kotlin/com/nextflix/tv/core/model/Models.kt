package com.nextflix.tv.core.model

import androidx.compose.runtime.Immutable
import com.nextflix.tv.core.i18n.AppLanguage
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
enum class MediaType(val apiName: String) {
    @SerialName("movie") Movie("movie"),
    @SerialName("tv") Tv("tv");

    companion object {
        fun of(value: String?): MediaType? = entries.firstOrNull { it.apiName == value }
    }
}

/** A Nextflix profile (GET /api/profiles). */
@Immutable
@Serializable
data class Profile(
    val id: Int,
    val name: String,
    val avatar: String = "",
    val color: String = "",
    val language: String? = null,
    @SerialName("created_at") val createdAt: String? = null,
) {
    val appLanguage: AppLanguage get() = AppLanguage.of(language)

    companion object {
        // Same limits as the server and the web app
        const val MAX_PROFILES = 5
        const val MAX_NAME = 20
    }
}

/** Body of POST /api/profiles and PUT /api/profiles/:id. */
@Serializable
data class ProfileInput(
    val name: String,
    val avatar: String,
    val color: String,
    val language: String,
)

/** A TMDB movie or show normalized for cards, rows and heroes (webOS lib/catalog.ts Title). */
@Immutable
@Serializable
data class Title(
    val id: Int,
    val type: MediaType,
    val title: String,
    val overview: String = "",
    /** TMDB image paths ("/abc.jpg"); turn into URLs with Core.imageUrl / TmdbImages */
    val poster: String = "",
    val backdrop: String = "",
    /** release date or first air date, "YYYY-MM-DD" or "" */
    val date: String = "",
    val vote: Double = 0.0,
    val genreIds: List<Int> = emptyList(),
    val originalLanguage: String? = null,
    /** Netflix Top 10 rank (ranked rows only) */
    val rank: Int? = null,
) {
    /** "movie-27205", unique across types */
    val key: String get() = "${type.apiName}-$id"
    val year: Int? get() = yearOf(date)
    /** "87% Match"; 0 when there is no vote */
    val matchPercent: Int get() = matchPercent(vote)
}

fun yearOf(date: String?): Int? = date?.take(4)?.toIntOrNull()?.takeIf { it > 1800 }

fun matchPercent(vote: Double): Int = if (vote > 0) (vote * 10).toInt().coerceIn(1, 99) else 0

/** Full details of a title (GET /api/tmdb-movie/:id). */
@Immutable
data class Details(
    val id: Int,
    val type: MediaType,
    val title: String,
    val originalTitle: String,
    val overview: String,
    val tagline: String,
    val date: String,
    val poster: String,
    val backdrop: String,
    val vote: Double,
    /** movies: minutes, 0 when unknown */
    val runtime: Int,
    /** shows: typical episode length in minutes, 0 when unknown */
    val episodeRunTime: Int,
    val genres: List<String>,
    val cast: List<String>,
    val directors: List<String>,
    val creators: List<String>,
    val originalLanguage: String?,
    val numberOfSeasons: Int,
    /** shows only: seasons with episodes, specials (season 0) left out, sorted */
    val seasons: List<SeasonInfo>,
) {
    val key: String get() = "${type.apiName}-$id"
    val year: Int? get() = yearOf(date)
}

@Immutable
@Serializable
data class SeasonInfo(
    @SerialName("season_number") val seasonNumber: Int,
    @SerialName("episode_count") val episodeCount: Int,
    val name: String? = null,
)

@Immutable
data class EpisodeInfo(
    val episodeNumber: Int,
    val seasonNumber: Int,
    /** "" for TMDB placeholders, see [hasRealName] */
    val name: String,
    val overview: String,
    val stillPath: String,
    val runtime: Int,
    val airDate: String,
) {
    /** TMDB's own names for untitled episodes ("Episode 3", "3. Bölüm") count as no name. */
    val hasRealName: Boolean
        get() = name.isNotEmpty() && name != "Episode $episodeNumber" && name != "$episodeNumber. Bölüm"
}

/** Lists of the home feeds (GET /api/upcoming-movies and /api/upcoming-tv-series). */
@Immutable
data class HomeFeeds(
    val trendingMovies: List<Title>,
    val trendingWeekMovies: List<Title>,
    val nowPlaying: List<Title>,
    val upcomingMovies: List<Title>,
    val trendingTv: List<Title>,
    val popularTv: List<Title>,
    val onTheAirTv: List<Title>,
    val airingToday: List<Title>,
)

/** One server recommendation row (GET /api/recommendations). [reason]: top_picks, popular, because_loved, ... */
@Immutable
data class RecommendationRow(
    val id: String,
    val title: String,
    val reason: String,
    val items: List<Title>,
)

enum class Top10Region(val apiName: String) { Turkey("TR"), Global("global") }

@Serializable
enum class RatingValue(val apiName: String) {
    @SerialName("dislike") Dislike("dislike"),
    @SerialName("like") Like("like"),
    @SerialName("love") Love("love"),
}

@Immutable
data class Rating(val rating: RatingValue? = null, val favorite: Boolean = false)

/** What a rating stores about the title besides the rating (shown in Favorites). */
data class RatingTarget(
    val type: MediaType,
    val tmdbId: Int,
    val title: String,
    val poster: String,
    val backdrop: String,
)

enum class AvailabilityStatus { Available, NotReleased, InTheaters, Unknown }

/** GET /api/availability: whether a title or an episode can be streamed yet. */
@Immutable
data class Availability(
    val status: AvailabilityStatus,
    val releaseDate: String? = null,
    val theatricalDate: String? = null,
    val digitalDate: String? = null,
)
