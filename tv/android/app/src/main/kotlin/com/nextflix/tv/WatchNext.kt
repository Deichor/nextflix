package com.nextflix.tv

import android.content.Context
import android.content.pm.PackageManager
import android.net.Uri
import android.util.Log
import androidx.tvprovider.media.tv.PreviewChannelHelper
import androidx.tvprovider.media.tv.TvContractCompat
import androidx.tvprovider.media.tv.WatchNextProgram
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.net.TmdbImages
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import java.time.OffsetDateTime

/**
 * Feeds the Android TV / Google TV home screen's "Continue watching" (Watch Next) row from the active profile's
 * Continue Watching list. Cards open the player (resume, or the next episode) through [AppLink.Open].
 * Synced a while after the profile changed or progress was saved, and when the app goes to the background.
 * A card the viewer removed from the row stays hidden until that title is watched again.
 */
class WatchNext(private val context: Context, private val core: Core) {
    private val enabled = context.packageManager.hasSystemFeature(PackageManager.FEATURE_LEANBACK)
    private val lock = Mutex()
    private var pending: Job? = null

    @OptIn(FlowPreview::class)
    fun start() {
        if (!enabled) return
        core.scope.launch {
            // Waits until Home has loaded (it fills the shared Continue Watching cache)
            core.session.state.map { it?.server to it?.profile?.id }.distinctUntilChanged()
                .debounce(SESSION_DEBOUNCE_MS)
                .collect { syncNow() }
        }
        core.scope.launch {
            core.progress.version.drop(1).debounce(PROGRESS_DEBOUNCE_MS).collect { syncNow() }
        }
    }

    fun syncNow() {
        if (!enabled) return
        val server = core.session.server ?: return
        val profileId = core.session.profileId ?: return
        pending?.cancel()
        pending = core.scope.launch {
            lock.withLock {
                try {
                    sync(server, profileId)
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    Log.w(TAG, "Watch Next sync failed", e)
                }
            }
        }
    }

    private suspend fun sync(server: String, profileId: Int) {
        val items = core.progress.continueWatchingCached(profileId).take(MAX_PROGRAMS)
        if (core.session.server != server || core.session.profileId != profileId) return
        val helper = PreviewChannelHelper(context)
        val resolver = context.contentResolver
        val existing = buildMap {
            resolver.query(TvContractCompat.WatchNextPrograms.CONTENT_URI, WatchNextProgram.PROJECTION, null, null, null)?.use { c ->
                while (c.moveToNext()) {
                    val program = WatchNextProgram.fromCursor(c)
                    program.internalProviderId?.let { put(it, program) }
                }
            }
        }
        val wanted = items.associateBy { providerId(profileId, it) }

        for ((key, program) in existing) {
            if (key !in wanted) resolver.delete(TvContractCompat.buildWatchNextProgramUri(program.id), null, null)
        }
        for ((key, progress) in wanted) {
            val old = existing[key]
            val engaged = engagedAt(progress)
            if (old != null && !old.isBrowsable) {
                // Removed by the viewer: only a newer watch brings it back
                if (engaged <= old.lastEngagementTimeUtcMillis) continue
                resolver.delete(TvContractCompat.buildWatchNextProgramUri(old.id), null, null)
                helper.publishWatchNextProgram(program(profileId, key, progress, engaged))
            } else if (old != null) {
                helper.updateWatchNextProgram(program(profileId, key, progress, engaged), old.id)
            } else {
                helper.publishWatchNextProgram(program(profileId, key, progress, engaged))
            }
        }
    }

    private fun program(profileId: Int, key: String, p: StreamProgress, engaged: Long): WatchNextProgram {
        val episode = p.mediaType == MediaType.Tv && p.season > 0 && p.episode > 0
        val backdrop = core.imageUrl(p.backdropPath.ifEmpty { null }, TmdbImages.W780)
        val poster = core.imageUrl(p.posterPath.ifEmpty { null }, TmdbImages.W500)
        val link = AppLink.Open(profileId, p.mediaType, p.tmdbId, p.season, p.episode, play = true)
        return WatchNextProgram.Builder().apply {
            setInternalProviderId(key)
            setContentId(p.key)
            setType(if (episode) TvContractCompat.WatchNextPrograms.TYPE_TV_EPISODE else TvContractCompat.WatchNextPrograms.TYPE_MOVIE)
            setWatchNextType(
                if (p.position > 0) TvContractCompat.WatchNextPrograms.WATCH_NEXT_TYPE_CONTINUE
                else TvContractCompat.WatchNextPrograms.WATCH_NEXT_TYPE_NEXT,
            )
            setLastEngagementTimeUtcMillis(engaged)
            setTitle(p.title)
            if (episode) {
                setSeasonNumber(p.season)
                setEpisodeNumber(p.episode)
                p.episodeTitle.takeIf { it.isNotBlank() }?.let(::setEpisodeTitle)
            }
            if (p.duration > 0) {
                setDurationMillis(millis(p.duration))
                setLastPlaybackPositionMillis(millis(p.position))
            }
            when {
                backdrop.isNotEmpty() -> {
                    setPosterArtUri(Uri.parse(backdrop))
                    setPosterArtAspectRatio(TvContractCompat.WatchNextPrograms.ASPECT_RATIO_16_9)
                }
                poster.isNotEmpty() -> {
                    setPosterArtUri(Uri.parse(poster))
                    setPosterArtAspectRatio(TvContractCompat.WatchNextPrograms.ASPECT_RATIO_MOVIE_POSTER)
                }
            }
            setIntent(link.intent(context))
        }.build()
    }

    private fun providerId(profileId: Int, p: StreamProgress) = "p$profileId:${p.key}"

    private fun engagedAt(p: StreamProgress): Long =
        p.updatedAt?.let { runCatching { OffsetDateTime.parse(it).toInstant().toEpochMilli() }.getOrNull() }
            ?: System.currentTimeMillis()

    private fun millis(seconds: Double): Int = (seconds * 1000).toLong().coerceIn(0, Int.MAX_VALUE.toLong()).toInt()

    private companion object {
        const val TAG = "Nextflix"
        const val MAX_PROGRAMS = 12
        const val PROGRESS_DEBOUNCE_MS = 30_000L
        const val SESSION_DEBOUNCE_MS = 15_000L
    }
}
