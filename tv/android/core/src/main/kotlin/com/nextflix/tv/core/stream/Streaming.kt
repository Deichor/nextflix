package com.nextflix.tv.core.stream

import androidx.compose.runtime.Immutable
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import android.os.SystemClock
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import kotlin.math.roundToInt

/**
 * What this TV plays without conversion (docs/API.md "Playback capabilities"). Sent with every streaming call
 * of a title: the server ranks sources for it and it is part of its source cache key.
 */
@Immutable
data class PlaybackCaps(
    /** HEVC Main / Main10: 4K / 10-bit HEVC is copied instead of converted */
    val hevc: Boolean = false,
    /** HDR display (only with hevc) */
    val hdr: Boolean = false,
    /** Dolby Vision decoder and display (only with hevc) */
    val dv: Boolean = false,
    /** Atmos (E-AC-3 JOC) reaches the speakers (only with eac3) */
    val atmos: Boolean = false,
    /** audio codecs played muxed with video (server / ffprobe names: aac, opus, ac3, eac3, ...); /play copies these */
    val audio: List<String> = listOf("aac"),
    /** audio codecs played only as a separate stream (split mode, /play?track=audio); usually empty on Android */
    val splitAudio: List<String> = emptyList(),
) {
    /**
     * Query entries. [stream] false (resolve, prefetch, release, sidegrade, upgrade, subalt): every codec in acodecs.
     * [stream] true (probe, seekpoint, play): the muxed ones in acodecs, split-only ones in split_acodecs.
     */
    fun query(stream: Boolean): Map<String, String> = buildMap {
        if (hevc) put("hevc", "1")
        if (hevc && hdr) put("hdr", "1")
        if (hevc && dv) put("dv", "1")
        if (atmos) put("atmos", "1")
        if (!stream) {
            put("acodecs", (audio + splitAudio).joinToString(","))
        } else {
            put("acodecs", audio.joinToString(","))
            if (splitAudio.isNotEmpty()) put("split_acodecs", splitAudio.joinToString(","))
        }
    }
}

/**
 * Detects [PlaybackCaps]. Implemented in :feature:player (MediaCodec / display / audio checks) and handed to
 * Core by the app, so Detail (prefetch) and Settings (shows them) can use it too.
 */
interface PlaybackCapsProvider {
    /** Current caps (cheap after the first call; honours DevicePrefs.hdrMode). */
    fun current(): PlaybackCaps

    /** Forget cached caps (HDR setting changed, a stream the TV could not take). */
    fun invalidate()

    /**
     * The TV refused audio it claimed to play: from now on claim only the safe codecs (AAC / Opus).
     * True when the caps changed (the player then restarts the stream).
     */
    fun restrictToSafeAudio(): Boolean = false

    /** Fixed caps, for previews and tests. */
    class Fixed(private val caps: PlaybackCaps = PlaybackCaps()) : PlaybackCapsProvider {
        override fun current() = caps
        override fun invalidate() = Unit
    }
}

enum class QualityCap(val height: Int) { P1080(1080), P720(720) }

/** The title part of every resolve-family query (webOS lib/stream.ts ResolveParams). */
@Immutable
data class ResolveParams(
    val type: MediaType,
    val title: String,
    val tmdbId: Int? = null,
    val year: Int? = null,
    val season: Int? = null,
    val episode: Int? = null,
    /** minutes */
    val runtime: Int? = null,
    val maxq: QualityCap? = null,
    val cinema: Boolean = false,
) {
    /** type, title, year (movies), season / episode (shows), tmdb_id, runtime, maxq, cinema. */
    fun query(): Map<String, String> = buildMap {
        put("type", type.apiName)
        put("title", title.trim())
        if (type == MediaType.Movie && year != null && year > 0) put("year", year.toString())
        if (type == MediaType.Tv) {
            put("season", (season ?: 1).toString())
            put("episode", (episode ?: 1).toString())
        }
        if (tmdbId != null && tmdbId > 0) put("tmdb_id", tmdbId.toString())
        if (runtime != null && runtime > 0) put("runtime", runtime.toString())
        if (maxq != null) put("maxq", maxq.height.toString())
        if (cinema) put("cinema", "1")
    }
}

fun PlayRequest.resolveParams(maxq: QualityCap? = null) = ResolveParams(
    type = type,
    title = title,
    tmdbId = tmdbId,
    year = year,
    season = season,
    episode = episode,
    runtime = runtimeMinutes,
    maxq = maxq,
    cinema = cinema,
)

/**
 * Warm-up on intent (webOS prefetchResolve / releaseTitle): Detail prefetches what Play would start and releases
 * it after a grace when left; the player warms the next episode near the end and releases titles it leaves.
 */
class StreamWarmup internal constructor(
    private val api: ApiClient,
    private val caps: PlaybackCapsProvider,
    private val scope: CoroutineScope,
) {
    private val prefetched = ConcurrentHashMap<String, Long>()

    /** POST /api/torrent-stream/prefetch, deduplicated for 10 min per title. [next]: (position, duration) s of the playing episode. */
    fun prefetch(params: ResolveParams, next: Pair<Double, Double>? = null) {
        if (params.title.isBlank()) return
        val key = "${titleKey(params)}|${params.maxq ?: ""}"
        val last = prefetched[key]
        val now = SystemClock.elapsedRealtime()
        if (last != null && now - last < PREFETCH_TTL_MS) return
        prefetched[key] = now
        // Off the main thread: the first caps detection enumerates the codecs
        scope.launch {
            try {
                val query = params.query() + caps.current().query(stream = false) + buildMap {
                    if (next != null) {
                        put("next_pos", next.first.roundToInt().toString())
                        put("next_dur", next.second.roundToInt().toString())
                    }
                }
                val answer = api.call("POST", "/api/torrent-stream/prefetch", query)
                val s = answer.obj?.str("status")
                if (s == "busy" || s == "early" || s == "deferred") prefetched.remove(key)
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                prefetched.remove(key)
            }
        }
    }

    /** Lets the server drop the title's warmed torrents after [delaySec] (cancelled by any new intent). */
    fun release(params: ResolveParams, delaySec: Int = 0) {
        if (params.title.isBlank()) return
        val base = titleKey(params)
        prefetched.keys.removeAll { it.startsWith("$base|") }
        scope.launch {
            val query = params.copy(maxq = null).query() + caps.current().query(stream = false) +
                (if (delaySec > 0) mapOf("delay" to delaySec.toString()) else emptyMap())
            api.beacon("/api/torrent-stream/release", query)
        }
    }

    private fun titleKey(p: ResolveParams) = p.copy(maxq = null).query().entries.joinToString("&").lowercase()

    private companion object {
        const val PREFETCH_TTL_MS = 10 * 60 * 1000L
    }
}
