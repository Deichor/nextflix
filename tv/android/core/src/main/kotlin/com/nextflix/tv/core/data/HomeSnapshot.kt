package com.nextflix.tv.core.data

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.model.HomeData
import com.nextflix.tv.core.model.RecommendationRow
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.net.obj
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.buildJsonObject
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/** Home's per-profile rows as last loaded; null: not loaded then. */
data class ProfileSnapshot(
    val cw: List<StreamProgress>?,
    val recs: List<RecommendationRow>?,
    val favs: List<Title>?,
)

/**
 * Home as last loaded, kept across launches so a cold start paints at once (then refreshes). Files in
 * filesDir/home/ in the server's JSON shapes, read with the same parsers:
 * - home-<sha1(server)>-<lang>.json: the GET /api/home answer
 * - profile-<sha1(server)>-<profile>-<lang>.json: {cw, recs, favs} as the arrays of their endpoints
 * Snapshots older than 7 days are ignored.
 */
class HomeSnapshotStore internal constructor(
    private val dir: File,
    private val json: Json,
    private val scope: CoroutineScope,
) {
    private val progressList = ListSerializer(StreamProgress.serializer())

    suspend fun readHome(server: String, language: AppLanguage): HomeData? =
        read(homeFile(server, language))?.let { orNull { it.toHomeData() } }

    suspend fun readProfile(server: String, profileId: Int, language: AppLanguage): ProfileSnapshot? {
        val o = read(profileFile(server, profileId, language))?.obj ?: return null
        return orNull {
            ProfileSnapshot(
                cw = (o["cw"] as? JsonArray)?.let { json.decodeFromJsonElement(progressList, it) },
                recs = (o["recs"] as? JsonArray)?.let { recommendationRowsOf(it) },
                favs = (o["favs"] as? JsonArray)?.let { favoritesOf(it) },
            )
        }
    }

    /** Saves [data] in the background. */
    fun writeHome(server: String, language: AppLanguage, data: HomeData) {
        scope.launch(Dispatchers.IO) { write(homeFile(server, language), data.toJson()) }
    }

    /** Saves [data] in the background. */
    fun writeProfile(server: String, profileId: Int, language: AppLanguage, data: ProfileSnapshot) {
        scope.launch(Dispatchers.IO) {
            val body = buildJsonObject {
                put("cw", data.cw?.let { json.encodeToJsonElement(progressList, it) } ?: JsonNull)
                put("recs", data.recs?.let { rows -> JsonArray(rows.map { it.toJson() }) } ?: JsonNull)
                put("favs", data.favs?.let { list -> JsonArray(list.map { it.toFavoriteJson() }) } ?: JsonNull)
            }
            write(profileFile(server, profileId, language), body)
        }
    }

    private fun homeFile(server: String, language: AppLanguage) = File(dir, "home-${sha1(server)}-${language.code}.json")

    private fun profileFile(server: String, profileId: Int, language: AppLanguage) =
        File(dir, "profile-${sha1(server)}-$profileId-${language.code}.json")

    private suspend fun read(file: File): JsonElement? = withContext(Dispatchers.IO) {
        orNull {
            if (!file.isFile || System.currentTimeMillis() - file.lastModified() > MAX_AGE_MS) return@orNull null
            json.parseToJsonElement(file.readText())
        }
    }

    // Temp file + rename: a reader never sees half a file
    private fun write(file: File, body: JsonElement) {
        runCatching {
            dir.mkdirs()
            // Snapshots too old to be read (other servers, languages, profiles) and stray temp files go
            val now = System.currentTimeMillis()
            dir.listFiles()?.forEach { if (now - it.lastModified() > MAX_AGE_MS) it.delete() }
            val tmp = File.createTempFile(file.name, ".tmp", dir)
            try {
                tmp.writeText(json.encodeToString(JsonElement.serializer(), body))
                if (!tmp.renameTo(file)) tmp.delete()
            } catch (e: Exception) {
                tmp.delete()
                throw e
            }
        }
    }

    private fun sha1(text: String): String =
        MessageDigest.getInstance("SHA-1").digest(text.toByteArray()).joinToString("") { "%02x".format(it) }

    private companion object {
        val MAX_AGE_MS = TimeUnit.DAYS.toMillis(7)
    }
}
