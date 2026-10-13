package com.nextflix.tv.core.data

import com.nextflix.tv.core.model.Availability
import com.nextflix.tv.core.model.AvailabilityStatus
import com.nextflix.tv.core.model.Details
import com.nextflix.tv.core.model.EpisodeInfo
import com.nextflix.tv.core.model.HomeFeeds
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.RecommendationRow
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.Top10Region
import com.nextflix.tv.core.model.placeholderEpisodes
import com.nextflix.tv.core.model.toDetails
import com.nextflix.tv.core.model.toEpisodes
import com.nextflix.tv.core.model.toTitle
import com.nextflix.tv.core.model.toTitles
import com.nextflix.tv.core.net.AnswerCache
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.ApiException
import com.nextflix.tv.core.net.arr
import com.nextflix.tv.core.net.int
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlin.time.Duration
import kotlin.time.Duration.Companion.hours
import kotlin.time.Duration.Companion.minutes

/**
 * TMDB catalog through the server (docs/API.md "Catalog"), shared by Browse, Detail, Search and the player.
 * Answers follow the profile language (X-Lang). Cached answers are scoped by server, profile and language.
 */
class CatalogRepository internal constructor(private val api: ApiClient, private val cache: AnswerCache) {

    private suspend fun <T> cached(key: String, ttl: Duration = 10.minutes, load: suspend () -> T): T =
        cache.get(cache.scoped(key), ttl, load = load)

    // ---- Lists ----

    /** Home feeds; throws only when both feed requests fail. */
    suspend fun homeFeeds(): HomeFeeds = cached("feeds") {
        coroutineScope {
            val movies = async { orNull { api.getJson("/api/upcoming-movies") } }
            val tv = async { orNull { api.getJson("/api/upcoming-tv-series") } }
            val m = movies.await()?.obj
            val t = tv.await()?.obj
            if (m == null && t == null) throw ApiException.Decode(IllegalStateException("feeds unavailable"))
            HomeFeeds(
                trendingMovies = m?.get("trending_daily").toTitles(MediaType.Movie),
                trendingWeekMovies = m?.get("trending_weekly").toTitles(MediaType.Movie),
                nowPlaying = m?.get("now_playing").toTitles(MediaType.Movie),
                upcomingMovies = m?.get("upcoming").toTitles(MediaType.Movie),
                trendingTv = t?.get("trending_daily").toTitles(MediaType.Tv),
                popularTv = t?.get("trending_weekly").toTitles(MediaType.Tv),
                onTheAirTv = t?.get("on_the_air").toTitles(MediaType.Tv),
                airingToday = t?.get("airing_today").toTitles(MediaType.Tv),
            )
        }
    }

    /** Netflix Top 10 of a region; titles carry [Title.rank]. 503 before the first download. */
    suspend fun top10(region: Top10Region, type: MediaType): List<Title> = cached("top10-${region.apiName}-${type.apiName}") {
        val items = api.getJson("/api/top10", mapOf("region" to region.apiName, "type" to type.apiName)).obj?.arr("items").orEmpty()
        items.mapIndexedNotNull { i, raw ->
            raw.toTitle(type)?.takeIf { it.poster.isNotEmpty() || it.backdrop.isNotEmpty() }?.let { it.copy(rank = it.rank ?: (i + 1)) }
        }
    }

    /** Personal rows (top picks, because you loved ...); not cached, rows change after ratings. */
    suspend fun recommendations(profileId: Int): List<RecommendationRow> {
        val rows = api.getJson("/api/recommendations", mapOf("profile_id" to profileId)).obj?.arr("rows").orEmpty()
        return rows.mapNotNull { raw ->
            val r = raw.obj ?: return@mapNotNull null
            val id = r.str("id").orEmpty()
            val items = r["items"].toTitles(MediaType.Movie)
            if (id.isEmpty() || items.isEmpty()) null else RecommendationRow(id, r.str("title").orEmpty(), r.str("reason").orEmpty(), items)
        }
    }

    /** The profile's favorites, most recently added first. */
    suspend fun favorites(profileId: Int): List<Title> {
        val list = api.getJson("/api/favorites", mapOf("profile_id" to profileId)).arr.orEmpty()
        return list.mapNotNull { raw ->
            val o = raw.obj ?: return@mapNotNull null
            o.toTitleWith(idKey = "tmdb_id")?.takeIf { it.poster.isNotEmpty() || it.backdrop.isNotEmpty() }
        }
    }

    suspend fun popularMovies(): List<Title> = cached("popular-movies") {
        api.getJson("/api/tmdb/movie/popular").obj?.get("results").toTitles(MediaType.Movie)
    }

    /** TMDB discover for movies; [params] like `with_genres=28|12`, `vote_count.gte=300`, `sort_by=...`. */
    suspend fun discoverMovies(params: Map<String, String>): List<Title> =
        cached("discover-movie-${params.entries.sortedBy { it.key }.joinToString("&")}") {
            val q = mapOf("sort_by" to "popularity.desc", "include_adult" to "false") + params
            api.getJson("/api/tmdb/discover/movie", q).obj?.get("results").toTitles(MediaType.Movie)
        }

    /** Shows on one streaming provider (TV Shows page rows). */
    suspend fun providerTv(providerId: Int, sortBy: String = "popularity.desc"): List<Title> =
        cached("provider-$providerId-$sortBy") {
            api.getJson("/api/providers/$providerId/tv", mapOf("sort_by" to sortBy, "page" to 1)).obj?.get("results").toTitles(MediaType.Tv)
        }

    // ---- Details ----

    suspend fun details(id: Int, type: MediaType): Details = cached("detail-${type.apiName}-$id") {
        api.getJson("/api/tmdb-movie/$id", mapOf("type" to type.apiName)).toDetails()
            ?: throw ApiException.Http(404, "not found", null)
    }

    /** Movie title logo path in [lang], else English, else language-less; "" when none. */
    suspend fun movieLogo(id: Int, lang: String): String = cached("logo-$id") {
        orNull {
            val logos = api.getJson("/api/tmdb/movie/$id/images").obj?.arr("logos").orEmpty().mapNotNull { it.obj }
            val pick = logos.firstOrNull { it.str("iso_639_1") == lang }
                ?: logos.firstOrNull { it.str("iso_639_1") == "en" }
                ?: logos.firstOrNull { it.str("iso_639_1").isNullOrEmpty() }
            pick?.str("file_path")
        }.orEmpty()
    }

    /** "More Like This" from the server's matcher (collection first, profile aware). */
    suspend fun related(id: Int, type: MediaType): List<Title> = cached("related-${type.apiName}-$id") {
        api.getJson("/api/catalog/${type.apiName}/$id/similar", mapOf("limit" to 24)).obj?.get("results")
            .toTitles(type).filterNot { it.id == id && it.type == type }
    }

    /** Episodes of a season; throws when the request fails (do not cache a failure). */
    suspend fun loadSeasonEpisodes(tvId: Int, season: SeasonInfo): List<EpisodeInfo> =
        cached("season-$tvId-${season.seasonNumber}", ttl = 1.hours) {
            api.getJson("/api/tmdb/tv/$tvId/season/${season.seasonNumber}").toEpisodes(season.seasonNumber)
                .ifEmpty { season.placeholderEpisodes() }
        }

    /** Like [loadSeasonEpisodes], but numbered placeholders on failure. */
    suspend fun seasonEpisodes(tvId: Int, season: SeasonInfo): List<EpisodeInfo> =
        orNull { loadSeasonEpisodes(tvId, season) } ?: season.placeholderEpisodes()

    /** Seasons of a show ([] on failure); the player uses it for Continue Watching requests. */
    suspend fun tvSeasons(tvId: Int): List<SeasonInfo> = orNull { details(tvId, MediaType.Tv).seasons }.orEmpty()

    /** Whether a title / an episode can be streamed yet; null when unknown. */
    suspend fun availability(type: MediaType, tmdbId: Int, season: Int? = null, episode: Int? = null): Availability? = orNull {
        val q = buildMap {
            put("media_type", type.apiName)
            put("tmdb_id", tmdbId)
            if (type == MediaType.Tv && season != null && episode != null) {
                put("season", season)
                put("episode", episode)
            }
        }
        api.getJson("/api/availability", q).toAvailability()
    }

    /** Drops cached answers whose key contains [match] ("feeds", "detail-tv-1396", ...). */
    fun invalidate(match: String) = cache.invalidate(match)
}

/** The 425 body of resolve and /api/availability share this shape. */
fun JsonElement?.toAvailability(): Availability? {
    val o = obj ?: return null
    val status = when (o.str("status")) {
        "available" -> AvailabilityStatus.Available
        "not_released" -> AvailabilityStatus.NotReleased
        "in_theaters" -> AvailabilityStatus.InTheaters
        "unknown" -> AvailabilityStatus.Unknown
        else -> return null
    }
    return Availability(status, o.str("release_date"), o.str("theatrical_date"), o.str("digital_date"))
}

private fun JsonObject.toTitleWith(idKey: String): Title? {
    val id = int(idKey) ?: return null
    val type = MediaType.of(str("media_type")) ?: MediaType.Movie
    val name = str("title")?.takeIf { it.isNotEmpty() } ?: return null
    return Title(id = id, type = type, title = name, poster = str("poster_path").orEmpty(), backdrop = str("backdrop_path").orEmpty())
}

/** Runs [block], null on any failure except cancellation. */
internal suspend fun <T> orNull(block: suspend () -> T?): T? =
    try {
        block()
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) {
        null
    }
