package com.nextflix.tv.feature.search

import androidx.compose.runtime.Immutable
import com.nextflix.tv.core.model.MediaType

// Search filters: same chips and rules as webOS lib/discover.ts and web/src/features/search/filters.ts

internal enum class TypeFilter(val apiName: String, val media: MediaType?) {
    All("all", null),
    Movie("movie", MediaType.Movie),
    Tv("tv", MediaType.Tv),
}

@Immutable
internal data class SearchFilters(
    val type: TypeFilter = TypeFilter.All,
    /** TMDB genre ids, combined with AND */
    val genres: List<Int> = emptyList(),
    val anime: Boolean = false,
) {
    val active: Boolean get() = type != TypeFilter.All || genres.isNotEmpty() || anime
    val key: String get() = "${type.apiName}|${genres.joinToString(",")}|${if (anime) 1 else 0}"

    /** Another type; genres that do not exist for it are dropped. */
    fun withType(type: TypeFilter): SearchFilters = copy(
        type = type,
        genres = genres.filter { id -> GenreChips.firstOrNull { it.id == id }?.fits(type) == true },
    )

    fun toggleGenre(id: Int): SearchFilters = copy(genres = if (id in genres) genres - id else genres + id)

    companion object {
        val None = SearchFilters()
    }
}

internal class GenreChip(val id: Int, private val types: Set<MediaType>) {
    fun fits(type: TypeFilter): Boolean = type.media == null || type.media in types
}

private val Both = setOf(MediaType.Movie, MediaType.Tv)
private val MovieOnly = setOf(MediaType.Movie)
private val TvOnly = setOf(MediaType.Tv)

internal val GenreChips = listOf(
    GenreChip(28, Both),
    GenreChip(12, Both),
    GenreChip(16, Both),
    GenreChip(35, Both),
    GenreChip(80, Both),
    GenreChip(99, Both),
    GenreChip(18, Both),
    GenreChip(10751, Both),
    GenreChip(14, Both),
    GenreChip(36, MovieOnly),
    GenreChip(27, MovieOnly),
    GenreChip(10762, TvOnly),
    GenreChip(10402, MovieOnly),
    GenreChip(9648, Both),
    GenreChip(10764, TvOnly),
    GenreChip(10749, MovieOnly),
    GenreChip(878, Both),
    GenreChip(53, Both),
    GenreChip(10752, Both),
    GenreChip(37, Both),
)

/** Query of GET /api/catalog/discover: browse by filters (no [q], with [page]) or filter the search results of [q]. */
internal fun discoverQuery(filters: SearchFilters, q: String, page: Int?): Map<String, Any?> = buildMap {
    if (page != null) put("page", page)
    if (filters.type != TypeFilter.All) put("type", filters.type.apiName)
    if (filters.genres.isNotEmpty()) put("genres", filters.genres.joinToString(","))
    if (filters.anime) put("anime", 1)
    if (q.isNotEmpty()) put("q", q)
}
