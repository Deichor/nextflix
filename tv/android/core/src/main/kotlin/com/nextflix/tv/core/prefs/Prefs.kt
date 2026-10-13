package com.nextflix.tv.core.prefs

import androidx.compose.runtime.Immutable
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.launch
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

enum class HdrMode { Auto, On, Off }

/** Settings of this TV (not per profile). */
@Immutable
data class DeviceSettings(
    val hdrMode: HdrMode = HdrMode.Auto,
    val introAnimation: Boolean = true,
    val introSound: Boolean = true,
)

/** Device-wide settings in DataStore; [settings] is the current value (defaults until loaded). */
class DevicePrefs internal constructor(private val store: DataStore<Preferences>, scope: CoroutineScope) {
    val settings: StateFlow<DeviceSettings> = store.data
        .map(::read)
        .distinctUntilChanged()
        .stateIn(scope, SharingStarted.Eagerly, DeviceSettings())

    /** The stored settings, read from disk when [settings] may still hold the defaults (app start). */
    suspend fun current(): DeviceSettings = read(store.data.first())

    private fun read(p: Preferences) = DeviceSettings(
        hdrMode = p[HDR]?.let { v -> HdrMode.entries.firstOrNull { it.name == v } } ?: HdrMode.Auto,
        introAnimation = p[INTRO_ANIMATION] ?: true,
        introSound = p[INTRO_SOUND] ?: true,
    )

    suspend fun setHdrMode(mode: HdrMode) = store.edit { it[HDR] = mode.name }
    suspend fun setIntroAnimation(on: Boolean) = store.edit { it[INTRO_ANIMATION] = on }
    suspend fun setIntroSound(on: Boolean) = store.edit { it[INTRO_SOUND] = on }

    private companion object {
        val HDR = stringPreferencesKey("device.hdr")
        val INTRO_ANIMATION = booleanPreferencesKey("device.introAnimation")
        val INTRO_SOUND = booleanPreferencesKey("device.introSound")
    }
}

/** The viewer's last subtitle choice: off, or a language (and the exact subtitle id when known). */
@Serializable
sealed interface SubtitleChoice {
    @Serializable
    @SerialName("off")
    data object Off : SubtitleChoice

    @Serializable
    @SerialName("lang")
    data class Language(val lang: String, val id: String? = null) : SubtitleChoice
}

/** Per-profile player preferences (webOS `hfx-player:<profileId>`). */
@Immutable
@Serializable
data class PlayerPrefs(
    /** ISO 639-1 of the last picked audio track, or "original" */
    val audioLang: String? = null,
    /** null: never chosen (the default pick runs) */
    val subtitle: SubtitleChoice? = null,
    val subtitleStyle: SubtitleStyle = SubtitleStyle(),
    val autoplayNext: Boolean = true,
)

/** PlayerPrefs of every profile, in DataStore as JSON per profile id. */
class PlayerPrefsStore internal constructor(
    private val store: DataStore<Preferences>,
    scope: CoroutineScope,
    private val json: Json,
) {
    private val _all = MutableStateFlow<Map<Int, PlayerPrefs>>(emptyMap())

    /** Every profile's prefs (profiles without saved prefs are missing: use [get]). */
    val all: StateFlow<Map<Int, PlayerPrefs>> = _all.asStateFlow()

    init {
        scope.launch {
            store.data.collect { prefs ->
                _all.value = prefs.asMap().mapNotNull { (key, value) ->
                    val id = key.name.removePrefix(PREFIX).takeIf { key.name.startsWith(PREFIX) }?.toIntOrNull()
                    val decoded = (value as? String)?.let { runCatching { json.decodeFromString(PlayerPrefs.serializer(), it) }.getOrNull() }
                    if (id != null && decoded != null) id to decoded else null
                }.toMap()
            }
        }
    }

    fun get(profileId: Int?): PlayerPrefs = _all.value[profileId ?: 0] ?: PlayerPrefs()

    /** Changes one profile's prefs: `update(pid) { it.copy(autoplayNext = false) }`. */
    suspend fun update(profileId: Int?, change: (PlayerPrefs) -> PlayerPrefs) {
        val id = profileId ?: 0
        val next = change(get(id))
        _all.value = _all.value + (id to next)
        store.edit { it[stringPreferencesKey("$PREFIX$id")] = json.encodeToString(PlayerPrefs.serializer(), next) }
    }

    private companion object {
        const val PREFIX = "player:"
    }
}
