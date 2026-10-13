package com.nextflix.tv.feature.subtitles

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.Localized

/** Subtitle texts of the player (webOS lib/i18n.ts player.*), for the Audio & Subtitles panel and toasts. */
interface SubtitleStrings {
    val subtitles: String
    val off: String
    val searching: String
    val none: String
    /** suffix of an AI translation row: "Turkish · AI translation" */
    val aiTranslation: String
    /** note while the lines at the position are being translated */
    val aiTranslating: String
    /** toast when a late subtitle in the profile's language arrived */
    val found: String
    val subAltRow: String
    val subAltSwitching: String
    val switchedSubtitles: String
    val timing: String
    val timingHint: String
    /** "+1.5 s" / "+1,5 sn" */
    fun seconds(value: Double): String
}

private object SubtitleEn : SubtitleStrings {
    override val subtitles = "Subtitles"
    override val off = "Off"
    override val searching = "Looking for subtitles..."
    override val none = "No subtitles found"
    override val aiTranslation = "AI translation"
    override val aiTranslating = "Translating subtitles with AI..."
    override val found = "English subtitles found"
    override val subAltRow = "English (another version) – switch"
    override val subAltSwitching = "Switching…"
    override val switchedSubtitles = "Switched to the version with English subtitles"
    override val timing = "Subtitle timing"
    override val timingHint = "Left: earlier · Right: later · OK: reset"
    override fun seconds(value: Double) = "${SubtitleRules.delayText(value, AppLanguage.En)} s"
}

private object SubtitleTr : SubtitleStrings {
    override val subtitles = "Altyazılar"
    override val off = "Kapalı"
    override val searching = "Altyazılar aranıyor..."
    override val none = "Altyazı bulunamadı"
    override val aiTranslation = "AI çevirisi"
    override val aiTranslating = "Altyazı AI ile çevriliyor..."
    override val found = "Türkçe altyazı bulundu"
    override val subAltRow = "Türkçe (başka sürümde) – geçiş yap"
    override val subAltSwitching = "Geçiliyor…"
    override val switchedSubtitles = "Türkçe altyazılı sürüme geçildi"
    override val timing = "Altyazı zamanlaması"
    override val timingHint = "Sol: daha erken · Sağ: daha geç · OK: sıfırla"
    override fun seconds(value: Double) = "${SubtitleRules.delayText(value, AppLanguage.Tr)} sn"
}

val subtitleStrings = Localized<SubtitleStrings>(en = SubtitleEn, tr = SubtitleTr)

/** Panel label of a subtitle row: the language name (or the provider's label), plus "AI translation". */
fun SubtitleTrack.menuLabel(language: AppLanguage): String {
    val code = Languages.norm(this.language)
    val name = if (code.isNotEmpty()) Languages.name(language, code) else label.ifEmpty { this.language }
    return if (isAi) "$name · ${subtitleStrings[language].aiTranslation}" else name
}
