package com.nextflix.tv.core.model

import com.nextflix.tv.core.net.arr
import com.nextflix.tv.core.net.dbl
import com.nextflix.tv.core.net.int
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.intOrNull

// Raw TMDB / server JSON -> models, as forgiving as the webOS app (lib/catalog.ts): bad items are skipped.

/** One TMDB result (or a server row item) -> Title; null for people, items without id or name. */
fun JsonElement?.toTitle(fallback: MediaType): Title? {
    val o = this.obj ?: return null
    val id = o.int("id") ?: o.int("tmdb_id") ?: return null
    val mediaType = o.str("media_type")
    if (mediaType == "person") return null
    val title = o.str("title") ?: o.str("name") ?: o.str("original_title") ?: o.str("original_name") ?: return null
    if (title.isEmpty()) return null
    val type = MediaType.of(mediaType) ?: if (o.str("name") != null && o.str("title") == null) MediaType.Tv else fallback
    return Title(
        id = id,
        type = type,
        title = title,
        overview = o.str("overview").orEmpty(),
        poster = o.str("poster_path").orEmpty(),
        backdrop = o.str("backdrop_path").orEmpty(),
        date = o.str("release_date") ?: o.str("first_air_date") ?: "",
        vote = o.dbl("vote_average") ?: 0.0,
        genreIds = o.arr("genre_ids")?.mapNotNull { (it as? JsonPrimitive)?.intOrNull }.orEmpty(),
        originalLanguage = o.str("original_language"),
        rank = o.int("rank"),
    )
}

/** A list of TMDB results -> titles with an image, without duplicates. */
fun JsonElement?.toTitles(fallback: MediaType): List<Title> {
    val list = this as? JsonArray ?: return emptyList()
    val seen = HashSet<String>()
    return list.mapNotNull { raw ->
        raw.toTitle(fallback)?.takeIf { (it.backdrop.isNotEmpty() || it.poster.isNotEmpty()) && seen.add(it.key) }
    }
}

/** Seasons with episodes, specials left out, sorted. */
fun JsonElement?.toSeasons(): List<SeasonInfo> =
    (this as? JsonArray).orEmpty().mapNotNull { raw ->
        val o = raw.obj ?: return@mapNotNull null
        val n = o.int("season_number") ?: return@mapNotNull null
        val count = o.int("episode_count") ?: 0
        if (n <= 0 || count <= 0) null else SeasonInfo(n, count, o.str("name"))
    }.sortedBy { it.seasonNumber }

/** GET /api/tmdb-movie/:id answer ({media_type, data} or the bare object) -> Details. */
fun JsonElement.toDetails(): Details? {
    val root = obj ?: return null
    val isTv = root.str("media_type") == "tv"
    val d: JsonObject = root.obj("data") ?: root
    val id = d.int("id") ?: return null
    val credits = d.obj("credits")
    val names = { list: JsonArray? -> list.orEmpty().mapNotNull { it.obj?.str("name") } }
    return Details(
        id = id,
        type = if (isTv) MediaType.Tv else MediaType.Movie,
        title = (if (isTv) d.str("name") else d.str("title")) ?: d.str("original_title") ?: d.str("original_name") ?: "",
        originalTitle = (if (isTv) d.str("original_name") else d.str("original_title")).orEmpty(),
        overview = d.str("overview").orEmpty(),
        tagline = d.str("tagline").orEmpty(),
        date = (if (isTv) d.str("first_air_date") else d.str("release_date")).orEmpty(),
        poster = d.str("poster_path").orEmpty(),
        backdrop = d.str("backdrop_path").orEmpty(),
        vote = d.dbl("vote_average") ?: 0.0,
        runtime = if (isTv) 0 else d.int("runtime") ?: 0,
        episodeRunTime = if (isTv) d.arr("episode_run_time")?.firstOrNull()?.let { (it as? JsonPrimitive)?.intOrNull } ?: 0 else 0,
        genres = names(d.arr("genres")),
        cast = names(credits?.arr("cast")).take(6),
        directors = credits?.arr("crew").orEmpty().mapNotNull { c -> c.obj?.takeIf { it.str("job") == "Director" }?.str("name") },
        creators = names(d.arr("created_by")),
        originalLanguage = d.str("original_language"),
        numberOfSeasons = d.int("number_of_seasons") ?: 0,
        seasons = if (isTv) d.arr("seasons").toSeasons() else emptyList(),
    )
}

/** Episodes of GET /api/tmdb/tv/:id/season/:n. */
fun JsonElement?.toEpisodes(season: Int): List<EpisodeInfo> =
    this?.obj?.arr("episodes").orEmpty().mapNotNull { raw ->
        val e = raw.obj ?: return@mapNotNull null
        EpisodeInfo(
            episodeNumber = e.int("episode_number") ?: return@mapNotNull null,
            seasonNumber = e.int("season_number") ?: season,
            name = e.str("name").orEmpty(),
            overview = e.str("overview").orEmpty(),
            stillPath = e.str("still_path").orEmpty(),
            runtime = e.int("runtime") ?: 0,
            airDate = e.str("air_date").orEmpty(),
        )
    }

/** Numbered entries without names, for a season whose episode list could not be loaded. */
fun SeasonInfo.placeholderEpisodes(): List<EpisodeInfo> =
    (1..episodeCount).map { EpisodeInfo(it, seasonNumber, "", "", "", 0, "") }
