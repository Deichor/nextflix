package com.nextflix.tv.feature.search

import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.toTitles
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.arr
import com.nextflix.tv.core.net.int
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import kotlinx.serialization.json.JsonElement

/** Results of a search; [didYouMean] is the corrected query whose results were merged in (typo), "" otherwise. */
internal data class SearchAnswer(val items: List<Title>, val didYouMean: String)

internal data class DiscoverPage(val items: List<Title>, val page: Int, val totalPages: Int)

/** Search endpoints (webOS lib/search.ts and the discover part of lib/discover.ts; docs/API.md Search, Discover). */
internal class SearchApi(private val api: ApiClient) {

    /** GET /api/tmdb/search: ranked results of [q] (one page) and an optional correction. */
    suspend fun ranked(q: String): SearchAnswer = parseAnswer(api.getJson("/api/tmdb/search", mapOf("q" to q)))

    /** GET /api/catalog/discover with a query: the ranked results of [q] that pass the filters. */
    suspend fun filtered(filters: SearchFilters, q: String): SearchAnswer =
        parseAnswer(api.getJson("/api/catalog/discover", discoverQuery(filters, q, page = null)))

    /** GET /api/catalog/discover without a query: one page of the ranked browse list. */
    suspend fun discover(filters: SearchFilters, page: Int): DiscoverPage =
        parseDiscover(api.getJson("/api/catalog/discover", discoverQuery(filters, "", page)), page)

    /** GET /api/catalog/suggest: title completions from the server's typo index. */
    suspend fun suggest(q: String): List<String> =
        parseSuggest(api.getJson("/api/catalog/suggest", mapOf("q" to q, "limit" to 5)))
}

internal fun parseAnswer(data: JsonElement): SearchAnswer {
    val o = data.obj
    return SearchAnswer(o?.get("results").toTitles(MediaType.Movie), o?.str("did_you_mean").orEmpty())
}

internal fun parseDiscover(data: JsonElement, page: Int): DiscoverPage {
    val o = data.obj
    return DiscoverPage(
        items = o?.get("results").toTitles(MediaType.Movie),
        page = o?.int("page")?.takeIf { it > 0 } ?: page,
        totalPages = o?.int("total_pages")?.takeIf { it > 0 } ?: 1,
    )
}

internal fun parseSuggest(data: JsonElement): List<String> =
    data.obj?.arr("results").orEmpty()
        .mapNotNull { it.obj?.str("title")?.takeIf(String::isNotEmpty) }
        .distinct()
