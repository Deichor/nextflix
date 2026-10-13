package com.nextflix.tv.core.data

import com.nextflix.tv.core.model.HomeData
import com.nextflix.tv.core.model.HomeFeeds
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.RecommendationRow
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.toTitle
import com.nextflix.tv.core.model.toTitles
import com.nextflix.tv.core.net.ApiException
import com.nextflix.tv.core.net.arr
import com.nextflix.tv.core.net.int
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.add
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

// Parsers of the home answers, shared by the single endpoints, GET /api/home and the home snapshot.

/** GET /api/upcoming-movies and /api/upcoming-tv-series bodies -> feeds; null when both are missing. */
internal fun feedsOf(movies: JsonObject?, tv: JsonObject?): HomeFeeds? {
    if (movies == null && tv == null) return null
    return HomeFeeds(
        trendingMovies = movies?.get("trending_daily").toTitles(MediaType.Movie),
        trendingWeekMovies = movies?.get("trending_weekly").toTitles(MediaType.Movie),
        nowPlaying = movies?.get("now_playing").toTitles(MediaType.Movie),
        upcomingMovies = movies?.get("upcoming").toTitles(MediaType.Movie),
        trendingTv = tv?.get("trending_daily").toTitles(MediaType.Tv),
        popularTv = tv?.get("trending_weekly").toTitles(MediaType.Tv),
        onTheAirTv = tv?.get("on_the_air").toTitles(MediaType.Tv),
        airingToday = tv?.get("airing_today").toTitles(MediaType.Tv),
        hasMovies = movies != null,
        hasTv = tv != null,
    )
}

/** GET /api/top10 body -> titles with [Title.rank]. */
internal fun top10Of(body: JsonElement?, type: MediaType): List<Title> =
    body.obj?.arr("items").orEmpty().mapIndexedNotNull { i, raw ->
        raw.toTitle(type)?.takeIf { it.poster.isNotEmpty() || it.backdrop.isNotEmpty() }?.let { it.copy(rank = it.rank ?: (i + 1)) }
    }

/** A TMDB list body ({results: [...]}) -> titles. */
internal fun resultsOf(body: JsonElement?, type: MediaType): List<Title> = body.obj?.get("results").toTitles(type)

/** GET /api/recommendations rows -> rows with items. */
internal fun recommendationRowsOf(rows: JsonElement?): List<RecommendationRow> =
    rows.arr.orEmpty().mapNotNull { raw ->
        val r = raw.obj ?: return@mapNotNull null
        val id = r.str("id").orEmpty()
        val items = r["items"].toTitles(MediaType.Movie)
        if (id.isEmpty() || items.isEmpty()) null else RecommendationRow(id, r.str("title").orEmpty(), r.str("reason").orEmpty(), items)
    }

/** GET /api/favorites body -> titles with an image. */
internal fun favoritesOf(list: JsonElement?): List<Title> =
    list.arr.orEmpty().mapNotNull { raw ->
        val o = raw.obj ?: return@mapNotNull null
        val id = o.int("tmdb_id") ?: return@mapNotNull null
        val type = MediaType.of(o.str("media_type")) ?: MediaType.Movie
        val name = o.str("title")?.takeIf { it.isNotEmpty() } ?: return@mapNotNull null
        Title(id = id, type = type, title = name, poster = o.str("poster_path").orEmpty(), backdrop = o.str("backdrop_path").orEmpty())
            .takeIf { it.poster.isNotEmpty() || it.backdrop.isNotEmpty() }
    }

/** GET /api/home body -> sections; a null section stays null. Genre rows by id (unknown ids are kept, unused). */
internal fun JsonElement.toHomeData(): HomeData {
    val o = obj ?: throw ApiException.Decode(IllegalStateException("home: not an object"))
    val genres = LinkedHashMap<String, List<Title>?>()
    o.arr("genres").orEmpty().forEach { raw ->
        val g = raw.obj ?: return@forEach
        val id = g.str("id") ?: return@forEach
        genres[id] = g.obj("data")?.let { resultsOf(it, MediaType.Movie) }
    }
    return HomeData(
        feeds = feedsOf(o.obj("upcoming_movies"), o.obj("upcoming_tv_series")),
        popular = o.obj("popular_movies")?.let { resultsOf(it, MediaType.Movie) },
        top10Movie = o.obj("top10_tr_movie")?.let { top10Of(it, MediaType.Movie) },
        top10Tv = o.obj("top10_tr_tv")?.let { top10Of(it, MediaType.Tv) },
        genres = genres,
    )
}

/** 404 / 405 from GET /api/home: a server from before that endpoint. */
internal fun Throwable.isOldServer(): Boolean = this is ApiException.Http && (status == 404 || status == 405)

// ---- Back to the server's JSON shapes (home snapshot); the parsers above read them unchanged ----

/** A title as a TMDB result. */
internal fun Title.toJson(): JsonObject = buildJsonObject {
    put("id", id)
    put("media_type", type.apiName)
    put("title", title)
    put("overview", overview)
    put("poster_path", poster)
    put("backdrop_path", backdrop)
    put("release_date", date)
    put("vote_average", vote)
    put("genre_ids", buildJsonArray { genreIds.forEach { add(it) } })
    originalLanguage?.let { put("original_language", it) }
    rank?.let { put("rank", it) }
}

internal fun List<Title>.toJson(): JsonArray = JsonArray(map { it.toJson() })

/** A favorite as GET /api/favorites sends it. */
internal fun Title.toFavoriteJson(): JsonObject = buildJsonObject {
    put("tmdb_id", id)
    put("media_type", type.apiName)
    put("title", title)
    put("poster_path", poster)
    put("backdrop_path", backdrop)
}

internal fun RecommendationRow.toJson(): JsonObject = buildJsonObject {
    put("id", id)
    put("title", title)
    put("reason", reason)
    put("items", items.toJson())
}

/** The GET /api/home shape of these sections. */
internal fun HomeData.toJson(): JsonObject = buildJsonObject {
    put("version", 1)
    put("upcoming_movies", feeds?.takeIf { it.hasMovies }?.let { f ->
        buildJsonObject {
            put("trending_daily", f.trendingMovies.toJson())
            put("trending_weekly", f.trendingWeekMovies.toJson())
            put("now_playing", f.nowPlaying.toJson())
            put("upcoming", f.upcomingMovies.toJson())
        }
    } ?: JsonNull)
    put("upcoming_tv_series", feeds?.takeIf { it.hasTv }?.let { f ->
        buildJsonObject {
            put("airing_today", f.airingToday.toJson())
            put("on_the_air", f.onTheAirTv.toJson())
            put("trending_daily", f.trendingTv.toJson())
            put("trending_weekly", f.popularTv.toJson())
        }
    } ?: JsonNull)
    put("popular_movies", popular?.let { buildJsonObject { put("results", it.toJson()) } } ?: JsonNull)
    put("top10_tr_movie", top10Movie?.let { buildJsonObject { put("items", it.toJson()) } } ?: JsonNull)
    put("top10_tr_tv", top10Tv?.let { buildJsonObject { put("items", it.toJson()) } } ?: JsonNull)
    put("genres", buildJsonArray {
        genres.forEach { (id, list) ->
            add(buildJsonObject {
                put("id", id)
                put("data", list?.let { buildJsonObject { put("results", it.toJson()) } } ?: JsonNull)
            })
        }
    })
}
