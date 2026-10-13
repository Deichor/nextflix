package com.nextflix.tv.feature.subtitles

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.nextflix.tv.core.Core
import kotlinx.coroutines.flow.first
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import java.io.IOException

// One DataStore per file and process (DataStore refuses two for the same file)
private val Context.subtitleTiming by preferencesDataStore("subtitle-timing")

/**
 * Subtitle timing per playing file (hash + index), like the web player: the delay chosen for a file is used again
 * when the same file plays later. The newest [MAX_FILES] files are kept.
 */
class SubtitleTimingStore(private val core: Core) {
    @Serializable
    private data class Entry(val file: String, val seconds: Double)

    private val store get() = core.appContext.subtitleTiming
    private val serializer = ListSerializer(Entry.serializer())

    private fun key(hash: String, index: Int) = "${hash.lowercase()}:$index"

    private fun decode(raw: String?): List<Entry> =
        raw?.let { runCatching { json.decodeFromString(serializer, it) }.getOrNull() }.orEmpty()

    /** Saved delay in seconds for the file, 0 when none. */
    suspend fun get(hash: String, index: Int): Double {
        if (hash.isEmpty()) return 0.0
        val k = key(hash, index)
        val entries = try {
            decode(store.data.first()[ENTRIES])
        } catch (e: IOException) {
            emptyList()
        }
        return entries.firstOrNull { it.file == k }?.seconds ?: 0.0
    }

    /** Keeps [seconds] for the file (0 forgets it), newest first. */
    suspend fun set(hash: String, index: Int, seconds: Double) {
        if (hash.isEmpty()) return
        val k = key(hash, index)
        try {
            store.edit { prefs ->
                val rest = decode(prefs[ENTRIES]).filter { it.file != k }
                val next = if (seconds == 0.0) rest else listOf(Entry(k, seconds)) + rest
                prefs[ENTRIES] = json.encodeToString(serializer, next.take(MAX_FILES))
            }
        } catch (e: IOException) {
            // Not kept this time; the delay still applies to this playback
        }
    }

    private companion object {
        const val MAX_FILES = 200
        val ENTRIES = stringPreferencesKey("files")
        val json = Json { ignoreUnknownKeys = true }
    }
}
