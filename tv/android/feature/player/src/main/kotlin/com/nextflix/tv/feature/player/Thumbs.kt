package com.nextflix.tv.feature.player

import android.graphics.BitmapFactory
import android.os.SystemClock
import android.util.LruCache
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import com.nextflix.tv.core.Core
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// Trick play thumbnails (webOS player/thumbs.ts): small JPEGs of the keyframe at or before a time on the server's
// 10 s grid (GET /api/torrent-stream/thumb). 204 / 504 (swarm busy or slow) are asked again later. The JPEG bytes
// are kept (small); only the few frames on screen are decoded.

internal const val THUMB_GRID = 10
private const val THUMB_W = 480
private const val MAX_CACHED = 150
private const val MAX_DECODED = 12
private const val PARALLEL = 2
private const val RETRY_MS = 2000L
private const val GIVE_UP_MS = 60_000L

internal fun gridT(t: Double): Int = maxOf(0, (t / THUMB_GRID).toInt() * THUMB_GRID)

/** Thumbnails of the files one player plays; [version] grows when a frame arrives. */
internal class Thumbs(private val core: Core, private val scope: CoroutineScope) {
    val version = mutableIntStateOf(0)

    private val jpegs = LruCache<String, ByteArray>(MAX_CACHED)
    private val decoded = LruCache<String, ImageBitmap>(MAX_DECODED)
    private val blocked = HashMap<String, Long>()
    private val inflight = HashMap<String, Job>()
    private var wanted: List<Pair<String, Triple<String, Int, Int>>> = emptyList()
    private var retry: Job? = null

    private fun key(hash: String, index: Int, t: Int) = "$hash:$index:$t"

    /** The frame for t when loaded (decoded on first use). */
    fun frame(hash: String, index: Int, t: Double): ImageBitmap? {
        val k = key(hash, index, gridT(t))
        decoded[k]?.let { return it }
        val bytes = jpegs[k] ?: return null
        val bitmap = BitmapFactory.decodeByteArray(bytes, 0, bytes.size)?.asImageBitmap() ?: return null
        decoded.put(k, bitmap)
        return bitmap
    }

    /** Frames to load, most important first. Requests already running are kept (their frames are cached anyway). */
    fun want(hash: String, index: Int, times: List<Double>) {
        wanted = times.map { gridT(it) }.distinct().map { key(hash, index, it) to Triple(hash, index, it) }
        pump()
    }

    fun clearWanted() {
        wanted = emptyList()
        retry?.cancel()
    }

    /** Asks the server to make the frames around the position in the background (only while the stream runs steadily). */
    fun prefetch(hash: String, index: Int, times: List<Int>) {
        val ts = times.map { gridT(it.toDouble()) }.filter { jpegs[key(hash, index, it)] == null }
        if (ts.isEmpty()) return
        core.api.beacon(
            "/api/torrent-stream/thumb/prefetch",
            mapOf("hash" to hash, "index" to index, "t" to ts.joinToString(","), "w" to THUMB_W),
        )
    }

    private fun pump() {
        val now = SystemClock.elapsedRealtime()
        var wake = 0L
        for ((k, f) in wanted) {
            if (inflight.size >= PARALLEL) break
            if (jpegs[k] != null || k in inflight) continue
            val until = blocked[k] ?: 0L
            if (until > now) {
                wake = if (wake == 0L) until else minOf(wake, until)
                continue
            }
            inflight[k] = scope.launch { load(k, f.first, f.second, f.third) }
        }
        retry?.cancel()
        if (wake != 0L) retry = scope.launch {
            delay(maxOf(200L, wake - now))
            pump()
        }
    }

    private suspend fun load(k: String, hash: String, index: Int, t: Int) {
        try {
            val res = core.api.raw("GET", "/api/torrent-stream/thumb", mapOf("hash" to hash, "index" to index, "t" to t, "w" to THUMB_W))
            val (code, bytes, retryAfter) = withContext(Dispatchers.IO) {
                res.use { Triple(it.code, if (it.code == 200) it.body.bytes() else null, it.header("Retry-After")) }
            }
            if (code == 200 && bytes != null && bytes.isNotEmpty()) {
                jpegs.put(k, bytes)
                version.intValue++
            } else if (code != 200) {
                // 204: the stream starts or another torrent plays; 504: the swarm is slow. Others will not change soon.
                val after = retryAfter?.toLongOrNull()?.times(1000)?.takeIf { it > 0 } ?: RETRY_MS
                blocked[k] = SystemClock.elapsedRealtime() + if (code == 204 || code == 504) after else GIVE_UP_MS
            }
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (_: Exception) {
            blocked[k] = SystemClock.elapsedRealtime() + RETRY_MS
        } finally {
            inflight.remove(k)
        }
        pump()
    }
}
