package com.nextflix.tv.feature.subtitles

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.prefs.SubtitleChoice
import java.text.Collator

/** Pure subtitle list rules (webOS lib/subtitles.ts, player/playRules.ts, the subtitle parts of engine.ts). */
object SubtitleRules {
    /** Subtitle timing limit in seconds, both ways. */
    const val MAX_DELAY_S = 60.0

    /** Step of the panel's timing row. */
    const val DELAY_STEP_S = 0.5

    private fun List<SubtitleTrack>.ofLang(lang: String): List<SubtitleTrack> {
        val l = Languages.norm(lang)
        return filter { Languages.norm(it.language) == l }
    }

    /** Whether a real subtitle of [lang] is listed; an AI translation does not count (a real one still replaces it). */
    fun hasLang(items: List<SubtitleTrack>, lang: String): Boolean {
        val l = Languages.norm(lang)
        return items.any { it.source != SOURCE_AI && Languages.norm(it.language) == l }
    }

    /** Both lists by id, the newer answer's entries first. */
    fun merge(prev: List<SubtitleTrack>, next: List<SubtitleTrack>): List<SubtitleTrack> {
        val ids = next.mapTo(HashSet()) { it.id }
        return next + prev.filter { it.id !in ids }
    }

    /** Best subtitle of [lang]: not forced (unless only forced ones exist), synced first. */
    fun best(items: List<SubtitleTrack>, lang: String): String? {
        val of = items.ofLang(lang)
        val pool = of.filter { !it.forced }.ifEmpty { of }
        return (pool.firstOrNull { it.synced } ?: pool.firstOrNull())?.id
    }

    data class Late(val items: List<SubtitleTrack>, val arrived: Boolean, val pick: String?)

    /**
     * A later list brought subtitles of [lang] that the shown list lacked: the merged list, whether [lang] is new,
     * and the subtitle to show by itself when the viewer has not chosen one.
     */
    fun late(shown: List<SubtitleTrack>, fresh: List<SubtitleTrack>, lang: String, viewerChose: Boolean): Late {
        val items = merge(shown, fresh)
        val arrived = !hasLang(shown, lang) && hasLang(items, lang)
        if (!arrived || viewerChose) return Late(items, arrived, null)
        val pool = items.ofLang(lang).filter { !it.forced }
        val pick = pool.firstOrNull { it.synced } ?: pool.firstOrNull()
        return Late(items, true, pick?.id)
    }

    /** Last choice of the viewer; without one a Turkish profile gets Turkish unless the audio is Turkish. */
    fun pick(items: List<SubtitleTrack>, profileLang: AppLanguage, pref: SubtitleChoice?, audioLang: String?): String? = when (pref) {
        SubtitleChoice.Off -> null
        is SubtitleChoice.Language ->
            if (pref.id != null && items.any { it.id == pref.id }) pref.id else best(items, pref.lang)
        null -> if (profileLang == AppLanguage.Tr && Languages.norm(audioLang) != "tr") best(items, "tr") else null
    }

    /** Another subtitle of the failed one's language: not one that failed, same forced flag, another provider first. */
    fun fallback(items: List<SubtitleTrack>, failedId: String, failed: Set<String>): String? {
        val bad = items.firstOrNull { it.id == failedId } ?: return null
        val lang = Languages.norm(bad.language)
        return items
            .filter { Languages.norm(it.language) == lang && it.id !in failed && it.id != failedId && it.forced == bad.forced }
            .sortedBy { (if (it.source != bad.source) 0 else 2) + (if (it.synced) 0 else 1) }
            .firstOrNull()?.id
    }

    /** One row per language (its best subtitle), [order]'s languages first, then by name in the UI language. */
    fun perLanguage(items: List<SubtitleTrack>, order: List<String>, language: AppLanguage): List<SubtitleTrack> {
        val collator = Collator.getInstance(language.locale)
        fun rank(s: SubtitleTrack): Int = order.indexOf(Languages.norm(s.language)).let { if (it >= 0) it else order.size }
        val names = HashMap<String, String>()
        fun name(s: SubtitleTrack): String {
            val code = Languages.norm(s.language)
            return names.getOrPut(code) { Languages.name(language, code) }
        }
        val sorted = items.withIndex().sortedWith { a, b ->
            rank(a.value).compareTo(rank(b.value)).takeIf { it != 0 }
                ?: collator.compare(name(a.value), name(b.value)).takeIf { it != 0 }
                ?: a.index.compareTo(b.index)
        }
        val seen = HashSet<String>()
        return sorted.map { it.value }.filter { seen.add(Languages.norm(it.language).ifEmpty { it.language }) }
    }

    enum class CueAnswer { Show, Fallback, Retry, Stop, AiOff }

    /**
     * An answer of a subtitle file. Empty: another subtitle only when this one never answered (a quota or broken
     * file); one that already answered asks again after a network error, 5xx or 429 while polling ([retryWaitMs]) and
     * stops after another 4xx. [ai]: the AI translation cannot be used now (502 `ai: true`): another subtitle like
     * after a quota, else the same one after AI_OFF_RETRY_MS. [loads]: earlier answers.
     */
    fun cueAnswer(cues: Int, partial: Boolean, loads: Int, status: Int = 200, ai: Boolean = false): CueAnswer = when {
        cues > 0 || partial -> CueAnswer.Show
        ai -> CueAnswer.AiOff
        loads == 0 -> CueAnswer.Fallback
        status in 400..499 && status != 429 -> CueAnswer.Stop
        else -> CueAnswer.Retry
    }

    /** Wait before a [CueAnswer.Retry]: errors back off ([retryMs], [n]: errors in a row), an empty answer waits a live poll. */
    fun retryWaitMs(failed: Boolean, status: Int, n: Int): Long =
        if (failed || status == 429) retryMs(n) else SubtitleRepository.LIVE_CUES_POLL_MS

    /** An unusable AI translation is asked again this late. */
    const val AI_OFF_RETRY_MS = 5 * 60 * 1000L

    /** Wait before asking a file again after its n-th server or network error in a row: 5 s doubling up to 5 min. */
    fun retryMs(n: Int): Long = minOf(5 * 60 * 1000L, 5000L shl (n - 1).coerceIn(0, 20))

    /** X-Subtitle-Sync states whose timing no longer changes (docs/API.md). */
    private val FINAL_SYNC = setOf("synced", "unchanged", "replaced", "poor", "original")

    /** An answer of a subtitle file that will not change: complete and finally timed (only these are kept). */
    fun finalCues(partial: Boolean, sync: String): Boolean = !partial && sync in FINAL_SYNC

    /**
     * When to ask a subtitle file again (ms), null once its answer is final: partial every LIVE_CUES_POLL_MS
     * (AI_CUES_POLL_MS while the lines at the position are being translated, PENDING_CUES_POLL_MS while still
     * untimed in the first PENDING_FAST_FOR_MS after it was put on: [shownMs]), complete but with timing that may
     * still change (live, pending, failed) every PROVISIONAL_CUES_POLL_MS while the viewer watches.
     */
    fun pollMs(partial: Boolean, sync: String, translating: Boolean = false, shownMs: Long = Long.MAX_VALUE): Long? = when {
        partial && translating -> SubtitleRepository.AI_CUES_POLL_MS
        partial && sync == "pending" && shownMs < SubtitleRepository.PENDING_FAST_FOR_MS -> SubtitleRepository.PENDING_CUES_POLL_MS
        partial -> SubtitleRepository.LIVE_CUES_POLL_MS
        finalCues(false, sync) -> null
        else -> SubtitleRepository.PROVISIONAL_CUES_POLL_MS
    }

    /** Delay as kept: within ±[MAX_DELAY_S], rounded to 0.1 s. */
    fun clampDelay(seconds: Double): Double {
        if (seconds.isNaN()) return 0.0
        return Math.round(seconds.coerceIn(-MAX_DELAY_S, MAX_DELAY_S) * 10) / 10.0
    }

    /** "+1,5" / "−0.5" / "0.0" for the timing row. */
    fun delayText(seconds: Double, language: AppLanguage): String {
        val n = String.format(java.util.Locale.ROOT, "%.1f", kotlin.math.abs(seconds))
        val sign = when {
            seconds > 0 -> "+"
            seconds < 0 -> "−"
            else -> ""
        }
        return sign + if (language == AppLanguage.Tr) n.replace('.', ',') else n
    }

    const val SOURCE_AI = "ai"
}
