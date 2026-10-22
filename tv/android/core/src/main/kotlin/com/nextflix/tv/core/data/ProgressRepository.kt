package com.nextflix.tv.core.data

import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.net.AnswerCache
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.ApiException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonNull
import java.time.OffsetDateTime
import kotlin.time.Duration.Companion.seconds

/**
 * Watch progress (docs/API.md "Continue watching"). [version] grows after every save / delete, so screens below
 * the player (Home's Continue Watching, Detail's episode bars) reload when they come back and see a newer value.
 */
class ProgressRepository internal constructor(private val api: ApiClient, private val cache: AnswerCache) {
    private val _version = MutableStateFlow(0)
    val version: StateFlow<Int> = _version.asStateFlow()

    private val listSerializer = ListSerializer(StreamProgress.serializer())

    /**
     * Continue Watching row: newest first, one entry per title, finished ones left out (server rules). A caught-up
     * weekly show stays as its next episode, not aired yet ([StreamProgress.isUpcoming]).
     */
    suspend fun continueWatching(profileId: Int): List<StreamProgress> {
        val data = api.getJson("/api/stream-progress", mapOf("profile_id" to profileId, "upcoming" to 1))
        return if (data == JsonNull) emptyList() else api.decode(listSerializer, data)
    }

    /** [continueWatching] cached for 15 s per progress version (Home, its prefetch and Watch Next share it). */
    suspend fun continueWatchingCached(profileId: Int): List<StreamProgress> =
        cache.get(cache.scoped("cw-$profileId-p${version.value}"), 15.seconds) { continueWatching(profileId) }

    /** Every entry of one title (finished ones too), newest first; [] on failure. */
    suspend fun forTitle(profileId: Int, type: MediaType, tmdbId: Int): List<StreamProgress> =
        orNull {
            val data = api.getJson(
                "/api/stream-progress/title",
                mapOf("profile_id" to profileId, "media_type" to type.apiName, "tmdb_id" to tmdbId),
            )
            if (data == JsonNull) emptyList() else api.decode(listSerializer, data)
        }.orEmpty().sortedByDescending { it.updatedAt?.let { t -> runCatching { OffsetDateTime.parse(t).toEpochSecond() }.getOrNull() } ?: 0L }

    /** One entry, or null when there is none (404). */
    suspend fun item(profileId: Int, type: MediaType, tmdbId: Int, season: Int = 0, episode: Int = 0): StreamProgress? =
        try {
            api.get(
                "/api/stream-progress/item",
                StreamProgress.serializer(),
                mapOf("profile_id" to profileId, "media_type" to type.apiName, "tmdb_id" to tmdbId, "season" to season, "episode" to episode),
            )
        } catch (e: ApiException.Http) {
            if (e.status == 404) null else throw e
        }

    /** Upsert by (profile, type, tmdb id, season, episode). Best effort: failures are swallowed (returns false). */
    suspend fun save(progress: StreamProgress): Boolean {
        val ok = orNull {
            api.call("PUT", "/api/stream-progress", body = api.body(progress.copy(id = null, updatedAt = null)))
            true
        } ?: false
        if (ok) _version.update { it + 1 }
        return ok
    }

    /** Removes every episode of the title. */
    suspend fun delete(profileId: Int, type: MediaType, tmdbId: Int) {
        api.delete("/api/stream-progress", mapOf("profile_id" to profileId, "media_type" to type.apiName, "tmdb_id" to tmdbId))
        _version.update { it + 1 }
    }
}
