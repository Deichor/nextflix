package com.nextflix.tv.feature.account

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ServerSettingsTest {
    @Test
    fun parsesIndexerWithoutKey() {
        val s = parseIndexer(
            Json.parseToJsonElement(
                """{"id":1,"jackett_url":"http://jackett:9117","api_key_set":true,"min_seeders":10,"bitsearch_enabled":false}""",
            ),
        )
        assertEquals(IndexerSettings("http://jackett:9117", apiKeySet = true, bitsearch = false), s)
        assertEquals(IndexerSettings("", apiKeySet = false, bitsearch = true), parseIndexer(JsonObject(emptyMap())))
    }

    @Test
    fun parsesDebridWithDefaults() {
        val s = parseDebrid(Json.parseToJsonElement("""{"torbox":{"api_key_set":true,"enabled":true,"prefer":false,"add_uncached":false}}"""))
        assertEquals(DebridSettings(apiKeySet = true, enabled = true, prefer = false, addUncached = false), s)
        assertEquals(DebridSettings(apiKeySet = false, enabled = true, prefer = true, addUncached = false), parseDebrid(JsonNull))
    }

    @Test
    fun keyIsOmittedReplacedOrCleared() {
        assertEquals(KeyChange.Keep, KeyChange.of("  ", cleared = false))
        assertEquals(KeyChange.Clear, KeyChange.of("", cleared = true))
        assertEquals(KeyChange.Replace("abc"), KeyChange.of(" abc ", cleared = true))

        assertFalse("jackett_api_key" in indexerBody("u", KeyChange.Keep, true))
        assertEquals(JsonNull, indexerBody("u", KeyChange.Clear, true)["jackett_api_key"])
        val torbox = debridBody(KeyChange.Replace("k"), enabled = true, prefer = false, addUncached = true)["torbox"]!!.jsonObject
        assertEquals("k", torbox["api_key"].toString().trim('"'))
        assertTrue("prefer" in torbox)
    }

    @Test
    fun dirtyOnlyAfterAChange() {
        val loaded = IndexerSettings("http://jackett:9117", apiKeySet = true, bitsearch = true)
        val ui = IndexerUi(loaded = loaded, url = loaded.jackettUrl, bitsearch = true)
        assertFalse(ui.dirty)
        assertTrue(ui.copy(url = "http://other:9117").dirty)
        assertTrue(ui.copy(keyCleared = true).dirty)
        assertFalse(ui.copy(url = " http://jackett:9117 ").dirty)
    }
}
