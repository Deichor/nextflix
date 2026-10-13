package com.nextflix.tv.core.session

import androidx.compose.runtime.Immutable
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.model.Profile
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.serialization.json.Json
import java.io.IOException

/** The saved server address and the active profile. */
@Immutable
data class SessionState(val server: String?, val profile: Profile?) {
    /** UI and content language of the active profile (English without one). */
    val language: AppLanguage get() = AppLanguage.of(profile?.language)
}

/**
 * Server address (set on the Setup screen) and active profile, kept in DataStore. Reads are synchronous
 * ([current]); [state] is null until the stored values were read once at start.
 */
class Session internal constructor(
    private val store: DataStore<Preferences>,
    private val scope: CoroutineScope,
    private val json: Json,
) {
    private val _state = MutableStateFlow<SessionState?>(null)

    /** null while loading (the app shows nothing until then), then every change. */
    val state: StateFlow<SessionState?> = _state.asStateFlow()

    val current: SessionState get() = _state.value ?: EMPTY

    /** "http://host:8252" without a trailing slash, or null before the first setup. */
    val server: String? get() = current.server
    val profile: Profile? get() = current.profile
    val profileId: Int? get() = current.profile?.id
    val language: AppLanguage get() = current.language

    init {
        scope.launch {
            val prefs = store.data.first()
            val profile = prefs[PROFILE]?.let { runCatching { json.decodeFromString(Profile.serializer(), it) }.getOrNull() }
            if (_state.value == null) _state.value = SessionState(prefs[SERVER], profile)
        }
    }

    /** Saves a server address (normalized). The profile is kept; callers usually go to Profiles next. */
    suspend fun setServer(url: String) {
        val normalized = ServerAddress.normalize(url)
        _state.value = current.copy(server = normalized)
        store.edit { it[SERVER] = normalized }
    }

    /**
     * Makes [profile] active (null: none) at once for headers and the UI language; saving it runs in the background
     * (the returned job ends after the write).
     */
    fun selectProfile(profile: Profile?): Job {
        _state.value = current.copy(profile = profile)
        return scope.launch {
            try {
                store.edit {
                    // The latest choice wins when two picks overlap
                    val latest = current.profile
                    if (latest == null) it.remove(PROFILE) else it[PROFILE] = json.encodeToString(Profile.serializer(), latest)
                }
            } catch (_: IOException) {
                // Kept for this run; the next pick saves again
            }
        }
    }

    /** [selectProfile], returning after the write. */
    suspend fun setProfile(profile: Profile?) {
        selectProfile(profile).join()
    }

    private companion object {
        val EMPTY = SessionState(null, null)
        val SERVER = stringPreferencesKey("server")
        val PROFILE = stringPreferencesKey("profile")
    }
}
