package com.nextflix.tv.feature.detail

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.CoreStrings
import com.nextflix.tv.core.i18n.Formats
import com.nextflix.tv.core.model.Availability
import com.nextflix.tv.core.model.AvailabilityStatus
import com.nextflix.tv.core.model.Details
import com.nextflix.tv.core.model.EpisodeInfo
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.find
import com.nextflix.tv.core.model.nextEpisodeOf
import com.nextflix.tv.core.model.toPlayRequest
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.stream.ResolveParams
import java.time.LocalDate
import kotlin.math.roundToInt

internal data class EpisodeRef(val season: Int, val episode: Int)

/**
 * What Play starts: [resume] the newest unfinished entry, [ep] the episode Play opens (shows), [saved] whether
 * that target has a saved torrent (then it is reused and not warmed up).
 */
internal data class PlayPlan(val resume: StreamProgress?, val ep: EpisodeRef?, val saved: Boolean)

/** [entries]: every progress entry of the title, newest first. */
internal fun planOf(d: Details, entries: List<StreamProgress>): PlayPlan {
    if (d.type == MediaType.Movie) {
        val p = entries.find()
        return PlayPlan(resume = p?.takeIf { !it.isFinished && it.position > 0 }, ep = null, saved = !p?.hash.isNullOrEmpty())
    }
    val latest = entries.firstOrNull()
    val resume = latest?.takeIf { !it.isFinished }
    val next = latest?.takeIf { it.isFinished }
        ?.let { nextEpisodeOf(d.seasons, it.season, it.episode) }
        ?.let { (s, e) -> EpisodeRef(s, e) }
    val ep = resume?.let { EpisodeRef(it.season, it.episode) }
        ?: next
        ?: d.seasons.firstOrNull()?.let { EpisodeRef(it.seasonNumber, 1) }
    val saved = ep != null && !entries.find(ep.season, ep.episode)?.hash.isNullOrEmpty()
    return PlayPlan(resume, ep, saved)
}

/** The title part of every player request (webOS metaOf). */
internal fun Details.baseRequest(): PlayRequest = PlayRequest(
    type = type,
    tmdbId = id,
    title = title,
    year = year,
    runtimeMinutes = if (type == MediaType.Movie) runtime.takeIf { it > 0 } else null,
    posterPath = poster.ifEmpty { null },
    backdropPath = backdrop.ifEmpty { null },
    originalLanguage = originalLanguage,
    seasons = seasons,
)

/**
 * A request for the movie or one episode. A [saved] entry with a torrent is reused from 5 s before its position
 * (what the webOS player looked up itself when it was not given a source).
 */
internal fun Details.requestFor(season: Int?, episode: Int?, saved: StreamProgress?, episodeTitle: String?): PlayRequest {
    val base = baseRequest().copy(season = season, episode = episode, episodeTitle = episodeTitle?.ifEmpty { null })
    if (saved == null || saved.hash.isEmpty()) return base
    val resume = saved.toPlayRequest()
    return base.copy(
        episodeTitle = resume.episodeTitle ?: base.episodeTitle,
        posterPath = base.posterPath ?: resume.posterPath,
        backdropPath = base.backdropPath ?: resume.backdropPath,
        startAt = resume.startAt,
        source = resume.source,
    )
}

/** Play / Resume of the page. [episodes]: the loaded episodes of the plan's season, for the episode title. */
internal fun Details.mainRequest(plan: PlayPlan, entries: List<StreamProgress>, episodes: List<EpisodeInfo>?): PlayRequest {
    val resume = plan.resume
    if (resume != null && resume.hash.isNotEmpty()) {
        return requestFor(resume.season.takeIf { it > 0 }, resume.episode.takeIf { it > 0 }, resume, null)
    }
    if (type == MediaType.Tv) {
        val ep = plan.ep ?: EpisodeRef(1, 1)
        val name = episodes?.firstOrNull { it.seasonNumber == ep.season && it.episodeNumber == ep.episode }?.takeIf { it.hasRealName }?.name
        return requestFor(ep.season, ep.episode, entries.find(ep.season, ep.episode), name)
    }
    return requestFor(null, null, entries.find(), null)
}

internal fun Details.episodeRequest(ep: EpisodeInfo, entries: List<StreamProgress>): PlayRequest =
    requestFor(ep.seasonNumber, ep.episodeNumber, entries.find(ep.seasonNumber, ep.episodeNumber), ep.name.takeIf { ep.hasRealName })

/** "Watch the cinema recording anyway". */
internal fun Details.cinemaRequest(): PlayRequest = baseRequest().copy(cinema = true)

/** What the page warms up on open; null when the target has a saved torrent or there is none. */
internal fun Details.warmTarget(plan: PlayPlan): ResolveParams? {
    if (plan.saved) return null
    return when (type) {
        MediaType.Movie -> ResolveParams(MediaType.Movie, title, tmdbId = id, year = year, runtime = runtime.takeIf { it > 0 })
        MediaType.Tv -> plan.ep?.let { ResolveParams(MediaType.Tv, title, tmdbId = id, season = it.season, episode = it.episode) }
    }
}

/** Text of the disabled Play button for a title that cannot be streamed yet, else null. */
internal fun unavailableText(a: Availability?, s: CoreStrings, language: AppLanguage): String? = when (a?.status) {
    AvailabilityStatus.NotReleased -> {
        val date = a.releaseDate?.takeIf { it.isNotBlank() } ?: a.digitalDate?.takeIf { it.isNotBlank() }
        if (date != null) s.comingOn(Formats.date(language, date)) else s.comingSoon
    }
    AvailabilityStatus.InTheaters -> {
        val digital = a.digitalDate?.takeIf { it.isNotBlank() }
        if (digital != null) s.inTheatersUntil(Formats.date(language, digital)) else s.inTheaters
    }
    else -> null
}

/** Minutes left of a resume entry, at least 1. */
internal fun minutesLeft(p: StreamProgress): Int = maxOf(1, ((p.duration - p.position) / 60).roundToInt())

/** An air date after today. */
internal fun isFuture(date: String, today: LocalDate = LocalDate.now()): Boolean =
    date.isNotEmpty() && runCatching { LocalDate.parse(date.take(10)).isAfter(today) }.getOrDefault(false)
