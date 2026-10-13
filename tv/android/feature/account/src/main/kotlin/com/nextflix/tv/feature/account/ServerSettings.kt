package com.nextflix.tv.feature.account

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.ApiException
import com.nextflix.tv.core.net.bool
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonObjectBuilder
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject

// Server-wide indexer and TorBox settings (docs/API.md, /api/settings/indexer and /api/settings/debrid), ported
// from web/src/features/settings. The server never returns a stored key, only whether one is set.

internal data class IndexerSettings(val jackettUrl: String, val apiKeySet: Boolean, val bitsearch: Boolean)

internal data class DebridSettings(val apiKeySet: Boolean, val enabled: Boolean, val prefer: Boolean, val addUncached: Boolean)

internal data class TestResult(
    val success: Boolean,
    val message: String,
    val planName: String? = null,
    val expiresAt: String? = null,
    val active: Boolean? = null,
)

/** What a save does with a stored key: keep it (field omitted), replace it, or remove it (null). */
internal sealed interface KeyChange {
    data object Keep : KeyChange
    data class Replace(val value: String) : KeyChange
    data object Clear : KeyChange

    companion object {
        fun of(typed: String, cleared: Boolean): KeyChange = when {
            typed.isNotBlank() -> Replace(typed.trim())
            cleared -> Clear
            else -> Keep
        }
    }
}

private fun JsonObjectBuilder.putKey(name: String, change: KeyChange) {
    when (change) {
        KeyChange.Keep -> Unit
        KeyChange.Clear -> put(name, JsonNull)
        is KeyChange.Replace -> put(name, change.value)
    }
}

internal fun parseIndexer(json: JsonElement): IndexerSettings {
    val o = json.obj
    return IndexerSettings(
        jackettUrl = o?.str("jackett_url").orEmpty(),
        apiKeySet = o?.bool("api_key_set") == true,
        bitsearch = o?.bool("bitsearch_enabled") != false,
    )
}

internal fun parseDebrid(json: JsonElement): DebridSettings {
    val tb = json.obj?.obj("torbox")
    return DebridSettings(
        apiKeySet = tb?.bool("api_key_set") == true,
        enabled = tb?.bool("enabled") != false,
        prefer = tb?.bool("prefer") != false,
        addUncached = tb?.bool("add_uncached") == true,
    )
}

internal fun indexerBody(url: String, key: KeyChange, bitsearch: Boolean) = buildJsonObject {
    put("jackett_url", url)
    put("bitsearch_enabled", bitsearch)
    putKey("jackett_api_key", key)
}

internal fun debridBody(key: KeyChange, enabled: Boolean, prefer: Boolean, addUncached: Boolean) = buildJsonObject {
    putJsonObject("torbox") {
        put("enabled", enabled)
        put("prefer", prefer)
        put("add_uncached", addUncached)
        putKey("api_key", key)
    }
}

internal class ServerSettingsApi(private val api: ApiClient) {
    suspend fun indexer(): IndexerSettings = parseIndexer(api.getJson("/api/settings/indexer"))

    suspend fun saveIndexer(url: String, key: KeyChange, bitsearch: Boolean): IndexerSettings =
        parseIndexer(api.call("PUT", "/api/settings/indexer", body = indexerBody(url, key, bitsearch)))

    /** Empty fields fall back to the stored settings on the server. */
    suspend fun testIndexer(url: String, key: String): TestResult = test {
        api.call("POST", "/api/settings/indexer/test", body = buildJsonObject {
            put("jackett_url", url)
            put("jackett_api_key", key)
        })
    }

    suspend fun debrid(): DebridSettings = parseDebrid(api.getJson("/api/settings/debrid"))

    suspend fun saveDebrid(key: KeyChange, enabled: Boolean, prefer: Boolean, addUncached: Boolean): DebridSettings =
        parseDebrid(api.call("PUT", "/api/settings/debrid", body = debridBody(key, enabled, prefer, addUncached)))

    /** TorBox user/me with [key] (empty: the stored one): plan and expiry. */
    suspend fun testDebrid(key: String): TestResult = test {
        api.call("POST", "/api/settings/debrid/test", body = buildJsonObject { putJsonObject("torbox") { put("api_key", key) } })
    }

    private suspend fun test(call: suspend () -> JsonElement): TestResult = try {
        val o = call().obj ?: JsonObject(emptyMap())
        val success = o.bool("success") != false
        TestResult(
            success = success,
            message = (if (success) o.str("message") else o.str("error") ?: o.str("message")).orEmpty(),
            planName = o.str("plan_name"),
            expiresAt = o.str("premium_expires_at"),
            active = o.bool("active"),
        )
    } catch (e: ApiException.Http) {
        TestResult(false, e.body.obj?.str("error") ?: e.body.obj?.str("message") ?: e.message.orEmpty())
    }
}

internal enum class Connection { Checking, Connected, NotSet, Error }

/** The line under the buttons after Save or Test. */
internal sealed interface Notice {
    data object Saved : Notice
    data class SaveFailed(val message: String) : Notice
    data class Tested(val result: TestResult) : Notice
    data class TestFailed(val message: String) : Notice
}

internal data class IndexerUi(
    val loaded: IndexerSettings? = null,
    val loadError: String? = null,
    val url: String = "",
    val key: String = "",
    val keyCleared: Boolean = false,
    val bitsearch: Boolean = true,
    val connection: Connection = Connection.Checking,
    val connectionError: String = "",
    val saving: Boolean = false,
    val testing: Boolean = false,
    val notice: Notice? = null,
) {
    val dirty: Boolean
        get() = loaded != null &&
            (url.trim() != loaded.jackettUrl || key.isNotBlank() || keyCleared || bitsearch != loaded.bitsearch)
}

internal class IndexerViewModel(private val api: ServerSettingsApi, private val describe: (Throwable) -> String) : ViewModel() {
    private val _state = MutableStateFlow(IndexerUi())
    val state: StateFlow<IndexerUi> = _state.asStateFlow()
    private var check: Job? = null

    fun load() {
        _state.value = IndexerUi()
        viewModelScope.launch {
            try {
                val s = api.indexer()
                reset(s)
                checkStored(s)
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { it.copy(loadError = describe(e), connection = Connection.Error) }
            }
        }
    }

    fun setUrl(v: String) = _state.update { it.copy(url = v, notice = null) }
    fun setKey(v: String) = _state.update { it.copy(key = v, keyCleared = if (v.isNotEmpty()) false else it.keyCleared, notice = null) }
    fun clearKey() = _state.update { it.copy(key = "", keyCleared = true, notice = null) }
    fun toggleBitsearch() = _state.update { it.copy(bitsearch = !it.bitsearch, notice = null) }

    fun save() {
        val st = _state.value
        if (st.loaded == null || st.saving || !st.dirty) return
        _state.update { it.copy(saving = true, notice = null) }
        viewModelScope.launch {
            try {
                val saved = api.saveIndexer(st.url.trim(), KeyChange.of(st.key, st.keyCleared), st.bitsearch)
                reset(saved)
                _state.update { it.copy(saving = false, notice = Notice.Saved) }
                checkStored(saved)
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { it.copy(saving = false, notice = Notice.SaveFailed(describe(e))) }
            }
        }
    }

    fun test() {
        val st = _state.value
        if (st.testing || st.url.isBlank()) return
        _state.update { it.copy(testing = true, notice = null) }
        viewModelScope.launch {
            try {
                val r = api.testIndexer(st.url.trim(), if (st.keyCleared) "" else st.key.trim())
                _state.update {
                    val clean = !it.dirty
                    it.copy(
                        testing = false,
                        notice = Notice.Tested(r),
                        connection = if (clean) (if (r.success) Connection.Connected else Connection.Error) else it.connection,
                        connectionError = if (clean && !r.success) r.message else it.connectionError,
                    )
                }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { it.copy(testing = false, notice = Notice.TestFailed(describe(e))) }
            }
        }
    }

    private fun reset(s: IndexerSettings) = _state.update {
        it.copy(loaded = s, loadError = null, url = s.jackettUrl, key = "", keyCleared = false, bitsearch = s.bitsearch)
    }

    /** Tests the stored settings so the badge shows the real state. */
    private fun checkStored(s: IndexerSettings) {
        check?.cancel()
        if (s.jackettUrl.isEmpty() || !s.apiKeySet) {
            _state.update { it.copy(connection = Connection.NotSet, connectionError = "") }
            return
        }
        _state.update { it.copy(connection = Connection.Checking) }
        check = viewModelScope.launch {
            val r = try {
                api.testIndexer("", "")
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                TestResult(false, describe(e))
            }
            _state.update { it.copy(connection = if (r.success) Connection.Connected else Connection.Error, connectionError = if (r.success) "" else r.message) }
        }
    }
}

internal data class DebridUi(
    val loaded: DebridSettings? = null,
    val loadError: String? = null,
    val key: String = "",
    val keyCleared: Boolean = false,
    val enabled: Boolean = true,
    val prefer: Boolean = true,
    val addUncached: Boolean = false,
    val connection: Connection = Connection.Checking,
    /** The stored key's plan (connected) or the error */
    val connectionResult: TestResult? = null,
    val saving: Boolean = false,
    val testing: Boolean = false,
    val notice: Notice? = null,
) {
    val dirty: Boolean
        get() = loaded != null && (
            key.isNotBlank() || keyCleared || enabled != loaded.enabled || prefer != loaded.prefer ||
                addUncached != loaded.addUncached
            )

    val canTest: Boolean
        get() = loaded != null && !testing && (key.isNotBlank() || (loaded.apiKeySet && !keyCleared))
}

internal class DebridViewModel(private val api: ServerSettingsApi, private val describe: (Throwable) -> String) : ViewModel() {
    private val _state = MutableStateFlow(DebridUi())
    val state: StateFlow<DebridUi> = _state.asStateFlow()
    private var check: Job? = null

    fun load() {
        _state.value = DebridUi()
        viewModelScope.launch {
            try {
                val s = api.debrid()
                reset(s)
                checkStored(s)
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { it.copy(loadError = describe(e), connection = Connection.Error) }
            }
        }
    }

    fun setKey(v: String) = _state.update { it.copy(key = v, keyCleared = if (v.isNotEmpty()) false else it.keyCleared, notice = null) }
    fun clearKey() = _state.update { it.copy(key = "", keyCleared = true, notice = null) }
    fun toggleEnabled() = _state.update { it.copy(enabled = !it.enabled, notice = null) }
    fun togglePrefer() = _state.update { it.copy(prefer = !it.prefer, notice = null) }
    fun toggleAddUncached() = _state.update { it.copy(addUncached = !it.addUncached, notice = null) }

    fun save() {
        val st = _state.value
        if (st.loaded == null || st.saving || !st.dirty) return
        _state.update { it.copy(saving = true, notice = null) }
        viewModelScope.launch {
            try {
                val saved = api.saveDebrid(KeyChange.of(st.key, st.keyCleared), st.enabled, st.prefer, st.addUncached)
                reset(saved)
                _state.update { it.copy(saving = false, notice = Notice.Saved) }
                checkStored(saved)
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { it.copy(saving = false, notice = Notice.SaveFailed(describe(e))) }
            }
        }
    }

    fun test() {
        val st = _state.value
        if (!st.canTest) return
        _state.update { it.copy(testing = true, notice = null) }
        viewModelScope.launch {
            try {
                val r = api.testDebrid(if (st.keyCleared) "" else st.key.trim())
                _state.update { it.copy(testing = false, notice = Notice.Tested(r)) }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { it.copy(testing = false, notice = Notice.TestFailed(describe(e))) }
            }
        }
    }

    private fun reset(s: DebridSettings) = _state.update {
        it.copy(loaded = s, loadError = null, key = "", keyCleared = false, enabled = s.enabled, prefer = s.prefer, addUncached = s.addUncached)
    }

    /** Asks TorBox with the stored key so the badge shows the real state and the plan. */
    private fun checkStored(s: DebridSettings) {
        check?.cancel()
        if (!s.apiKeySet) {
            _state.update { it.copy(connection = Connection.NotSet, connectionResult = null) }
            return
        }
        _state.update { it.copy(connection = Connection.Checking) }
        check = viewModelScope.launch {
            val r = try {
                api.testDebrid("")
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                TestResult(false, describe(e))
            }
            _state.update {
                it.copy(connection = if (r.success && r.active != false) Connection.Connected else Connection.Error, connectionResult = r)
            }
        }
    }
}
