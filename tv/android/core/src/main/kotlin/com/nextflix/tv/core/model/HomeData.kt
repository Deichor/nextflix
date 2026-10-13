package com.nextflix.tv.core.model

import androidx.compose.runtime.Immutable

/**
 * Genre rows of GET /api/home by id, in row order (server tmdb.HomeGenreRows). The server adds
 * sort_by=popularity.desc and include_adult=false; these params win.
 */
val HomeGenres: Map<String, Map<String, String>> = linkedMapOf(
    "action_adventure" to mapOf("with_genres" to "28|12", "vote_count.gte" to "300"),
    "comedies" to mapOf("with_genres" to "35", "vote_count.gte" to "300"),
    "scifi_fantasy" to mapOf("with_genres" to "878|14", "vote_count.gte" to "300"),
    "horror" to mapOf("with_genres" to "27", "vote_count.gte" to "200"),
    "acclaimed" to mapOf("sort_by" to "vote_average.desc", "vote_count.gte" to "5000"),
)

/** Home's catalog rows (GET /api/home). A null section failed or was not ready on the server. */
@Immutable
data class HomeData(
    val feeds: HomeFeeds?,
    val popular: List<Title>?,
    val top10Movie: List<Title>?,
    val top10Tv: List<Title>?,
    /** By [HomeGenres] id; a missing id reads as null */
    val genres: Map<String, List<Title>?>,
) {
    /** Every section is there. */
    val complete: Boolean
        get() = feeds != null && feeds.hasMovies && feeds.hasTv && popular != null && top10Movie != null &&
            top10Tv != null && HomeGenres.keys.all { genres[it] != null }

    /** No section at all. */
    val empty: Boolean
        get() = feeds == null && popular == null && top10Movie == null && top10Tv == null && genres.values.all { it == null }

    /** This answer with its null sections taken from [previous]. */
    fun over(previous: HomeData?): HomeData {
        if (previous == null || complete) return this
        return HomeData(
            feeds = feeds?.over(previous.feeds) ?: previous.feeds,
            popular = popular ?: previous.popular,
            top10Movie = top10Movie ?: previous.top10Movie,
            top10Tv = top10Tv ?: previous.top10Tv,
            genres = previous.genres + genres.filterValues { it != null },
        )
    }
}
