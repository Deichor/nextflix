package com.nextflix.tv.core.data

import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.Rating
import com.nextflix.tv.core.model.RatingTarget
import com.nextflix.tv.core.model.RatingValue
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.bool
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObjectBuilder
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * Thumbs ratings and favorites (docs/API.md "Ratings and Favorites"). [version] grows after every change, so the
 * Favorites row and the recommendations reload (they also depend on ratings).
 */
class RatingsRepository internal constructor(private val api: ApiClient) {
    private val _version = MutableStateFlow(0)
    val version: StateFlow<Int> = _version.asStateFlow()

    /** The profile's rating of a title; no rating / failure -> Rating(). */
    suspend fun get(profileId: Int, type: MediaType, tmdbId: Int): Rating =
        orNull {
            api.getJson("/api/ratings/item", mapOf("profile_id" to profileId, "media_type" to type.apiName, "tmdb_id" to tmdbId)).toRating()
        } ?: Rating()

    /** Sets (or with null clears) the thumbs rating; returns the stored state, null on failure. */
    suspend fun setRating(profileId: Int, target: RatingTarget, value: RatingValue?): Rating? =
        send(profileId, target) { if (value == null) put("rating", JsonNull) else put("rating", value.apiName) }

    /** Adds to or removes from Favorites; returns the stored state, null on failure. */
    suspend fun setFavorite(profileId: Int, target: RatingTarget, favorite: Boolean): Rating? =
        send(profileId, target) { put("favorite", favorite) }

    private suspend fun send(
        profileId: Int,
        t: RatingTarget,
        patch: JsonObjectBuilder.() -> Unit,
    ): Rating? {
        val body = buildJsonObject {
            put("profile_id", profileId)
            put("media_type", t.type.apiName)
            put("tmdb_id", t.tmdbId)
            put("title", t.title)
            put("poster_path", t.poster)
            put("backdrop_path", t.backdrop)
            patch()
        }
        val result = orNull { api.call("PUT", "/api/ratings", body = body).toRating() }
        if (result != null) _version.update { it + 1 }
        return result
    }

    private fun JsonElement.toRating(): Rating {
        val o = obj ?: return Rating()
        return Rating(rating = RatingValue.entries.firstOrNull { it.apiName == o.str("rating") }, favorite = o.bool("favorite") == true)
    }
}
