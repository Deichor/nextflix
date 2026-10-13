package com.nextflix.tv.core.data

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.model.Availability
import com.nextflix.tv.core.model.AvailabilityStatus
import com.nextflix.tv.core.model.Details
import com.nextflix.tv.core.model.EpisodeInfo
import com.nextflix.tv.core.model.HomeData
import com.nextflix.tv.core.model.HomeFeeds
import com.nextflix.tv.core.model.HomeGenres
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.RecommendationRow
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.Top10Region
import com.nextflix.tv.core.model.placeholderEpisodes
import com.nextflix.tv.core.model.toDetails
import com.nextflix.tv.core.model.toEpisodes
import com.nextflix.tv.core.model.toTitles
import com.nextflix.tv.core.net.AnswerCache
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.ApiException
import com.nextflix.tv.core.net.arr
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import com.nextflix.tv.core.session.Session
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.serialization.json.JsonElement
import kotlin.time.Duration
import kotlin.time.Duration.Companion.hours
import kotlin.time.Duration.Companion.minutes

/**
 * TMDB catalog through the server (docs/API.md "Catalog"), shared by Browse, Detail, Search and the player.
 * Answers follow the profile language (X-Lang). Cached answers are scoped by server, profile and language; lists
 * that do not depend on the profile only by server and language.
 */
class CatalogRepository internal constructor(
    private val api: ApiClient,
    private val cache: AnswerCache,
    private val session: Session,
) {

    private suspend fun <T> cached(key: String, ttl: Duration = 10.minutes, load: suspend () -> T): T =
        cache.get(cache.scoped(key), ttl, load = load)

    /** For answers that do not depend on the profile (only server and language). */
    private suspend fun <T> shared(key: String, ttl: Duration = 10.minutes, load: suspend () -> T): T =
        cache.get(cache.langScoped(key), ttl, load = load)

    /** Servers that answered 404 / 405 to GET /api/home in this run (asked again after a restart). */
    @Volatile
    private var oldServers: Set<String> = emptySet()

    // ---- Lists ----

    /**
     * Home's catalog rows in one call (GET /api/home), shared by every profile with [language]. A server without
     * that endpoint gets the same data from the single endpoints. Answers with null sections are not cached.
     */
    suspend fun home(language: AppLanguage = session.language): HomeData {
        val server = session.server ?: throw ApiException.NoServer()
        return cache.get(cache.langScoped("home", language), 10.minutes, keep = { it.complete }) {
            if (server in oldServers) {
                legacyHome(language)
            } else {
                try {
                    api.getJson("/api/home", mapOf("x_lang" to language.code)).toHomeData().also { seed(language, it) }
                } catch (e: ApiException.Http) {
                    if (!e.isOldServer()) throw e
                    oldServers = oldServers + server
                    legacyHome(language)
                }
            }
        }
    }

    /** Fills the single endpoints' cache entries (Movies tab) from a GET /api/home answer (same bodies). */
    private fun seed(language: AppLanguage, data: HomeData) {
        fun put(key: String, value: Any?) {
            if (value != null) cache.store(cache.langScoped(key, language), 10.minutes, value)
        }
        put("feeds", data.feeds?.takeIf { it.hasMovies && it.hasTv })
        put("popular-movies", data.popular)
        put(top10Key(Top10Region.Turkey, MediaType.Movie), data.top10Movie)
        put(top10Key(Top10Region.Turkey, MediaType.Tv), data.top10Tv)
        HomeGenres.forEach { (id, params) -> put(discoverKey(params), data.genres[id]) }
    }

    private fun top10Key(region: Top10Region, type: MediaType) = "top10-${region.apiName}-${type.apiName}"

    private fun discoverKey(params: Map<String, String>) = "discover-movie-${params.entries.sortedBy { it.key }.joinToString("&")}"

    /** GET /api/home of an old server: the single endpoints in parallel. */
    private suspend fun legacyHome(language: AppLanguage): HomeData {
        // The single endpoints follow the profile language (X-Lang)
        if (language != session.language) throw ApiException.Decode(IllegalStateException("old server: home in $language"))
        val data = coroutineScope {
            val feeds = async { orNull { homeFeeds() } }
            val popular = async { orNull { popularMovies() } }
            val trMovie = async { orNull { top10(Top10Region.Turkey, MediaType.Movie) } }
            val trTv = async { orNull { top10(Top10Region.Turkey, MediaType.Tv) } }
            val genres = HomeGenres.mapValues { (_, params) -> async { orNull { discoverMovies(params) } } }
            HomeData(feeds.await(), popular.await(), trMovie.await(), trTv.await(), genres.mapValues { it.value.await() })
        }
        if (data.empty) throw ApiException.Decode(IllegalStateException("home unavailable"))
        return data
    }

    /** Home feeds; throws only when both feed requests fail. */
    suspend fun homeFeeds(): HomeFeeds = shared("feeds") {
        coroutineScope {
            val movies = async { orNull { api.getJson("/api/upcoming-movies") } }
            val tv = async { orNull { api.getJson("/api/upcoming-tv-series") } }
            feedsOf(movies.await()?.obj, tv.await()?.obj) ?: throw ApiException.Decode(IllegalStateException("feeds unavailable"))
        }
    }

    /** Netflix Top 10 of a region; titles carry [Title.rank]. 503 before the first download. */
    suspend fun top10(region: Top10Region, type: MediaType): List<Title> = shared(top10Key(region, type)) {
        top10Of(api.getJson("/api/top10", mapOf("region" to region.apiName, "type" to type.apiName)), type)
    }

    /** Personal rows (top picks, because you loved ...); not cached, rows change after ratings. */
    suspend fun recommendations(profileId: Int): List<RecommendationRow> =
        recommendationRowsOf(api.getJson("/api/recommendations", mapOf("profile_id" to profileId)).obj?.get("rows"))

    /** Home's recommendations: cached briefly per progress / ratings version (shared with the Home prefetch). */
    suspend fun homeRecommendations(profileId: Int, progressVersion: Int, ratingsVersion: Int): List<RecommendationRow> =
        cached("browse-recs-$profileId-p$progressVersion-r$ratingsVersion", 5.minutes) { recommendations(profileId) }

    /** The profile's favorites, most recently added first. */
    suspend fun favorites(profileId: Int): List<Title> =
        favoritesOf(api.getJson("/api/favorites", mapOf("profile_id" to profileId)))

    /** Home's favorites: cached briefly per ratings version (shared with the Home prefetch). */
    suspend fun homeFavorites(profileId: Int, ratingsVersion: Int): List<Title> =
        cached("browse-favs-$profileId-r$ratingsVersion", 1.minutes) { favorites(profileId) }

    suspend fun popularMovies(): List<Title> = shared("popular-movies") {
        resultsOf(api.getJson("/api/tmdb/movie/popular"), MediaType.Movie)
    }

    /** TMDB discover for movies; [params] like `with_genres=28|12`, `vote_count.gte=300`, `sort_by=...`. */
    suspend fun discoverMovies(params: Map<String, String>): List<Title> =
        shared(discoverKey(params)) {
            val q = mapOf("sort_by" to "popularity.desc", "include_adult" to "false") + params
            resultsOf(api.getJson("/api/tmdb/discover/movie", q), MediaType.Movie)
        }

    /** Shows on one streaming provider (TV Shows page rows). */
    suspend fun providerTv(providerId: Int, sortBy: String = "popularity.desc"): List<Title> =
        shared("provider-$providerId-$sortBy") {
            resultsOf(api.getJson("/api/providers/$providerId/tv", mapOf("sort_by" to sortBy, "page" to 1)), MediaType.Tv)
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

/** Runs [block], null on any failure except cancellation. */
internal suspend fun <T> orNull(block: suspend () -> T?): T? =
    try {
        block()
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) {
        null
    }
