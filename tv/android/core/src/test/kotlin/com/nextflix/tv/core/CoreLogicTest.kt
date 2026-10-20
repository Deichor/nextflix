package com.nextflix.tv.core

import com.nextflix.tv.core.data.HomeSnapshotStore
import com.nextflix.tv.core.data.favoritesOf
import com.nextflix.tv.core.data.feedsOf
import com.nextflix.tv.core.data.isOldServer
import com.nextflix.tv.core.data.recommendationRowsOf
import com.nextflix.tv.core.data.resultsOf
import com.nextflix.tv.core.data.toFavoriteJson
import com.nextflix.tv.core.data.toHomeData
import com.nextflix.tv.core.data.toJson
import com.nextflix.tv.core.data.top10Of
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.model.HomeData
import com.nextflix.tv.core.model.HomeGenres
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.toDetails
import com.nextflix.tv.core.model.toPlayRequest
import com.nextflix.tv.core.model.toTitles
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.net.ApiException
import com.nextflix.tv.core.net.NextflixJson
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.prefs.SubtitleSize
import com.nextflix.tv.core.prefs.SubtitleStyle
import com.nextflix.tv.core.session.ServerAddress
import com.nextflix.tv.core.stream.PlaybackCaps
import com.nextflix.tv.core.stream.ResolveParams
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonArray
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.nio.file.Files
import java.util.concurrent.TimeUnit

class CoreLogicTest {
    private val server = "http://192.168.1.221:8252"

    @Test
    fun imagesGoThroughTheServerProxy() {
        assertEquals("$server/tmdb-img/t/p/w500/abc.jpg", TmdbImages.url(server, "/abc.jpg", "w500"))
        assertEquals("$server/tmdb-img/t/p/w500/abc.jpg", TmdbImages.url(server, "abc.jpg", "w500"))
        assertEquals("$server/tmdb-img/t/p/w300/xyz.png", TmdbImages.url(server, "https://image.tmdb.org/t/p/w1280/xyz.png", "w300"))
        assertEquals("$server/tmdb-img/t/p/w342/poster.jpg", TmdbImages.url(server, "/tmdb-img/t/p/w500/poster.jpg", "w342"))
        assertEquals("", TmdbImages.url(server, null, "w500"))
        assertEquals("", TmdbImages.url(null, "/abc.jpg", "w500"))
    }

    @Test
    fun serverAddressIsNormalized() {
        assertEquals("http://192.168.1.5:8252", ServerAddress.normalize(" 192.168.1.5/ "))
        assertEquals("http://nextflix.local:8252", ServerAddress.normalize("nextflix.local"))
        assertEquals("http://host:9000", ServerAddress.normalize("http://host:9000/"))
        assertEquals("https://host", ServerAddress.normalize("https://host"))
    }

    @Test
    fun tmdbListsBecomeTitles() {
        val json = NextflixJson.parseToJsonElement(
            """
            [
              {"id": 1, "title": "Movie", "backdrop_path": "/b.jpg", "release_date": "2024-05-01", "vote_average": 7.4},
              {"id": 2, "name": "Show", "poster_path": "/p.jpg", "first_air_date": "2020-01-01"},
              {"id": 3, "media_type": "person", "name": "Someone", "poster_path": "/x.jpg"},
              {"id": 4, "title": "No image"},
              {"id": 1, "title": "Movie again", "backdrop_path": "/b.jpg"}
            ]
            """,
        )
        val titles = json.toTitles(MediaType.Movie)
        assertEquals(listOf("movie-1", "tv-2"), titles.map { it.key })
        assertEquals(2024, titles[0].year)
        assertEquals(74, titles[0].matchPercent)
    }

    @Test
    fun detailsOfAShowKeepRealSeasonsOnly() {
        val json = NextflixJson.parseToJsonElement(
            """
            {"media_type": "tv", "data": {"id": 1396, "name": "Breaking Bad", "first_air_date": "2008-01-20",
              "episode_run_time": [47], "number_of_seasons": 2,
              "seasons": [{"season_number": 0, "episode_count": 9}, {"season_number": 2, "episode_count": 13},
                          {"season_number": 1, "episode_count": 7}],
              "credits": {"cast": [{"name": "Bryan"}], "crew": [{"name": "Vince", "job": "Director"}]}}}
            """,
        )
        val d = json.toDetails()!!
        assertEquals(MediaType.Tv, d.type)
        assertEquals(listOf(1, 2), d.seasons.map { it.seasonNumber })
        assertEquals(47, d.episodeRunTime)
        assertEquals(listOf("Vince"), d.directors)
    }

    @Test
    fun nextEpisodeCrossesSeasons() {
        val seasons = listOf(SeasonInfo(1, 2), SeasonInfo(2, 3))
        val req = PlayRequest(MediaType.Tv, 1, "Show", season = 1, episode = 2, startAt = 300.0, seasons = seasons)
        val next = req.nextEpisode()!!
        assertEquals(2 to 1, next.season to next.episode)
        assertEquals(0.0, next.startAt, 0.0)
        assertNull(req.copy(season = 2, episode = 3).nextEpisode())
        assertNull(PlayRequest(MediaType.Movie, 1, "Movie").nextEpisode())
    }

    @Test
    fun progressResumesFiveSecondsEarlier() {
        val p = StreamProgress(profileId = 1, mediaType = MediaType.Tv, tmdbId = 7, title = "Show", season = 1, episode = 3, hash = "ab", fileIndex = 2, position = 100.0, duration = 1000.0)
        val req = p.toPlayRequest()
        assertEquals(95.0, req.startAt, 0.0)
        assertEquals("ab", req.source?.hash)
        assertEquals(0.0, p.copy(position = 950.0).toPlayRequest().startAt, 0.0)
    }

    @Test
    fun routesSurviveSerialization() {
        val route: Route = Route.Player(PlayRequest(MediaType.Movie, 27205, "Inception", year = 2010))
        val text = NextflixJson.encodeToString(Route.serializer(), route)
        assertEquals(route, NextflixJson.decodeFromString(Route.serializer(), text))
    }

    @Test
    fun capsAndResolveQueries() {
        val caps = PlaybackCaps(hevc = true, hdr = true, audio = listOf("aac", "opus"), splitAudio = listOf("eac3"))
        assertEquals(mapOf("hevc" to "1", "hdr" to "1", "acodecs" to "aac,opus,eac3"), caps.query(stream = false))
        assertEquals(mapOf("hevc" to "1", "hdr" to "1", "acodecs" to "aac,opus", "split_acodecs" to "eac3"), caps.query(stream = true))
        val q = ResolveParams(MediaType.Tv, " Show ", tmdbId = 5, year = 2001).query()
        assertEquals(mapOf("type" to "tv", "title" to "Show", "season" to "1", "episode" to "1", "tmdb_id" to "5"), q)
    }

    @Test
    fun subtitleStyleCyclesWithWrapAround() {
        val style = SubtitleStyle(size = SubtitleSize.XLarge)
        assertEquals(SubtitleSize.Small, style.cycle(SubtitleStyle.Option.Size).size)
        assertEquals(SubtitleSize.Large, style.cycle(SubtitleStyle.Option.Size, -1).size)
    }

    // ---- GET /api/home ----

    private val moviesBody = """{"trending_daily": [{"id": 1, "title": "M1", "backdrop_path": "/b.jpg", "overview": "o"}],
        "trending_weekly": [], "now_playing": [], "upcoming": [{"id": 2, "title": "M2", "poster_path": "/p.jpg"}], "cached_at": "x"}"""
    private val tvBody = """{"airing_today": [], "on_the_air": [{"id": 3, "name": "S3", "poster_path": "/p.jpg"}],
        "trending_daily": [{"id": 4, "name": "S4", "backdrop_path": "/b.jpg"}], "trending_weekly": [], "cached_at": "x"}"""
    private val popularBody = """{"results": [{"id": 5, "title": "M5", "poster_path": "/p.jpg", "genre_ids": [28]}], "page": 1}"""
    private val top10Body = """{"region": "TR", "week": "w", "type": "movie",
        "items": [{"id": 6, "title": "M6", "poster_path": "/p.jpg"}, {"id": 7, "title": "No image"}, {"id": 8, "title": "M8", "poster_path": "/p.jpg", "rank": 9}]}"""
    private val genreBody = """{"results": [{"id": 10, "title": "M10", "backdrop_path": "/b.jpg"}], "total_results": 1, "filters": {}}"""

    private fun homeBody(genres: String = "[]", tv: String = tvBody, top10Tv: String = "null") = """
        {"version": 1, "lang": "en", "generated_at": "2026-10-13T10:00:00Z",
         "upcoming_movies": $moviesBody, "upcoming_tv_series": $tv, "popular_movies": $popularBody,
         "top10_tr_movie": $top10Body, "top10_tr_tv": $top10Tv, "genres": $genres}
    """

    private fun json(text: String) = NextflixJson.parseToJsonElement(text)

    @Test
    fun homeNullSectionsStayNull() {
        val home = json(homeBody(tv = "null")).toHomeData()
        assertEquals(listOf("movie-1"), home.feeds?.trendingMovies?.map { it.key })
        assertEquals(false, home.feeds?.hasTv)
        assertEquals(emptyList<Title>(), home.feeds?.trendingTv)
        assertNull(home.top10Tv)
        assertEquals(listOf(1, 9), home.top10Movie?.map { it.rank })
        assertFalse(home.complete)

        // A later answer keeps what the earlier one had
        val earlier = json(homeBody(top10Tv = top10Body)).toHomeData()
        val merged = home.over(earlier)
        assertEquals(listOf("tv-4"), merged.feeds?.trendingTv?.map { it.key })
        assertEquals(listOf("movie-1"), merged.feeds?.trendingMovies?.map { it.key })
        assertEquals(listOf(6, 8), merged.top10Tv?.map { it.id })
        assertTrue(json("""{"version": 1}""").toHomeData().empty)
    }

    @Test
    fun homeGenresGoById() {
        val genres = """[{"id": "comedies", "params": {"with_genres": "35"}, "data": $genreBody},
            {"id": "horror", "params": {}, "data": null}, {"id": "future_row", "params": {}, "data": $genreBody}]"""
        val home = json(homeBody(genres)).toHomeData()
        assertEquals(listOf(10), home.genres["comedies"]?.map { it.id })
        assertNull(home.genres["horror"])
        assertNull(home.genres["acclaimed"])
        assertEquals(listOf("action_adventure", "comedies", "scifi_fantasy", "horror", "acclaimed"), HomeGenres.keys.toList())
        assertFalse(home.complete)
    }

    @Test
    fun legacyEndpointsGiveTheSameHome() {
        val genreRows = HomeGenres.keys.joinToString(",", "[", "]") { """{"id": "$it", "params": {}, "data": $genreBody}""" }
        val home = json(homeBody(genreRows, top10Tv = top10Body)).toHomeData()
        // What the old server path builds from the single endpoints
        val legacy = HomeData(
            feeds = feedsOf(json(moviesBody).obj, json(tvBody).obj),
            popular = resultsOf(json(popularBody), MediaType.Movie),
            top10Movie = top10Of(json(top10Body), MediaType.Movie),
            top10Tv = top10Of(json(top10Body), MediaType.Tv),
            genres = HomeGenres.mapValues { resultsOf(json(genreBody), MediaType.Movie) },
        )
        assertEquals(legacy, home)
        assertTrue(home.complete)
        assertTrue(ApiException.Http(404, "", null).isOldServer())
        assertTrue(ApiException.Http(405, "", null).isOldServer())
        assertFalse(ApiException.Http(503, "", null).isOldServer())
    }

    @Test
    fun homeSnapshotReadsBackTheSame() {
        val genreRows = HomeGenres.keys.joinToString(",", "[", "]") { """{"id": "$it", "params": {}, "data": $genreBody}""" }
        val home = json(homeBody(genreRows, tv = "null")).toHomeData()
        assertEquals(home, json(home.toJson().toString()).toHomeData())

        val recs = recommendationRowsOf(json("""[{"id": "top", "title": "Top", "reason": "top_picks", "items": [{"id": 1, "title": "A", "poster_path": "/p.jpg", "original_language": "ko"}]}]"""))
        assertEquals(recs, recommendationRowsOf(JsonArray(recs.map { it.toJson() })))
        val favs = favoritesOf(json("""[{"tmdb_id": 3, "media_type": "tv", "title": "S", "backdrop_path": "/b.jpg"}]"""))
        assertEquals(listOf("tv-3"), favs.map { it.key })
        assertEquals(favs, favoritesOf(JsonArray(favs.map { it.toFavoriteJson() })))
    }

    @Test
    fun snapshotWritesDropFilesTooOldToRead(): Unit = runBlocking {
        val dir = Files.createTempDirectory("home").toFile()
        try {
            val old = File(dir, "home-old-en.json").apply { writeText("{}") }
            old.setLastModified(System.currentTimeMillis() - TimeUnit.DAYS.toMillis(8))
            val recent = File(dir, "home-recent-en.json").apply { writeText("{}") }
            val job = Job()
            HomeSnapshotStore(dir, NextflixJson, CoroutineScope(job)).writeHome(server, AppLanguage.En, json("""{"version": 1}""").toHomeData())
            job.children.forEach { it.join() }
            assertFalse(old.exists())
            assertTrue(recent.exists())
            // The new snapshot next to the recent one
            assertEquals(2, dir.listFiles()!!.size)
        } finally {
            dir.deleteRecursively()
        }
    }
}
