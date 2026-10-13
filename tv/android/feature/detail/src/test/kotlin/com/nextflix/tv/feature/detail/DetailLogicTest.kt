package com.nextflix.tv.feature.detail

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.coreStrings
import com.nextflix.tv.core.model.Availability
import com.nextflix.tv.core.model.AvailabilityStatus
import com.nextflix.tv.core.model.Details
import com.nextflix.tv.core.model.EpisodeInfo
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.nav.SavedSource
import com.nextflix.tv.core.stream.ResolveParams
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.LocalDate

class DetailLogicTest {
    private val hash = "a".repeat(40)

    private fun details(type: MediaType, seasons: List<SeasonInfo> = emptyList()) = Details(
        id = 7, type = type, title = "Show", originalTitle = "Show", overview = "", tagline = "", date = "2020-05-01",
        poster = "/p.jpg", backdrop = "/b.jpg", vote = 7.5, runtime = if (type == MediaType.Movie) 120 else 0,
        episodeRunTime = 0, genres = emptyList(), cast = emptyList(), directors = emptyList(), creators = emptyList(),
        originalLanguage = "en", numberOfSeasons = seasons.size, seasons = seasons,
    )

    private fun progress(type: MediaType, s: Int = 0, e: Int = 0, pos: Double, dur: Double = 1000.0, h: String = "", t: String = "2026-01-01T00:00:00Z") =
        StreamProgress(profileId = 1, mediaType = type, tmdbId = 7, title = "Show", season = s, episode = e, hash = h, fileIndex = 2, position = pos, duration = dur, updatedAt = t)

    private val seasons = listOf(SeasonInfo(1, 3), SeasonInfo(2, 2))

    @Test
    fun movieWithoutProgressStartsFresh() {
        val plan = planOf(details(MediaType.Movie), emptyList())
        assertEquals(PlayPlan(null, null, false), plan)
        val r = details(MediaType.Movie).mainRequest(plan, emptyList(), null)
        assertEquals(2020, r.year)
        assertEquals(120, r.runtimeMinutes)
        assertNull(r.source)
        assertEquals(0.0, r.startAt, 0.0)
    }

    @Test
    fun movieResumesSavedSource() {
        val p = progress(MediaType.Movie, pos = 500.0, h = hash)
        val plan = planOf(details(MediaType.Movie), listOf(p))
        assertEquals(p, plan.resume)
        assertTrue(plan.saved)
        val r = details(MediaType.Movie).mainRequest(plan, listOf(p), null)
        assertEquals(SavedSource(hash, 2, null), r.source)
        assertEquals(495.0, r.startAt, 0.0)
        assertEquals("/p.jpg", r.posterPath)
        assertNull(details(MediaType.Movie).warmTarget(plan))
    }

    @Test
    fun finishedMovieIsNoResumeButReusesTheFile() {
        val p = progress(MediaType.Movie, pos = 950.0, h = hash)
        val plan = planOf(details(MediaType.Movie), listOf(p))
        assertNull(plan.resume)
        val r = details(MediaType.Movie).mainRequest(plan, listOf(p), null)
        assertEquals(0.0, r.startAt, 0.0)
        assertEquals(hash, r.source?.hash)
    }

    @Test
    fun showStartsAtFirstEpisode() {
        val d = details(MediaType.Tv, seasons)
        val plan = planOf(d, emptyList())
        assertEquals(EpisodeRef(1, 1), plan.ep)
        assertEquals(ResolveParams(MediaType.Tv, "Show", tmdbId = 7, season = 1, episode = 1), d.warmTarget(plan))
        val eps = listOf(EpisodeInfo(1, 1, "Pilot", "", "", 0, ""))
        val r = d.mainRequest(plan, emptyList(), eps)
        assertEquals("Pilot", r.episodeTitle)
        assertEquals(seasons, r.seasons)
        assertNull(r.runtimeMinutes)
    }

    @Test
    fun showGoesToTheEpisodeAfterAFinishedOne() {
        val d = details(MediaType.Tv, seasons)
        val entries = listOf(
            progress(MediaType.Tv, 1, 3, pos = 990.0, t = "2026-02-01T00:00:00Z"),
            progress(MediaType.Tv, 1, 1, pos = 300.0, t = "2026-01-01T00:00:00Z"),
        )
        val plan = planOf(d, entries)
        assertNull(plan.resume)
        assertEquals(EpisodeRef(2, 1), plan.ep)
        assertFalse(plan.saved)
    }

    @Test
    fun showResumesNewestUnfinishedEpisode() {
        val d = details(MediaType.Tv, seasons)
        val entries = listOf(progress(MediaType.Tv, 2, 1, pos = 100.0, h = hash).copy(episodeTitle = "Ep"))
        val plan = planOf(d, entries)
        assertEquals(EpisodeRef(2, 1), plan.ep)
        assertTrue(plan.saved)
        val r = d.mainRequest(plan, entries, null)
        assertEquals(2, r.season)
        assertEquals(1, r.episode)
        assertEquals("Ep", r.episodeTitle)
        assertEquals(95.0, r.startAt, 0.0)
    }

    @Test
    fun lastEpisodeFinishedStartsOver() {
        val d = details(MediaType.Tv, seasons)
        val plan = planOf(d, listOf(progress(MediaType.Tv, 2, 2, pos = 999.0)))
        assertEquals(EpisodeRef(1, 1), plan.ep)
    }

    @Test
    fun placeholderEpisodeNamesAreNotSent() {
        val d = details(MediaType.Tv, seasons)
        val r = d.episodeRequest(EpisodeInfo(3, 1, "Episode 3", "", "", 0, ""), emptyList())
        assertNull(r.episodeTitle)
        assertEquals(3, r.episode)
    }

    @Test
    fun cinemaRequestHasNoSource() {
        val r = details(MediaType.Movie).cinemaRequest()
        assertTrue(r.cinema)
        assertNull(r.source)
    }

    @Test
    fun unavailableTexts() {
        val en = coreStrings[AppLanguage.En]
        assertNull(unavailableText(null, en, AppLanguage.En))
        assertNull(unavailableText(Availability(AvailabilityStatus.Available), en, AppLanguage.En))
        assertEquals("Coming Soon", unavailableText(Availability(AvailabilityStatus.NotReleased), en, AppLanguage.En))
        assertEquals(
            "Coming November 5, 2026",
            unavailableText(Availability(AvailabilityStatus.NotReleased, releaseDate = "2026-11-05"), en, AppLanguage.En),
        )
        assertEquals("In theaters", unavailableText(Availability(AvailabilityStatus.InTheaters), en, AppLanguage.En))
        assertEquals(
            "In theaters · Streaming from December 1, 2026",
            unavailableText(Availability(AvailabilityStatus.InTheaters, digitalDate = "2026-12-01"), en, AppLanguage.En),
        )
    }

    @Test
    fun futureDates() {
        val today = LocalDate.of(2026, 10, 13)
        assertTrue(isFuture("2026-10-14", today))
        assertFalse(isFuture("2026-10-13", today))
        assertFalse(isFuture("", today))
        assertFalse(isFuture("bad", today))
    }

    @Test
    fun minutesLeftIsAtLeastOne() {
        assertEquals(1, minutesLeft(progress(MediaType.Movie, pos = 990.0)))
        assertEquals(10, minutesLeft(progress(MediaType.Movie, pos = 400.0)))
    }
}
