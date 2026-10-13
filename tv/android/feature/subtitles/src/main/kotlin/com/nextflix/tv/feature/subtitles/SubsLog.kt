package com.nextflix.tv.feature.subtitles

import java.text.Normalizer

/**
 * Subtitle diagnostics for the playback log: fields of the `subs`, `subs-none` and `subdelay` events
 * (webOS player/subsLog.ts, same as the web player). The server keeps string values to 40 chars of
 * [A-Za-z0-9._:/+-] and drops events with more than 16 fields, so values are made safe here.
 */
object SubsLog {
    /** Why a subtitle was shown. */
    enum class Why(val token: String) { Open("open"), Poll("poll"), User("user"), Auto("auto"), Fallback("fallback") }

    /** One answer of a subtitle file as shown. */
    data class Shown(val id: String, val cues: List<Cue>, val partial: Boolean, val sync: String)

    data class Change(val log: Boolean, val shift: Double? = null, val moved: Int? = null)

    data class Shift(val shift: Double?, val moved: Int, val matched: Int)

    data class DelayChange(val subdelay: Double, val from: Double)

    private const val MAX_TOKEN = 40

    /** Header value when the server sent none. */
    const val SYNC_NONE = "-"

    /** Polls that only moved cues (a live timing step) are logged at most this often. */
    const val MOVE_LOG_MS = 60_000L

    /** Delay changes are logged once they stopped for this long. */
    const val DELAY_SETTLE_MS = 3_000L

    private val MARKS = Regex("[̀-ͯ]")
    private val UNSAFE = Regex("[^A-Za-z0-9._:/+-]+")

    private fun round1(n: Double): Double = Math.round(n * 10) / 10.0

    /** A log-safe token: ASCII (Turkish letters folded), other runs become "_", at most [max] chars, "-" when empty. */
    fun token(s: String?, max: Int = MAX_TOKEN): String {
        var out = Normalizer.normalize(s.orEmpty().replace('ı', 'i'), Normalizer.Form.NFD)
        out = MARKS.replace(out, "")
        out = UNSAFE.replace(out, "_").trimStart('_').take(max).trimEnd('_')
        return out.ifEmpty { "-" }
    }

    /** X-Subtitle-Sync as logged: the value, or "-" without one. */
    fun syncValue(header: String?): String {
        val h = header?.trim().orEmpty()
        return if (h.isNotEmpty()) token(h, 16) else SYNC_NONE
    }

    /** Start of the first and the last cue (0.1 s). */
    fun cueSpan(cues: List<Cue>): Pair<Double, Double>? =
        if (cues.isEmpty()) null else round1(cues.first().start) to round1(cues.last().start)

    /** How a new answer moved the cues of the last one, matched by text (each text's first cue). */
    fun cueShift(prev: List<Cue>, next: List<Cue>): Shift {
        val at = HashMap<String, Double>()
        for (c in prev) at.putIfAbsent(c.text, c.start)
        val seen = HashSet<String>()
        val d = ArrayList<Double>()
        var moved = 0
        for (c in next) {
            if (!seen.add(c.text)) continue
            val s = at[c.text] ?: continue
            val x = c.start - s
            d.add(x)
            if (kotlin.math.abs(x) >= 0.1) moved++
        }
        if (d.isEmpty()) return Shift(null, moved, 0)
        d.sort()
        return Shift(round1(d[d.size shr 1]), moved, d.size)
    }

    /**
     * Whether an answer is worth a `subs` event against the last logged one ([sinceMs] ago): another subtitle,
     * the first cues, partial or sync changed, cues replaced, or moved (at most every [MOVE_LOG_MS]). A live
     * track that only grew is not.
     */
    fun change(prev: Shown?, next: Shown, sinceMs: Long = Long.MAX_VALUE): Change {
        if (prev == null || prev.id != next.id) return Change(true)
        if (prev.cues === next.cues) return Change(prev.partial != next.partial || prev.sync != next.sync)
        val s = cueShift(prev.cues, next.cues)
        val shift = if (s.moved > 0) s.shift else null
        val moved = if (s.moved > 0) s.moved else null
        // Most old cues gone: the server served another subtitle
        val replaced = prev.cues.isNotEmpty() && s.matched < minOf(prev.cues.size, next.cues.size) / 2.0
        if (prev.partial != next.partial || prev.sync != next.sync || replaced) return Change(true, shift, moved)
        if (s.moved > 0) return if (sinceMs >= MOVE_LOG_MS) Change(true, shift, moved) else Change(false)
        return Change(prev.cues.isEmpty() && next.cues.isNotEmpty())
    }

    /** Fields of a `subs` event. */
    fun subsFields(item: SubtitleTrack?, shown: Shown, why: Why, delay: Double, change: Change? = null): Map<String, Any> =
        buildMap {
            put("id", token(shown.id, 24))
            put("cues", shown.cues.size)
            put("partial", shown.partial)
            if (item != null) {
                put("lang", token(item.language, 8))
                put("provider", token(item.source, 16))
                put("name", token(item.release.ifEmpty { item.label }))
            }
            put("sync", shown.sync)
            put("why", why.token)
            cueSpan(shown.cues)?.let { (first, last) ->
                put("first", first)
                put("last", last)
            }
            put("subdelay", delay)
            change?.shift?.let { put("shift", it) }
            change?.moved?.let { put("moved", it) }
        }

    /** Fields of a `subs-none` event: why (off, open, failed, missing), the profile language and what the list has. */
    fun noneFields(why: String, lang: String, items: List<SubtitleTrack>?, extra: Map<String, Any> = emptyMap()): Map<String, Any> {
        val langs = LinkedHashSet<String>()
        items.orEmpty().forEach { s -> token(s.language, 8).takeIf { it != "-" }?.let(langs::add) }
        return buildMap {
            put("why", why)
            put("lang", token(lang, 8))
            put("n", items.orEmpty().size)
            if (langs.isNotEmpty()) put("have", token(langs.joinToString("+")))
            putAll(extra)
        }
    }

    /** Debounced subtitle delay changes: the settled value once [DELAY_SETTLE_MS] passed after the last change. */
    class DelayLog {
        private var logged = 0.0
        private var changedAt = -1L

        /** Current delay (seconds). */
        var current = 0.0
            private set

        /** The viewer changed the delay. */
        fun change(v: Double, now: Long) {
            current = v
            changedAt = now
        }

        /** The settled change to log, or null (nothing pending, still changing, or back where it was). [force]: now. */
        fun take(now: Long, force: Boolean = false): DelayChange? {
            if (changedAt < 0 || (!force && now - changedAt < DELAY_SETTLE_MS)) return null
            changedAt = -1
            if (current == logged) return null
            val out = DelayChange(current, logged)
            logged = current
            return out
        }

        /** The delay was set for another file: a pending change is returned first, [v] is the new baseline. */
        fun reset(v: Double, now: Long): DelayChange? {
            val pending = take(now, true)
            logged = v
            current = v
            return pending
        }
    }
}

/** Receives the subtitle events of the playback log (`subs`, `subs-none`, `subdelay`); fields are Number / String / Boolean. */
fun interface SubtitleLogSink {
    fun note(event: String, fields: Map<String, Any>)
}
