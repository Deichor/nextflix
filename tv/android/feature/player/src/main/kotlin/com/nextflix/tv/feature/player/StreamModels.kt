package com.nextflix.tv.feature.player

import androidx.compose.runtime.Immutable
import com.nextflix.tv.core.model.Availability
import com.nextflix.tv.core.nav.SavedSource
import com.nextflix.tv.core.net.arr
import com.nextflix.tv.core.net.bool
import com.nextflix.tv.core.net.dbl
import com.nextflix.tv.core.net.int
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject

// Answers of the streaming endpoints (docs/API.md "Streaming"), parsed loosely like the webOS app does.

/** Swarm health of a resolve answer: bytes/s delivered and needed. */
internal data class SourceHealth(val rate: Double, val need: Double, val keepsUp: Boolean, val dead: Boolean)

/** A resolve / reuse / sidegrade / upgrade / subalt answer. */
internal data class ResolvedSource(
    val hash: String,
    val index: Int,
    val magnet: String?,
    val fileName: String?,
    val torrentTitle: String?,
    val quality: String?,
    val hdr: Boolean,
    val health: SourceHealth?,
    val cached: Boolean,
    val atmos: Boolean,
    val debrid: Boolean,
    val cinema: Boolean,
) {
    fun toSource() = StreamSource(
        hash = hash,
        index = index,
        magnet = magnet,
        fileName = fileName,
        torrentTitle = torrentTitle,
        quality = quality,
        hdr = hdr,
        starving = health?.keepsUp == false,
        atmos = atmos,
        ratio = healthRatio(health),
        debrid = debrid,
        cinema = cinema,
    )

    companion object {
        fun parse(json: JsonElement?): ResolvedSource? {
            val o = json.obj ?: return null
            val hash = o.str("hash")?.takeIf { it.isNotEmpty() } ?: return null
            val health = o.obj("health")?.let {
                SourceHealth(
                    rate = it.dbl("rate") ?: 0.0,
                    need = it.dbl("need") ?: 0.0,
                    keepsUp = it.bool("keeps_up") ?: false,
                    dead = it.bool("dead") ?: false,
                )
            }
            return ResolvedSource(
                hash = hash,
                index = o.int("index") ?: 0,
                magnet = o.str("magnet")?.takeIf { it.isNotEmpty() },
                fileName = o.str("file_name"),
                torrentTitle = o.str("torrent_title"),
                quality = o.str("quality"),
                hdr = o.bool("hdr") ?: false,
                health = health,
                cached = o.bool("cached") ?: false,
                atmos = o.obj("format")?.bool("atmos") ?: false,
                debrid = o.bool("debrid") == true || o.str("source") == "torbox",
                cinema = o.bool("cinema") == true,
            )
        }
    }
}

/** The source the player plays (webOS StreamSource). */
@Immutable
internal data class StreamSource(
    val hash: String,
    val index: Int,
    val magnet: String? = null,
    val fileName: String? = null,
    val torrentTitle: String? = null,
    val quality: String? = null,
    val hdr: Boolean = false,
    /** the resolve measured it as not keeping up */
    val starving: Boolean = false,
    val atmos: Boolean = false,
    /** swarm rate / needed bitrate measured by the resolve; null: not measured */
    val ratio: Double? = null,
    /** plays from TorBox instead of the swarm */
    val debrid: Boolean = false,
    /** a cinema recording */
    val cinema: Boolean = false,
)

internal fun SavedSource.toSource() = StreamSource(hash = hash, index = index, magnet = magnet)

/** GET /reuse: play (with the source to play) or resolve again without the saved one. */
internal data class ReuseAnswer(val play: Boolean, val reason: String?, val source: ResolvedSource?)

/** One audio track of the probe answer. */
@Immutable
internal data class AudioTrack(
    val index: Int,
    val language: String,
    val title: String,
    val codec: String,
    val channels: Int,
    /** sent untouched to this client */
    val direct: Boolean = false,
    /** default disposition in the file */
    val default: Boolean = false,
)

internal data class VideoInfo(
    val codec: String,
    val width: Int,
    val height: Int,
    val hdr: Boolean,
    val dolbyVision: Int,
    val direct: Boolean,
)

/** GET /probe. */
internal data class ProbeInfo(
    val duration: Double,
    val video: VideoInfo?,
    val audioTracks: List<AudioTrack>,
    val durationMismatch: Boolean,
) {
    companion object {
        fun parse(json: JsonElement?): ProbeInfo {
            val o = json.obj ?: JsonObject(emptyMap())
            val width = o.int("width") ?: 0
            val video = if (width > 0) {
                VideoInfo(
                    codec = o.str("video_codec").orEmpty(),
                    width = width,
                    height = o.int("height") ?: 0,
                    hdr = o.bool("hdr") ?: false,
                    dolbyVision = o.int("dolby_vision") ?: 0,
                    direct = o.bool("direct") ?: false,
                )
            } else {
                null
            }
            val tracks = o.arr("audio_tracks").orEmpty().mapNotNull { raw ->
                val t = raw.obj ?: return@mapNotNull null
                AudioTrack(
                    index = t.int("index") ?: return@mapNotNull null,
                    language = t.str("language").orEmpty(),
                    title = t.str("title").orEmpty(),
                    codec = t.str("codec").orEmpty(),
                    channels = t.int("channels") ?: 0,
                    direct = t.bool("direct") ?: false,
                    default = t.bool("default") ?: false,
                )
            }
            return ProbeInfo(
                duration = o.dbl("duration") ?: 0.0,
                video = video,
                audioTracks = tracks,
                durationMismatch = o.bool("duration_mismatch") ?: false,
            )
        }
    }
}

/** Resolve failed: [status] 0 for network errors; [availability] set for 425. */
internal class StreamError(val status: Int, message: String, val availability: Availability? = null) : Exception(message)

/** Quality badges of the top bar. */
@Immutable
internal data class QualityBadge(val label: String, val hdr: Boolean, val dv: Boolean)

/** One intro / credits segment (seconds). */
@Immutable
internal data class SkipSegment(val start: Double, val end: Double)

@Immutable
internal data class SkipSegments(val intro: SkipSegment?, val credits: SkipSegment?) {
    companion object {
        fun parse(json: JsonElement?): SkipSegments? {
            val o = json.obj ?: return null
            fun seg(key: String) = o.obj(key)?.let { s ->
                val start = s.dbl("start")
                val end = s.dbl("end")
                if (start != null && end != null && end > start) SkipSegment(start, end) else null
            }
            return SkipSegments(seg("intro"), seg("credits"))
        }
    }
}

internal enum class SkipPhase { Intro, Credits }

/** Which skip button the position calls for. */
internal fun skipPhase(segs: SkipSegments?, position: Double, duration: Double): SkipPhase? {
    if (segs == null || duration <= 0) return null
    val i = segs.intro
    if (i != null && position >= i.start && position < i.end - 1) return SkipPhase.Intro
    val c = segs.credits
    if (c != null && position >= c.start && position < minOf(c.end, duration) - 1) return SkipPhase.Credits
    return null
}
