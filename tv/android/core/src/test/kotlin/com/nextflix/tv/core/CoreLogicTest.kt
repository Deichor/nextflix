package com.nextflix.tv.core

import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.toDetails
import com.nextflix.tv.core.model.toPlayRequest
import com.nextflix.tv.core.model.toTitles
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.net.NextflixJson
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.prefs.SubtitleSize
import com.nextflix.tv.core.prefs.SubtitleStyle
import com.nextflix.tv.core.session.ServerAddress
import com.nextflix.tv.core.stream.PlaybackCaps
import com.nextflix.tv.core.stream.ResolveParams
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

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
}
