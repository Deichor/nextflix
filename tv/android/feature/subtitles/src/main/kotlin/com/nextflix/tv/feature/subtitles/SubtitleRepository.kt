package com.nextflix.tv.feature.subtitles

import com.nextflix.tv.core.Core
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.net.ApiException
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import java.io.IOException
import kotlin.math.floor

/**
 * Subtitle data from the server (webOS lib/subtitles.ts). Owned by :feature:subtitles; used by
 * [SubtitleController] (and the player only through the controller).
 */
class SubtitleRepository(private val core: Core) {
    @Serializable
    private data class ListAnswer(val items: List<SubtitleTrack?> = emptyList())

    // Parsed complete files, least recently used first (a few only: they can be large)
    private val cueCache = object : LinkedHashMap<String, CueFile>(8, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, CueFile>) = size > CUE_CACHE_MAX
    }

    /**
     * GET /api/subtitles for the playing file. [refresh]: ask the providers again, skipping the server caches
     * (late uploads; the server limits how often). Returns [] on failure; items without id are dropped.
     */
    suspend fun list(query: SubtitleQuery, refresh: Boolean = false): List<SubtitleTrack> {
        val params = buildMap<String, Any?> {
            put("tmdb_id", query.tmdbId)
            put("media_type", query.type.apiName)
            put("hash", query.hash)
            put("index", query.index)
            put("langs", query.langs.joinToString(","))
            if (query.type == MediaType.Tv) {
                put("season", query.season ?: 1)
                put("episode", query.episode ?: 1)
            }
            query.title?.takeIf { it.isNotEmpty() }?.let { put("title", it) }
            query.year?.takeIf { it > 0 }?.let { put("year", it) }
            query.uiLang?.takeIf { it.isNotEmpty() }?.let { put("ui_lang", it) }
            if (refresh) put("refresh", 1)
        }
        return try {
            core.api.get<ListAnswer>("/api/subtitles", params).items.filterNotNull().filter { it.id.isNotEmpty() }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            emptyList()
        }
    }

    /**
     * GET /api/subtitles/file?id=[&t=] parsed from WebVTT (cue text kept as sent; SubtitleOverlay reads its
     * <i>, <b>, <u> markup and entities). [at]: the viewer's position (s) so a live embedded track is read from
     * there. Recognizes the "NOTE nextflix-partial" / "NOTE nextflix-ai-translating" markers, X-Subtitle-Sync and
     * X-Subtitle-Holes. Complete files (not partial) are kept in a small LRU (5 files); 5xx / network errors give
     * `failed = true`.
     */
    suspend fun cues(id: String, at: Double? = null): CueFile {
        synchronized(cueCache) { cueCache[id] }?.let { return it }
        val t = at?.takeIf { it > 0 }?.let { floor(it).toLong() }
        val file = try {
            val res = core.api.raw("GET", "/api/subtitles/file", mapOf("id" to id, "t" to t))
            // Read and closed off the main thread (closing an unread body may touch the socket), even when cancelled
            val (code, text) = withContext(NonCancellable + Dispatchers.IO) {
                res.use { it.code to if (it.isSuccessful) it.body.string() else "" }
            }
            val cues = if (text.isEmpty()) emptyList() else withContext(Dispatchers.Default) { Vtt.parse(text) }
            CueFile(
                cues = cues,
                partial = Vtt.PARTIAL_NOTE in text,
                sync = SubsLog.syncValue(res.header("X-Subtitle-Sync")),
                translating = Vtt.AI_NOTE in text,
                failed = code >= 500,
                status = code,
                holes = res.header("X-Subtitle-Holes").orEmpty(),
            )
        } catch (e: CancellationException) {
            throw e
        } catch (e: ApiException) {
            CueFile(emptyList(), sync = SubsLog.SYNC_NONE, failed = true, status = 0)
        } catch (e: IOException) {
            CueFile(emptyList(), sync = SubsLog.SYNC_NONE, failed = true, status = 0)
        } catch (e: RuntimeException) {
            CueFile(emptyList(), sync = SubsLog.SYNC_NONE, failed = true, status = 0)
        }
        if (file.cues.isNotEmpty() && !file.partial) synchronized(cueCache) { cueCache[id] = file }
        return file
    }

    companion object {
        /** a live (partial) subtitle file is fetched again this often while it plays */
        const val LIVE_CUES_POLL_MS = 15_000L
        /** an AI translation still missing the lines at the position is asked again this often */
        const val AI_CUES_POLL_MS = 3_000L
        /** late uploads: while the profile's language is missing, the list is asked again (refresh) this often */
        const val LATE_SUBTITLE_POLL_MS = 10 * 60 * 1000L
        /** the list is asked again this long after the first answer when it lacks the profile's language */
        val EARLY_RETRIES_MS = listOf(8_000L, 20_000L)
        /** how often the player asks `subalt` again while the server scans (its Retry-After) */
        const val SUBALT_RETRY_MAX = 4

        private const val CUE_CACHE_MAX = 5
    }
}
