package com.nextflix.tv.core.model

import androidx.compose.runtime.Immutable
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.nav.SavedSource
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Saved playback position (table stream_progress, docs/API.md "Continue watching"). Movies use season 0 and
 * episode 0. [hash] / [fileIndex] / [magnet] name the torrent file that played, so a resume can reuse it.
 */
@Immutable
@Serializable
data class StreamProgress(
    val id: Long? = null,
    @SerialName("profile_id") val profileId: Int,
    @SerialName("media_type") val mediaType: MediaType,
    @SerialName("tmdb_id") val tmdbId: Int,
    val title: String = "",
    @SerialName("poster_path") val posterPath: String = "",
    @SerialName("backdrop_path") val backdropPath: String = "",
    val season: Int = 0,
    val episode: Int = 0,
    @SerialName("episode_title") val episodeTitle: String = "",
    val magnet: String = "",
    val hash: String = "",
    @SerialName("file_index") val fileIndex: Int = 0,
    /** seconds */
    val position: Double = 0.0,
    /** seconds */
    val duration: Double = 0.0,
    @SerialName("updated_at") val updatedAt: String? = null,
) {
    val key: String get() = "${mediaType.apiName}-$tmdbId"

    /** >= 92 % watched, like the server */
    val isFinished: Boolean get() = duration > 0 && position >= duration * FINISHED_RATIO

    /** 0..1 */
    val fraction: Float get() = if (duration > 0) (position / duration).toFloat().coerceIn(0f, 1f) else 0f

    /** Seconds left (0 when unknown). */
    val remaining: Double get() = (duration - position).coerceAtLeast(0.0)

    companion object {
        const val FINISHED_RATIO = 0.92
    }
}

/** The entry of one episode (movies: season 0, episode 0). */
fun List<StreamProgress>.find(season: Int = 0, episode: Int = 0): StreamProgress? =
    firstOrNull { it.season == season && it.episode == episode }

/** The episode after season/episode in [seasons], or null at the end of the show. */
fun nextEpisodeOf(seasons: List<SeasonInfo>, season: Int, episode: Int): Pair<Int, Int>? {
    val current = seasons.firstOrNull { it.seasonNumber == season }
    if (current != null && episode < current.episodeCount) return season to episode + 1
    val next = seasons.firstOrNull { it.seasonNumber > season && it.episodeCount > 0 } ?: return null
    return next.seasonNumber to 1
}

/** Continue Watching / resume: a player request that reuses the saved torrent and starts 5 s before the position. */
fun StreamProgress.toPlayRequest(): PlayRequest = PlayRequest(
    type = mediaType,
    tmdbId = tmdbId,
    title = title,
    posterPath = posterPath.ifEmpty { null },
    backdropPath = backdropPath.ifEmpty { null },
    season = season.takeIf { it > 0 },
    episode = episode.takeIf { it > 0 },
    episodeTitle = episodeTitle.ifEmpty { null },
    startAt = if (isFinished) 0.0 else (position - 5).coerceAtLeast(0.0),
    source = if (hash.isNotEmpty()) SavedSource(hash = hash, index = fileIndex, magnet = magnet.ifEmpty { null }) else null,
)
