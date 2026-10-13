package com.nextflix.tv.feature.subtitles

import androidx.compose.runtime.Immutable
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.nav.PlayRequest
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** One listed subtitle (docs/API.md "Subtitles", the `Sub` object of GET /api/subtitles). */
@Immutable
@Serializable
data class SubtitleTrack(
    /** opaque id for GET /api/subtitles/file */
    val id: String,
    /** torrent, embedded, opensubtitles, subdl, subsource, turkcealtyazi, subf2m, yifysubtitles, ai */
    val source: String = "",
    /** ISO 639-1 / 639-2 code as the provider gave it (normalize with Languages.norm) */
    val language: String = "",
    val label: String = "",
    val release: String = "",
    val hi: Boolean = false,
    val forced: Boolean = false,
    val synced: Boolean = false,
    val score: Double = 0.0,
    /** embedded track of a big file: read while it plays, fetch with the position and poll */
    val live: Boolean = false,
    /** OpenSubtitles item while its download quota is used up */
    val quota: Boolean = false,
    /** source "ai": the language this AI translation is made from */
    @SerialName("ai_from") val aiFrom: String? = null,
    /** lines a reference of the playing file has where this subtitle has none (filled by AI when it is on) */
    val holes: Int = 0,
) {
    val isAi: Boolean get() = source == SubtitleRules.SOURCE_AI
}

/** Query of GET /api/subtitles for the playing file. */
@Immutable
data class SubtitleQuery(
    val tmdbId: Int,
    val type: MediaType,
    val season: Int? = null,
    val episode: Int? = null,
    /** playing torrent file */
    val hash: String,
    val index: Int,
    /** wanted languages in order, e.g. ["tr", "en"] */
    val langs: List<String>,
    /** playing torrent name (ranking hint) */
    val title: String? = null,
    val year: Int? = null,
    /** the profile's language: the AI translation target "profile" */
    val uiLang: String? = null,
    /** ISO 639-1 of the title's original language: counts as the audio language while none is known */
    val originalLanguage: String? = null,
) {
    /** The profile's language (the first of [langs]). */
    val preferred: String get() = langs.firstOrNull().orEmpty()

    companion object {
        /** Query for [request] playing file [hash] / [index]; [torrentTitle]: the torrent or file name. */
        fun of(request: PlayRequest, hash: String, index: Int, language: AppLanguage, torrentTitle: String? = null) = SubtitleQuery(
            tmdbId = request.tmdbId,
            type = request.type,
            season = request.season,
            episode = request.episode,
            hash = hash,
            index = index,
            langs = if (language == AppLanguage.Tr) listOf("tr", "en") else listOf("en", "tr"),
            title = torrentTitle?.takeIf { it.isNotBlank() },
            year = request.year,
            uiLang = language.code,
            originalLanguage = request.originalLanguage,
        )
    }
}

/** One subtitle cue; times in seconds on the file's timeline. */
@Immutable
data class Cue(val start: Double, val end: Double, val text: String)

/** Answer of GET /api/subtitles/file. */
@Immutable
data class CueFile(
    /** sorted by start */
    val cues: List<Cue>,
    /** live or being timed: more / better cues follow, ask again (LIVE_CUES_POLL_MS) */
    val partial: Boolean = false,
    /** X-Subtitle-Sync header value (original, pending, live, synced, unchanged, replaced, poor, failed; "-" none) */
    val sync: String = "",
    /** AI translation: the lines at the position are still being translated (ask again after AI_CUES_POLL_MS) */
    val translating: Boolean = false,
    /** no cues because of a server or network error (often over in seconds), not a missing file */
    val failed: Boolean = false,
    /** HTTP status (0: network error) */
    val status: Int = 200,
    /** X-Subtitle-Holes ("2 holes, 40 lines, 38 filled"), "" without */
    val holes: String = "",
)

/** What the player shows about subtitles (driven by [SubtitleController]). */
@Immutable
data class SubtitleState(
    /** every listed subtitle (merged across refreshes) */
    val tracks: List<SubtitleTrack> = emptyList(),
    /** one row per language for the panel (its best subtitle), the profile's languages first */
    val menu: List<SubtitleTrack> = emptyList(),
    /** selected subtitle id, null = off */
    val selectedId: String? = null,
    /** cues of the selected subtitle (empty while loading or off) */
    val cues: List<Cue> = emptyList(),
    /** no list answer yet for the playing file (the panel shows "Looking for subtitles...") */
    val searching: Boolean = false,
    /** an AI translation is being made at the position for the selected subtitle (show a note) */
    val translating: Boolean = false,
    /** subtitle timing in seconds (+ shows them later), kept per file */
    val delaySeconds: Double = 0.0,
    /** X-Subtitle-Sync of the last cue answer ("" before one) */
    val sync: String = "",
) {
    val selected: SubtitleTrack? get() = selectedId?.let { id -> tracks.firstOrNull { it.id == id } }

    /** Normalized language of the selected subtitle (the panel's checked row), null when off. */
    val selectedLanguage: String?
        get() = selected?.let { Languages.norm(it.language).ifEmpty { it.language } }
}

/** One-off things the player reacts to. */
sealed interface SubtitleEvent {
    /**
     * After the retries the profile's language [lang] is still missing while the audio is in another language:
     * the player asks `/api/torrent-stream/subalt` for a release that carries it (again after each late check).
     */
    data class Missing(val lang: String) : SubtitleEvent

    /** A late upload in [lang] arrived (and shows unless the viewer chose another): toast, drop the subalt row. */
    data class Found(val lang: String) : SubtitleEvent
}
