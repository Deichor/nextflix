package com.nextflix.tv.feature.player

import com.nextflix.tv.core.Core
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.data.toAvailability
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.nav.SavedSource
import com.nextflix.tv.core.net.ApiException
import com.nextflix.tv.core.net.bool
import com.nextflix.tv.core.net.dbl
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import com.nextflix.tv.core.stream.ResolveParams
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.JsonElement
import java.util.Locale
import kotlin.math.roundToInt

/**
 * The streaming endpoints the player calls (webOS lib/stream.ts). Every call carries the device's playback caps
 * (docs/API.md "Playback capabilities"): resolve-family calls with every codec, probe / seekpoint / play with the
 * stream form, always the same set.
 */
internal class StreamApi(private val core: Core) {
    private val api get() = core.api

    private fun caps(stream: Boolean): Map<String, String> = core.playbackCaps.current().query(stream)

    private fun resolveQuery(p: ResolveParams): Map<String, String> = p.query() + caps(stream = false)

    /** GET /resolve; throws [StreamError] (425 carries the availability). */
    suspend fun resolve(p: ResolveParams, refresh: Boolean, avoid: List<String>, t: Double): ResolvedSource {
        val q = resolveQuery(p) + buildMap {
            if (refresh) put("refresh", "1")
            if (avoid.isNotEmpty()) put("avoid", avoid.joinToString(","))
            if (t > 0) put("t", t.roundToInt().toString())
        }
        val answer = try {
            api.getJson("/api/torrent-stream/resolve", q)
        } catch (e: ApiException.Http) {
            val availability = if (e.status == 425) e.body.toAvailability() else null
            throw StreamError(e.status, e.message.orEmpty(), availability)
        } catch (e: ApiException) {
            throw StreamError(0, e.message.orEmpty())
        }
        return ResolvedSource.parse(answer) ?: throw StreamError(404, "")
    }

    /** GET /reuse for a saved source; null when the server could not tell (the saved source plays as it is). */
    suspend fun reuse(p: ResolveParams, saved: SavedSource, t: Double): ReuseAnswer? = quiet {
        val q = resolveQuery(p) + buildMap {
            put("hash", saved.hash.lowercase())
            put("index", saved.index.toString())
            saved.magnet?.let { put("magnet", it) }
            if (t > 0) put("t", t.roundToInt().toString())
        }
        val o = api.getJson("/api/torrent-stream/reuse", q).obj ?: return@quiet null
        when (o.str("action")) {
            "resolve" -> ReuseAnswer(play = false, reason = o.str("reason"), source = null)
            "play" -> ResolvedSource.parse(o["source"])?.let { ReuseAnswer(play = true, reason = null, source = it) }
            else -> null
        }
    }

    /** /sidegrade, /upgrade: a source on 200, null on 204 or failure. */
    private suspend fun optionalSource(path: String, p: ResolveParams, current: String, quality: Int, extra: Map<String, String> = emptyMap()) =
        quiet {
            val q = resolveQuery(p) + buildMap {
                put("current", current)
                if (quality > 0) put("current_q", quality.toString())
                putAll(extra)
            }
            val (code, body, _) = fetch(path, q)
            if (code == 200) ResolvedSource.parse(parse(body)) else null
        }

    /** The source Play would use when the server already found it (resolve?cached=1: never searches or holds), else null. */
    suspend fun cached(p: ResolveParams): ResolvedSource? = quiet {
        val (code, body, _) = fetch("/api/torrent-stream/resolve", resolveQuery(p) + ("cached" to "1"))
        if (code == 200) ResolvedSource.parse(parse(body)) else null
    }

    /** A higher resolution the server warmed up that keeps up now. */
    suspend fun upgrade(p: ResolveParams, current: String, quality: Int) =
        optionalSource("/api/torrent-stream/upgrade", p, current, quality)

    /** Another source of the same quality that keeps up now. [state]: "draining" / "critical" (buffer verdict). */
    suspend fun sidegrade(p: ResolveParams, current: String, quality: Int, state: String?, avoid: Collection<String>) =
        optionalSource(
            "/api/torrent-stream/sidegrade", p, current, quality,
            buildMap {
                if (state != null) put(state, "1")
                if (avoid.isNotEmpty()) put("avoid", avoid.toList().takeLast(5).joinToString(","))
            },
        )

    /** A release whose subtitles include [lang]; [retryAfterS] while the server scans other releases. */
    suspend fun subAlt(p: ResolveParams, current: String, quality: Int, lang: String): Pair<ResolvedSource?, Int?> =
        quiet {
            val q = resolveQuery(p) + buildMap {
                put("current", current)
                put("lang", lang)
                if (quality > 0) put("current_q", quality.toString())
            }
            val (code, body, retryAfter) = fetch("/api/torrent-stream/subalt", q)
            if (code == 200) ResolvedSource.parse(parse(body)) to null else null to retryAfter?.toIntOrNull()?.takeIf { it > 0 }
        } ?: (null to null)

    /** Adds a saved torrent to TorrServer again (it may have been dropped since). */
    suspend fun files(magnet: String) {
        quiet { fetch("/api/torrent-stream/files", mapOf("magnet" to magnet)) }
    }

    /** GET /probe with the length check of the title; throws on failure. */
    suspend fun probe(hash: String, index: Int, meta: PlayRequest): ProbeInfo {
        val q = mapOf("hash" to hash, "index" to index.toString()) + caps(stream = true) + runtimeCheck(meta)
        return ProbeInfo.parse(api.getJson("/api/torrent-stream/probe", q))
    }

    /** File time of the stream's time 0 for a stream asked for at [t]; null when unknown (not exact, failed). */
    suspend fun seekPoint(hash: String, index: Int, t: Double, audio: Int, timeoutMs: Long): Double? = quiet {
        withTimeoutOrNull(timeoutMs) {
            val q = mapOf("hash" to hash, "index" to index.toString(), "t" to fixed(t), "audio" to audio.toString()) + caps(stream = true)
            val o = api.getJson("/api/torrent-stream/seekpoint", q).obj ?: return@withTimeoutOrNull null
            val at = o.dbl("t") ?: return@withTimeoutOrNull null
            at.takeIf { o.bool("exact") != false && it >= -1 && it <= t + 1 }
        }
    }

    /** /play: a live fragmented MP4 from [t] with audio track [audio]. */
    fun playUrl(hash: String, index: Int, t: Double, audio: Int): String =
        api.url(
            "/api/torrent-stream/play",
            mapOf("hash" to hash, "index" to index, "t" to fixed(t), "audio" to audio) + caps(stream = true),
        ).toString()

    /** A paused player keeps its file ready on the server. */
    fun keepAlive(hash: String, index: Int) {
        if (HASH.matches(hash)) api.beacon("/api/torrent-stream/keepalive", mapOf("hash" to hash.lowercase(), "index" to index))
    }

    /** Drops torrents this player used (the server never drops one that is playing again). */
    fun releaseTorrents(hashes: Collection<String>) {
        val list = hashes.map { it.lowercase() }.filter { HASH.matches(it) }.distinct().take(10)
        if (list.isNotEmpty()) api.beacon("/api/torrent-stream/release", mapOf("hash" to list.joinToString(",")))
    }

    /** GET /skip-segments of the playing episode file. */
    suspend fun skipSegments(meta: PlayRequest, hash: String, index: Int, duration: Double): SkipSegments? = quiet {
        val q = mapOf(
            "tmdb_id" to meta.tmdbId, "season" to meta.season, "episode" to meta.episode,
            "hash" to hash, "index" to index, "duration" to "%.1f".format(Locale.US, duration),
        )
        SkipSegments.parse(api.getJson("/api/skip-segments", q))
    }

    private fun runtimeCheck(m: PlayRequest): Map<String, String> {
        if (m.tmdbId <= 0) return emptyMap()
        return buildMap {
            put("type", m.type.apiName)
            put("tmdb_id", m.tmdbId.toString())
            if (m.type == MediaType.Tv) {
                put("season", (m.season ?: 1).toString())
                put("episode", (m.episode ?: 1).toString())
            }
            m.runtimeMinutes?.takeIf { it > 0 }?.let { put("runtime", it.toString()) }
        }
    }

    /** Status, body and Retry-After of a GET whose status matters (204 answers). */
    private suspend fun fetch(path: String, query: Map<String, String>): Triple<Int, String, String?> {
        val res = api.raw("GET", path, query)
        return withContext(Dispatchers.IO) {
            res.use { Triple(it.code, it.body.string(), it.header("Retry-After")) }
        }
    }

    private fun parse(text: String): JsonElement? = runCatching { api.json.parseToJsonElement(text) }.getOrNull()

    private companion object {
        val HASH = Regex("^[0-9a-fA-F]{40}$")

        fun fixed(t: Double) = "%.3f".format(Locale.US, t)
    }
}

/** Runs [block]; null on any failure except cancellation (best-effort calls). */
internal suspend fun <T> quiet(block: suspend () -> T?): T? =
    try {
        block()
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) {
        null
    }
