package com.nextflix.tv.core.net

import android.os.SystemClock
import com.nextflix.tv.core.session.Session
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.async
import kotlin.time.Duration

/**
 * In-memory answer cache (webOS lib/http.ts `cached`): a screen opened again paints at once, and concurrent asks
 * for one key share one request. Least recently used entries go first.
 */
class AnswerCache internal constructor(
    private val session: Session,
    private val scope: CoroutineScope,
    private val max: Int = 150,
) {
    private class Entry(val data: Any?, val expiresAt: Long)

    private val entries = LinkedHashMap<String, Entry>(32, 0.75f, true)
    private val inflight = HashMap<String, Deferred<Any?>>()

    /** Key that includes server, profile and language (answers depend on all three). */
    fun scoped(key: String): String {
        val s = session.current
        return "${s.server}|${s.profile?.id ?: 0}|${s.language.code}|$key"
    }

    /** The cached answer, or null when missing or expired. */
    @Suppress("UNCHECKED_CAST")
    fun <T> peek(key: String): T? = synchronized(this) {
        val e = entries[key] ?: return null
        if (SystemClock.elapsedRealtime() >= e.expiresAt) {
            entries.remove(key)
            return null
        }
        e.data as T
    }

    /**
     * The cached answer, else [load]s it once (concurrent callers share the load). The result is kept for [ttl]
     * when [keep] says so (e.g. not for a partial answer). A failed load is not cached and rethrows.
     */
    @Suppress("UNCHECKED_CAST")
    suspend fun <T> get(key: String, ttl: Duration, keep: (T) -> Boolean = { true }, load: suspend () -> T): T {
        peek<T>(key)?.let { return it }
        val deferred = synchronized(this) {
            inflight[key] ?: scope.async {
                try {
                    val data = load()
                    if (keep(data)) store(key, ttl, data)
                    data
                } finally {
                    synchronized(this@AnswerCache) { inflight.remove(key) }
                }
            }.also { inflight[key] = it as Deferred<Any?> }
        }
        return deferred.await() as T
    }

    fun store(key: String, ttl: Duration, data: Any?) = synchronized(this) {
        entries[key] = Entry(data, SystemClock.elapsedRealtime() + ttl.inWholeMilliseconds)
        while (entries.size > max) entries.remove(entries.keys.first())
    }

    /** Drops every entry whose key contains [match] (e.g. "|cw" after playback). */
    fun invalidate(match: String) = synchronized(this) {
        entries.keys.removeAll { match in it }
    }

    fun clear() = synchronized(this) { entries.clear() }
}
