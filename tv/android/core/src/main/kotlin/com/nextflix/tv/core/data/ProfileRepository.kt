package com.nextflix.tv.core.data

import com.nextflix.tv.core.model.Profile
import com.nextflix.tv.core.model.ProfileInput
import com.nextflix.tv.core.net.ApiClient
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonNull

/** /api/profiles (docs/API.md "Profiles"). */
class ProfileRepository internal constructor(private val api: ApiClient) {
    /** All profiles ordered by id. */
    suspend fun list(): List<Profile> {
        val data = api.getJson("/api/profiles")
        return if (data == JsonNull) emptyList() else api.decode(ListSerializer(Profile.serializer()), data)
    }

    /** 400 (ApiException.Http) for an empty / too long name or when 5 profiles exist. */
    suspend fun create(input: ProfileInput): Profile = api.post("/api/profiles", api.body(input))

    suspend fun update(id: Int, input: ProfileInput): Profile = api.put("/api/profiles/$id", api.body(input))

    /** Also deletes the profile's progress, ratings and favorites; 400 for the last profile. */
    suspend fun delete(id: Int) = api.delete("/api/profiles/$id")
}
