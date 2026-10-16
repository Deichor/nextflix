package com.nextflix.tv.feature.subtitles

import java.text.Normalizer
import kotlin.math.abs

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

    /**
     * How an answer moved the cues of another ([cueShift]): the median start change of the cues matched by text
     * (0.1 s), how many moved by 0.1 s or more and how many matched. Of the moved cues: the first and last start in
     * the new answer (0.1 s), the median and the largest change (0.01 s); with a position also the moved cues near it
     * and the start change of the cue showing there (0 when it stayed; null when none shows or its line is gone).
     */
    data class Moves(
        val shift: Double?,
        val moved: Int,
        val matched: Int,
        val from: Double? = null,
        val to: Double? = null,
        val median: Double? = null,
        val max: Double? = null,
        val near: Int? = null,
        val here: Double? = null,
    )

    /** Whether an answer is worth a `subs` event, and how it moved the cues (only when some moved). */
    data class Change(val log: Boolean, val moves: Moves? = null)

    data class DelayChange(val subdelay: Double, val from: Double)

    private const val MAX_TOKEN = 40

    /** Header value when the server sent none. */
    const val SYNC_NONE = "-"

    /** Polls that only moved cues (a live timing step) away from the viewer are logged at most this often. */
    const val MOVE_LOG_MS = 60_000L

    /** Moved cues this close to the position (s before, after) are logged at once. */
    const val NEAR_BEFORE_S = 5.0
    const val NEAR_AFTER_S = 120.0

    /** Delay changes are logged once they stopped for this long. */
    const val DELAY_SETTLE_MS = 3_000L

    private val MARKS = Regex("[̀-ͯ]")
    private val UNSAFE = Regex("[^A-Za-z0-9._:/+-]+")

    private fun round1(n: Double): Double = Math.round(n * 10) / 10.0
    private fun round2(n: Double): Double = Math.round(n * 100) / 100.0

    /** A log-safe token: ASCII (Turkish letters folded), other runs become "_", at most [max] chars, "-" when empty. */
    fun token(s: String?, max: Int = MAX_TOKEN): String {
        var out = Normalizer.normalize(s.orEmpty().replace('ı', 'i'), Normalizer.Form.NFD)
        out = MARKS.replace(out, "")
        out = UNSAFE.replace(out, "_").trimStart('_').take(max).trimEnd('_')
        return out.ifEmpty { "-" }
    }

    /**
     * A subtitle id as logged: 8 hex chars of its FNV-1a hash (32 bit, over its UTF-16 chars; the ids are ASCII).
     * Cut ids would not do: every subtitle timed for one file starts with the same chars.
     */
    fun idHash(id: String): String {
        var h = 0x811c9dc5.toInt()
        for (c in id) h = (h xor c.code) * 0x01000193
        return (h.toLong() and 0xffffffffL).toString(16).padStart(8, '0')
    }

    /** X-Subtitle-Sync as logged: the value, or "-" without one. */
    fun syncValue(header: String?): String {
        val h = header?.trim().orEmpty()
        return if (h.isNotEmpty()) token(h, 16) else SYNC_NONE
    }

    /** Start of the first and the last cue (0.1 s). */
    fun cueSpan(cues: List<Cue>): Pair<Double, Double>? =
        if (cues.isEmpty()) null else round1(cues.first().start) to round1(cues.last().start)

    /**
     * How a new answer moved the cues of the last one, matched by text (each text's first cue); [at]: the file time
     * shown (position minus the subtitle delay), for the cues near it and the one showing.
     */
    fun cueShift(prev: List<Cue>, next: List<Cue>, at: Double? = null): Moves {
        val was = HashMap<String, Double>()
        for (c in prev) was.putIfAbsent(c.text, c.start)
        val seen = HashSet<String>()
        val d = ArrayList<Double>()
        val m = ArrayList<Double>()
        var firstMoved = 0.0
        var lastMoved = 0.0
        var largest = 0.0
        var near = 0
        fun isNear(s: Double) = at != null && s >= at - NEAR_BEFORE_S && s <= at + NEAR_AFTER_S
        for (c in next) {
            if (!seen.add(c.text)) continue
            val s = was[c.text] ?: continue
            val x = c.start - s
            d.add(x)
            if (abs(x) < 0.1) continue
            if (m.isEmpty()) firstMoved = c.start
            lastMoved = c.start
            m.add(x)
            if (abs(x) > abs(largest)) largest = x
            if (isNear(s) || isNear(c.start)) near++
        }
        if (d.isEmpty()) return Moves(null, 0, 0)
        d.sort()
        val shift = round1(d[d.size shr 1])
        if (m.isEmpty()) return Moves(shift, 0, d.size)
        m.sort()
        val moves = Moves(shift, m.size, d.size, round1(firstMoved), round1(lastMoved), round2(m[m.size shr 1]), round2(largest))
        if (at == null) return moves
        // The line on screen, matched to its nearest start in the new answer (its text may come more than once)
        val shown = CueLookup.active(prev, at).firstOrNull()
        val here = shown?.let { s -> next.filter { it.text == s.text }.map { it.start - s.start }.minByOrNull { abs(it) } }
        return moves.copy(near = near, here = here?.let { round2(it) })
    }

    /**
     * Whether an answer is worth a `subs` event against the last logged one ([sinceMs] ago): another subtitle,
     * the first cues, partial or sync changed, cues replaced, or moved (at once near [at], the file time shown, else
     * at most every [MOVE_LOG_MS], the moves then add up). A live track that only grew is not.
     */
    fun change(prev: Shown?, next: Shown, sinceMs: Long = Long.MAX_VALUE, at: Double? = null): Change {
        if (prev == null || prev.id != next.id) return Change(true)
        if (prev.cues === next.cues) return Change(prev.partial != next.partial || prev.sync != next.sync)
        val s = cueShift(prev.cues, next.cues, at)
        val moves = s.takeIf { it.moved > 0 }
        // Most old cues gone: the server served another subtitle
        val replaced = prev.cues.isNotEmpty() && s.matched < minOf(prev.cues.size, next.cues.size) / 2.0
        if (prev.partial != next.partial || prev.sync != next.sync || replaced) return Change(true, moves)
        if (moves != null) return if (sinceMs >= MOVE_LOG_MS || (moves.near ?: 0) > 0) Change(true, moves) else Change(false)
        return Change(prev.cues.isEmpty() && next.cues.isNotEmpty())
    }

    /** Fields of a `subs` event; the list item's fields only when a subtitle starts (its polls keep them). */
    fun subsFields(item: SubtitleTrack?, shown: Shown, why: Why, delay: Double, moves: Moves? = null): Map<String, Any> =
        buildMap {
            put("id", idHash(shown.id))
            put("cues", shown.cues.size)
            put("partial", shown.partial)
            if (item != null && why != Why.Poll) {
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
            if (moves != null) {
                moves.shift?.let { put("shift", it) }
                put("moved", moves.moved)
                moves.from?.let { put("mfrom", it) }
                moves.to?.let { put("mto", it) }
                moves.median?.let { put("mshift", it) }
                moves.max?.let { put("mmax", it) }
                moves.near?.let { put("near", it) }
                moves.here?.let { put("here", it) }
            }
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
