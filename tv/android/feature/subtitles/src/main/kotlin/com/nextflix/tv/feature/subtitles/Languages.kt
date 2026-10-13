package com.nextflix.tv.feature.subtitles

import com.nextflix.tv.core.i18n.AppLanguage
import java.util.Locale

/** Language codes and names for subtitle and audio menus (webOS lib/subtitles.ts normLang / languageName). */
object Languages {
    private val ISO639_2 = mapOf(
        "eng" to "en", "tur" to "tr", "spa" to "es", "fre" to "fr", "fra" to "fr", "ger" to "de", "deu" to "de",
        "ita" to "it", "por" to "pt", "rus" to "ru", "jpn" to "ja", "kor" to "ko", "chi" to "zh", "zho" to "zh",
        "cmn" to "zh", "yue" to "zh", "ara" to "ar", "hin" to "hi", "dut" to "nl", "nld" to "nl", "swe" to "sv",
        "nor" to "no", "nob" to "nb", "nno" to "nn", "dan" to "da", "fin" to "fi", "pol" to "pl", "cze" to "cs",
        "ces" to "cs", "slo" to "sk", "slk" to "sk", "hun" to "hu", "rum" to "ro", "ron" to "ro", "gre" to "el",
        "ell" to "el", "heb" to "he", "tha" to "th", "vie" to "vi", "ind" to "id", "may" to "ms", "msa" to "ms",
        "ukr" to "uk", "bul" to "bg", "hrv" to "hr", "srp" to "sr", "slv" to "sl", "est" to "et", "lav" to "lv",
        "lit" to "lt", "per" to "fa", "fas" to "fa", "ice" to "is", "isl" to "is", "tam" to "ta", "tel" to "te",
        "ben" to "bn", "urd" to "ur", "cat" to "ca", "baq" to "eu", "eus" to "eu", "glg" to "gl", "fil" to "fil",
        "tgl" to "tl", "aze" to "az", "kaz" to "kk", "geo" to "ka", "kat" to "ka", "arm" to "hy", "hye" to "hy",
        "alb" to "sq", "sqi" to "sq", "mac" to "mk", "mkd" to "mk", "bos" to "bs", "lat" to "la", "wel" to "cy",
        "cym" to "cy", "gle" to "ga", "kur" to "ku",
    )

    private val UNDETERMINED = setOf("und", "zxx", "mul", "mis")

    // Used when the platform has no name for a code
    private val NAMES = mapOf(
        "en" to ("English" to "İngilizce"), "tr" to ("Turkish" to "Türkçe"), "es" to ("Spanish" to "İspanyolca"),
        "fr" to ("French" to "Fransızca"), "de" to ("German" to "Almanca"), "it" to ("Italian" to "İtalyanca"),
        "pt" to ("Portuguese" to "Portekizce"), "ru" to ("Russian" to "Rusça"), "ja" to ("Japanese" to "Japonca"),
        "ko" to ("Korean" to "Korece"), "zh" to ("Chinese" to "Çince"), "ar" to ("Arabic" to "Arapça"),
        "hi" to ("Hindi" to "Hintçe"), "nl" to ("Dutch" to "Felemenkçe"), "sv" to ("Swedish" to "İsveççe"),
        "no" to ("Norwegian" to "Norveççe"), "da" to ("Danish" to "Danca"), "fi" to ("Finnish" to "Fince"),
        "pl" to ("Polish" to "Lehçe"), "cs" to ("Czech" to "Çekçe"), "hu" to ("Hungarian" to "Macarca"),
        "ro" to ("Romanian" to "Rumence"), "el" to ("Greek" to "Yunanca"), "he" to ("Hebrew" to "İbranice"),
        "th" to ("Thai" to "Tayca"), "vi" to ("Vietnamese" to "Vietnamca"), "id" to ("Indonesian" to "Endonezce"),
        "uk" to ("Ukrainian" to "Ukraynaca"), "bg" to ("Bulgarian" to "Bulgarca"), "hr" to ("Croatian" to "Hırvatça"),
        "sr" to ("Serbian" to "Sırpça"), "fa" to ("Persian" to "Farsça"),
    )

    /** ISO 639-1 base code from 639-1 / 639-2 / BCP 47 ("tur", "TR", "tr-TR" -> "tr"); "" for none or undetermined. */
    fun norm(code: String?): String {
        val c = code.orEmpty().trim().lowercase(Locale.ROOT).split('-', '_').first()
        if (c.isEmpty() || c in UNDETERMINED) return ""
        return if (c.length == 3) ISO639_2[c] ?: c else c
    }

    /** Name of a language in the UI language ("tr" -> "Turkish" / "Türkçe"); the code when unknown, "" for "". */
    fun name(language: AppLanguage, code: String): String {
        if (code.isEmpty()) return ""
        var name = runCatching { Locale.forLanguageTag(code).getDisplayLanguage(language.locale) }.getOrDefault("")
        if (name.isEmpty() || name.equals(code, ignoreCase = true)) {
            name = NAMES[code]?.let { if (language == AppLanguage.Tr) it.second else it.first } ?: code
        }
        return name.replaceFirstChar { it.titlecase(language.locale) }
    }
}
