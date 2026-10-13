package com.nextflix.tv.feature.player

import com.nextflix.tv.feature.subtitles.Languages

// Default audio track (webOS lib/subtitles.ts pickAudioTrack, same rules as the web player's audioPick.ts).

// Words a track title uses for a language ("English 5.1", "Rus MVO")
private val LANG_WORDS = mapOf(
    "en" to listOf("english", "eng"),
    "tr" to listOf("turkish", "türkçe", "turkce", "tur"),
    "ru" to listOf("russian", "rus", "русский"),
    "es" to listOf("spanish", "español", "spa"),
    "fr" to listOf("french", "français", "fre"),
    "de" to listOf("german", "deutsch", "ger"),
    "it" to listOf("italian", "italiano", "ita"),
    "ja" to listOf("japanese", "jpn"),
    "ko" to listOf("korean", "kor"),
    "zh" to listOf("chinese", "mandarin"),
    "hi" to listOf("hindi"),
    "pt" to listOf("portuguese", "português", "por"),
    "uk" to listOf("ukrainian", "ukr"),
)
private val ORIGINAL_WORD = Regex("(^|[^a-z])(orig|orij)|оригинал", RegexOption.IGNORE_CASE)
private val DUB_WORD = Regex(
    "(^|[^a-z])(dub|dubbed|mvo|dvo|avo|voice.?over|dublaj)([^a-z]|$)|дубл|многоголос|двухголос|закадр",
    RegexOption.IGNORE_CASE,
)
private val COMMENTARY = Regex("comment|yorum|коммент", RegexOption.IGNORE_CASE)
private val WORD_SPLIT = Regex("[\\s.,:;()\\[\\]/|+_-]+")

private fun words(title: String) = title.lowercase().split(WORD_SPLIT).filter { it.isNotEmpty() }

/**
 * Default track, first match wins: the last picked language (not with the "original" preference), Turkish for a
 * Turkish profile (idem), a track tagged with the original language, one whose title says "Original" or names the
 * original language, an untagged track (usually the original) over ones tagged with another language, the file's
 * default track, the first.
 */
internal fun pickAudioTrack(
    tracks: List<AudioTrack>,
    profileLang: String,
    originalLang: String?,
    prefLang: String?,
    norm: (String?) -> String = ::langOf,
): Int {
    if (tracks.isEmpty()) return 0
    fun find(lang: String?): AudioTrack? {
        val l = norm(lang)
        return if (l.isEmpty()) null else tracks.firstOrNull { norm(it.language) == l }
    }
    val keepOriginal = prefLang == "original"
    val orig = norm(originalLang)
    val byTitle = {
        val names = LANG_WORDS[orig].orEmpty()
        tracks.firstOrNull { t ->
            t.title.isNotEmpty() && !COMMENTARY.containsMatchIn(t.title) &&
                (ORIGINAL_WORD.containsMatchIn(t.title) || words(t.title).any { it in names })
        }
    }
    val untagged = {
        val plain = tracks.filter { norm(it.language).isEmpty() && !COMMENTARY.containsMatchIn(it.title) }
        val clean = plain.filterNot { DUB_WORD.containsMatchIn(it.title) }
        val pool = clean.ifEmpty { plain }
        pool.firstOrNull { it.default } ?: pool.firstOrNull()
    }
    val pick = (if (keepOriginal) null else find(prefLang))
        ?: (if (!keepOriginal && profileLang == "tr") find("tr") else null)
        ?: find(orig)
        ?: byTitle()
        ?: (if (orig.isNotEmpty()) untagged() else null)
        ?: tracks.firstOrNull { it.default }
        ?: tracks.first()
    return pick.index
}

private val NO_LANGUAGE = setOf("und", "zxx", "mul", "mis")

/** ISO 639-1 code of a track or subtitle language ("eng" -> "en"); "" for none or undetermined. */
internal fun langOf(code: String?): String = Languages.norm(code).takeUnless { it in NO_LANGUAGE }.orEmpty()

private val ATMOS_CODEC = Regex("e-?ac-?3|truehd", RegexOption.IGNORE_CASE)

/** "Atmos" / "7.1" / "5.1" for the track as it reaches the TV: Atmos only when copied; converted audio is 5.1 at most. */
internal fun audioBadge(track: AudioTrack?, atmosRelease: Boolean): String {
    if (track == null) return ""
    if (track.direct && atmosRelease && ATMOS_CODEC.containsMatchIn(track.codec)) return "Atmos"
    if (track.channels >= 8) return if (track.direct) "7.1" else "5.1"
    return if (track.channels >= 6) "5.1" else ""
}
