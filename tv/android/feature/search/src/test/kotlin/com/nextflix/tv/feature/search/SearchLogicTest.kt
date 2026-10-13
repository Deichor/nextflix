package com.nextflix.tv.feature.search

import com.nextflix.tv.core.model.MediaType
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SearchLogicTest {

    @Test
    fun typeChangeDropsGenresOfTheOtherType() {
        val f = SearchFilters(genres = listOf(27, 10762, 28))
        assertEquals(listOf(27, 28), f.withType(TypeFilter.Movie).genres)
        assertEquals(listOf(10762, 28), f.withType(TypeFilter.Tv).genres)
        assertEquals(listOf(27, 10762, 28), f.withType(TypeFilter.All).genres)
    }

    @Test
    fun filtersKeyAndActive() {
        assertFalse(SearchFilters.None.active)
        val f = SearchFilters.None.toggleGenre(16).toggleGenre(878).copy(anime = true)
        assertTrue(f.active)
        assertEquals("all|16,878|1", f.key)
        assertEquals(listOf(878), f.toggleGenre(16).genres)
    }

    @Test
    fun discoverQueryParams() {
        val f = SearchFilters(TypeFilter.Tv, listOf(16, 878), anime = true)
        assertEquals(
            mapOf("page" to 2, "type" to "tv", "genres" to "16,878", "anime" to 1),
            discoverQuery(f, "", 2),
        )
        assertEquals(mapOf("q" to "ghost"), discoverQuery(SearchFilters.None, "ghost", null))
    }

    @Test
    fun parsesSearchAnswer() {
        val json = Json.parseToJsonElement(
            """{"did_you_mean":"The Mandalorian","results":[
              {"id":82856,"title":"The Mandalorian","media_type":"tv","backdrop_path":"/b.jpg"},
              {"id":1,"title":"No image","media_type":"movie"},
              {"id":2,"name":"Someone","media_type":"person","profile_path":"/p.jpg"}]}""",
        )
        val a = parseAnswer(json)
        assertEquals("The Mandalorian", a.didYouMean)
        assertEquals(1, a.items.size)
        assertEquals(MediaType.Tv, a.items[0].type)
        assertEquals("", parseAnswer(Json.parseToJsonElement("""{"results":[]}""")).didYouMean)
    }

    @Test
    fun parsesDiscoverPage() {
        val p = parseDiscover(Json.parseToJsonElement("""{"page":2,"total_pages":0,"results":[]}"""), 2)
        assertEquals(2, p.page)
        assertEquals(1, p.totalPages)
        assertEquals(70, parseDiscover(Json.parseToJsonElement("""{"page":1,"total_pages":70,"results":[]}"""), 1).totalPages)
    }

    @Test
    fun parsesSuggestions() {
        val json = Json.parseToJsonElement(
            """{"query":"thron","ready":true,"results":[{"title":"Game of Thrones","id":1399},
              {"title":"Game of Thrones","id":5},{"title":""},{"title":"Thronecast","id":66477}]}""",
        )
        assertEquals(listOf("Game of Thrones", "Thronecast"), parseSuggest(json))
    }
}
