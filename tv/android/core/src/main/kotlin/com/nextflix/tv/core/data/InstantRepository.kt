package com.nextflix.tv.core.data

import android.os.SystemClock
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.bool
import com.nextflix.tv.core.net.obj
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.onCompletion
import kotlinx.coroutines.flow.onStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import java.util.concurrent.ConcurrentHashMap

/** A title asked for the instant-play hint (TV shows: the episode, default S01E01). */
data class InstantItem(
    val type: MediaType,
    val tmdbId: Int,
    val title: String? = null,
    val season: Int? = null,
    val episode: Int? = null,
) {
    val key: String get() = "${type.apiName}:$tmdbId"
}

/**
 * Instant-play hints (the bolt on cards: TorBox has a release cached), same rules as webOS
 * components/instant.ts: asks of ~150 ms are batched into one POST /api/torrent-stream/instant, unknown answers
 * are asked again after 20 s (twice) while someone still watches them, known answers are kept 10 min.
 */
class InstantRepository internal constructor(private val api: ApiClient, private val scope: CoroutineScope) {
    private val confined = Dispatchers.Default.limitedParallelism(1)
    private val states = ConcurrentHashMap<String, MutableStateFlow<Boolean?>>()

    // Confined to `confined`
    private val answeredAt = HashMap<String, Long>()
    private val want = LinkedHashMap<String, InstantItem>()
    private val inflight = HashSet<String>()
    private val rounds = HashMap<String, Int>()
    private val retryItems = LinkedHashMap<String, InstantItem>()
    private val watchers = HashMap<String, Int>()
    private var flushJob: Job? = null
    private var retryJob: Job? = null
    private var off = false

    /** Last known answer: true / false, null when unknown. */
    fun peek(key: String): Boolean? = states[key]?.value

    /** The answer for [item] while collected; collecting asks the server (batched). */
    fun observe(item: InstantItem): Flow<Boolean?> = state(item.key)
        .onStart {
            withContext(confined) {
                watchers[item.key] = (watchers[item.key] ?: 0) + 1
                ask(item, retry = false)
            }
        }
        .onCompletion {
            scope.launch(confined) {
                val n = (watchers[item.key] ?: 1) - 1
                if (n <= 0) watchers.remove(item.key) else watchers[item.key] = n
            }
        }

    private fun state(key: String) = states.getOrPut(key) { MutableStateFlow(null) }

    private fun ask(item: InstantItem, retry: Boolean) {
        val key = item.key
        if (off || item.tmdbId <= 0 || key in want || key in inflight) return
        val at = answeredAt[key]
        if (at != null && !retry) {
            val ttl = if (states[key]?.value == null) UNKNOWN_TTL else KNOWN_TTL
            if (SystemClock.elapsedRealtime() - at < ttl || key in retryItems) return
        }
        want[key] = item
        if (flushJob == null) {
            flushJob = scope.launch(confined) {
                delay(FLUSH_MS)
                flushJob = null
                val items = want.values.toList()
                want.clear()
                items.chunked(BATCH).forEach { send(it) }
            }
        }
    }

    private fun send(items: List<InstantItem>) {
        items.forEach { inflight += it.key }
        scope.launch(confined) {
            val body = buildJsonObject {
                putJsonArray("items") {
                    items.forEach { it ->
                        addJsonObject {
                            put("type", it.type.apiName)
                            put("tmdb_id", it.tmdbId)
                            it.title?.let { t -> put("title", t) }
                            it.season?.let { s -> put("season", s) }
                            it.episode?.let { e -> put("episode", e) }
                        }
                    }
                }
            }
            val answer = orNull { api.call("POST", "/api/torrent-stream/instant", body = body) }?.obj
            if (answer?.bool("enabled") == false) off = true
            val results = answer?.get("results")?.obj
            val now = SystemClock.elapsedRealtime()
            items.forEach { item ->
                inflight -= item.key
                val v = (results?.get(item.key) as? JsonPrimitive)?.let { p -> p.content.toBooleanStrictOrNull() }
                answeredAt[item.key] = now
                state(item.key).value = v
                if (v == null) {
                    if (!off) scheduleRetry(item)
                } else {
                    rounds.remove(item.key)
                }
            }
        }
    }

    private fun scheduleRetry(item: InstantItem) {
        val n = rounds[item.key] ?: 0
        if (n >= MAX_ROUNDS) return
        rounds[item.key] = n + 1
        retryItems[item.key] = item
        if (retryJob != null) return
        retryJob = scope.launch(confined) {
            delay(RETRY_MS)
            retryJob = null
            val again = retryItems.values.toList()
            retryItems.clear()
            again.forEach { if ((watchers[it.key] ?: 0) > 0) ask(it, retry = true) }
        }
    }

    private companion object {
        const val FLUSH_MS = 150L
        const val RETRY_MS = 20_000L
        const val MAX_ROUNDS = 2
        const val KNOWN_TTL = 10 * 60_000L
        const val UNKNOWN_TTL = 60_000L
        const val BATCH = 100
    }
}
