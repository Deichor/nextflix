package com.nextflix.tv.feature.browse

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.model.HomeData
import com.nextflix.tv.core.model.HomeFeeds
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.RecommendationRow
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.net.NextflixJson
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.LocalDate
import kotlin.random.Random

class BrowseLogicTest {
    private fun movie(id: Int, overview: String = "x".repeat(50), backdrop: String = "/b.jpg") =
        Title(id = id, type = MediaType.Movie, title = "M$id", overview = overview, backdrop = backdrop)

    private fun show(id: Int) = Title(id = id, type = MediaType.Tv, title = "T$id", backdrop = "/b.jpg")

    @Test
    fun interleaveAlternatesAndCaps() {
        val out = interleave(listOf(movie(1), movie(2), movie(3)), listOf(show(1)), max = 3)
        assertEquals(listOf("movie-1", "tv-1", "movie-2"), out.map { it.key })
    }

    @Test
    fun uniqueKeepsFirst() {
        assertEquals(listOf(1, 2, 3), unique(listOf(movie(1), movie(2)), listOf(movie(2), movie(3))).map { it.id })
    }

    @Test
    fun featuredKeepsKeyAndSkipsWeakTitles() {
        val pool = listOf(movie(1, overview = "short"), movie(2, backdrop = ""), movie(3), movie(4))
        assertEquals(4, pickFeatured(pool, "movie-4")?.id)
        repeat(20) { assertTrue(pickFeatured(pool, null, Random(it))?.id in setOf(3, 4)) }
        assertNull(pickFeatured(listOf(movie(1, overview = "")), null))
    }

    @Test
    fun rankedNumbersFromOne() {
        assertEquals(listOf(1, 2), ranked(listOf(movie(5), movie(6), movie(7)), 2).map { it.rank })
    }

    @Test
    fun recommendationTitlesAreLocalized() {
        fun row(title: String, reason: String, id: String = "because-movie-1") = RecommendationRow(id, title, reason, emptyList())
        assertEquals("Dune yapımına bayıldığın için", recommendationTitle(BrowseTr, row("Because you loved Dune", "because_loved")))
        assertEquals(
            "Because you added Up to Favorites",
            recommendationTitle(BrowseEn, row("Because you added Up to Favorites", "because_favorite")),
        )
        assertEquals("Gündemdekiler", recommendationTitle(BrowseTr, row("Trending Now", "popular", "trending")))
        assertEquals("Senin İçin En İyi Seçimler", recommendationTitle(BrowseTr, row("Top Picks for You", "top_picks", "top-picks")))
        assertEquals("Odd", recommendationTitle(BrowseTr, row("Odd", "because_liked")))
    }

    @Test
    fun turkishLocative() {
        assertEquals("Bilim kurguda", trLocative("Bilim kurgu"))
        assertEquals("Suçta", trLocative("Suç"))
        assertEquals("Dramda", trLocative("Dram"))
        assertEquals("Gerilimde", trLocative("Gerilim"))
        assertEquals("Komedide", trLocative("Komedi"))
    }

    private fun pick(json: String) = Json.parseToJsonElement(json).toSurprisePick()!!

    @Test
    fun surpriseReasons() {
        val loved = pick(
            """{"title":{"id":1408,"media_type":"tv","title":"Queen of the South","logo_path":"/l.png","genre_ids":[80,18]},
               "reason":{"kind":"because_loved","title":"Breaking Bad","media_type":"tv","tmdb_id":1396}}""",
        )
        assertEquals("/l.png", loved.logo)
        assertEquals(MediaType.Tv, loved.title.type)
        assertEquals("Bayıldığın Breaking Bad dizisine benzediği için", reasonText(BrowseTr, AppLanguage.Tr, loved))
        assertEquals("Because you loved Breaking Bad", reasonText(BrowseEn, AppLanguage.En, loved))

        val genres = pick("""{"title":{"id":1,"title":"A"},"reason":{"kind":"genre","genre_ids":[18,80],"genre_id":18}}""")
        assertEquals("Because you like drama and crime", reasonText(BrowseEn, AppLanguage.En, genres))
        assertEquals("Dram ve suç sevdiğin için", reasonText(BrowseTr, AppLanguage.Tr, genres))

        val adjacent = pick("""{"title":{"id":1,"title":"A"},"reason":{"kind":"adjacent","genre_id":878}}""")
        assertEquals("A new pick in sci-fi", reasonText(BrowseEn, AppLanguage.En, adjacent))
        assertEquals("Bilim kurguda yeni bir keşif", reasonText(BrowseTr, AppLanguage.Tr, adjacent))

        val wild = pick("""{"title":{"id":1,"title":"A","genre_ids":[18]},"reason":{"kind":"wild_card","language":"ko"}}""")
        assertEquals("Bambaşka bir şey – çok beğenilen bir Kore filmi", reasonText(BrowseTr, AppLanguage.Tr, wild))
        val anime = pick("""{"title":{"id":1,"title":"A","genre_ids":[16]},"reason":{"kind":"wild_card","language":"ja"}}""")
        assertEquals("Something completely different – a highly rated anime", reasonText(BrowseEn, AppLanguage.En, anime))
        val english = pick("""{"title":{"id":1,"title":"A"},"reason":{"kind":"wild_card"}}""")
        assertEquals("Something completely different", reasonText(BrowseEn, AppLanguage.En, english))

        val trending = pick("""{"title":{"id":1,"title":"A"},"reason":{"kind":"trending_pick"}}""")
        assertEquals("Trending this week", reasonText(BrowseEn, AppLanguage.En, trending))
    }

    @Test
    fun homeRowsFollowLoadState() {
        val feeds = HomeFeeds(listOf(movie(1)), emptyList(), emptyList(), emptyList(), listOf(show(2)), emptyList(), emptyList(), emptyList())
        val home = Query(
            HomeData(
                feeds = feeds,
                popular = listOf(movie(3)),
                top10Movie = null,
                top10Tv = emptyList(),
                genres = mapOf("comedies" to listOf(movie(4)), "horror" to null, "unknown" to listOf(movie(5))),
            ),
        )
        val recs = Query(listOf(RecommendationRow("trending", "Trending Now", "popular", listOf(movie(9)))))
        val rows = homeRows(BrowseEn, "Ann", home, Query(failed = true), recs, Query())
        val byId = rows.associateBy { it.id }
        assertEquals("Continue Watching for Ann", byId.getValue("cw").title)
        assertEquals(emptyList<Any>(), byId.getValue("cw").items)
        // The server sends a trending row, so the local one is left out
        assertTrue("trending" !in byId)
        assertEquals("Trending Now", byId.getValue("rec-trending").title)
        assertNull(byId.getValue("favorites").items)
        assertTrue("recs-loading" !in byId)
        assertEquals(listOf(3), (byId.getValue("popular-movies").items as List<*>).map { (it as Title).id })
        // Null sections hide their rows; genre rows go by id, unknown ids are ignored
        assertEquals(emptyList<Any>(), byId.getValue("top10-tr-movie").items)
        assertEquals(listOf(4), (byId.getValue("genre-comedies").items as List<*>).map { (it as Title).id })
        assertEquals(emptyList<Any>(), byId.getValue("genre-horror").items)
        assertEquals(emptyList<Any>(), byId.getValue("genre-action_adventure").items)
        assertEquals(GENRE_ROWS.map { "genre-${it.id}" }, rows.map { it.id }.filter { it.startsWith("genre-") })
    }

    @Test
    fun homeRowsWhileLoading() {
        val rows = homeRows(BrowseEn, "Ann", Query(), Query(), Query(), Query())
        val byId = rows.associateBy { it.id }
        assertTrue("recs-loading" in byId)
        assertNull(byId.getValue("trending").items)
        assertNull(byId.getValue("genre-acclaimed").items)
        assertEquals(mapOf("sort_by" to "vote_average.desc", "vote_count.gte" to "5000"), GENRE_ROWS.last().params)
    }

    @Test
    fun upcomingEpisodeLabel() {
        val today = LocalDate.of(2026, 10, 22) // Thursday
        fun entry(date: String?) = StreamProgress(profileId = 1, mediaType = MediaType.Tv, tmdbId = 7, season = 2, episode = 5, nextAirDate = date)
        assertNull(upcomingLabel(entry(null), BrowseEn, AppLanguage.En, today))
        assertEquals("New episode Thursday", upcomingLabel(entry("2026-10-22"), BrowseEn, AppLanguage.En, today))
        assertEquals("Yeni bölüm Çarşamba", upcomingLabel(entry("2026-10-28"), BrowseTr, AppLanguage.Tr, today))
        assertEquals("New episode October 29", upcomingLabel(entry("2026-10-29"), BrowseEn, AppLanguage.En, today))
        assertEquals("Yeni bölüm 29 Ekim", upcomingLabel(entry("2026-10-29"), BrowseTr, AppLanguage.Tr, today))
    }

    @Test
    fun upcomingEntryDecodes() {
        val list = NextflixJson.decodeFromString(
            ListSerializer(StreamProgress.serializer()),
            """[{"profile_id":1,"media_type":"tv","tmdb_id":7,"season":2,"episode":5,"position":0,"next_air_date":"2026-10-29"},
               {"profile_id":1,"media_type":"tv","tmdb_id":8,"season":1,"episode":1,"position":30}]""",
        )
        assertEquals("2026-10-29", list[0].nextAirDate)
        assertTrue(list[0].isUpcoming)
        assertFalse(list[1].isUpcoming)
    }
}
