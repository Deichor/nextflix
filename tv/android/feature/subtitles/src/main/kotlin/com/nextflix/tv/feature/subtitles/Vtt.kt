package com.nextflix.tv.feature.subtitles

/** WebVTT as the server sends it (webOS lib/subtitles.ts parseVtt). */
internal object Vtt {
    /** A live or still-timed answer: more or better cues follow, ask again. */
    const val PARTIAL_NOTE = "NOTE nextflix-partial"

    /** An AI translation still missing the lines at the position. */
    const val AI_NOTE = "NOTE nextflix-ai-translating"

    private val TIME = Regex("""(?:(\d+):)?(\d{1,2}):(\d{2})[.,](\d{1,3})""")
    private val BLANK_LINES = Regex("\n{2,}")

    fun parseTime(s: String): Double? {
        val m = TIME.find(s) ?: return null
        val (h, min, sec, frac) = m.destructured
        val ms = frac.padEnd(3, '0').toInt()
        return (h.ifEmpty { "0" }.toLong() * 3600 + min.toInt() * 60 + sec.toInt()) + ms / 1000.0
    }

    /**
     * Cues sorted by start. Blocks without a timing line (header, NOTE, STYLE) are skipped, cue settings after the
     * end time are ignored, cues without text or with end <= start are dropped. The text is kept as sent (markup
     * is read by [CueMarkup]).
     */
    fun parse(text: String): List<Cue> {
        val cues = ArrayList<Cue>()
        val normalized = text.removePrefix("﻿").replace("\r\n", "\n").replace('\r', '\n')
        for (block in normalized.split(BLANK_LINES)) {
            val lines = block.split('\n')
            val at = lines.indexOfFirst { "-->" in it }
            if (at < 0) continue
            val parts = lines[at].split("-->")
            val start = parseTime(parts[0]) ?: continue
            val end = parseTime(parts.getOrElse(1) { "" }) ?: continue
            if (end <= start) continue
            val body = lines.subList(at + 1, lines.size).joinToString("\n").trim()
            if (body.isNotEmpty()) cues.add(Cue(start, end, body))
        }
        cues.sortBy { it.start }
        return cues
    }
}

/** Which cues show when (webOS lib/subtitles.ts activeCues, player/cueTiming.ts). Cues sorted by start. */
object CueLookup {
    /** The overlay checks at least this often (seeks, pauses, a new cue list). */
    const val CHECK_MAX_MS = 100L

    /** ...and this long after a cue boundary, so the position has passed it. */
    private const val EDGE_MS = 4L

    /** How far back overlapping cues are looked for. */
    private const val LOOK_BACK = 12

    /** Index of the last cue starting at or before [t], -1 when none. */
    private fun lastStartedAt(cues: List<Cue>, t: Double): Int {
        var lo = 0
        var hi = cues.size - 1
        var last = -1
        while (lo <= hi) {
            val mid = (lo + hi) ushr 1
            if (cues[mid].start <= t) {
                last = mid
                lo = mid + 1
            } else {
                hi = mid - 1
            }
        }
        return last
    }

    /** Cues showing at time [t], in start order. */
    fun active(cues: List<Cue>, t: Double): List<Cue> {
        val last = lastStartedAt(cues, t)
        if (last < 0) return emptyList()
        val out = ArrayList<Cue>(2)
        var i = last
        while (i >= 0 && i > last - LOOK_BACK) {
            if (cues[i].end > t) out.add(cues[i])
            i--
        }
        out.reverse()
        return out
    }

    /** Seconds from [t] until the cues shown change (a start, or the end of a shown cue); infinite when never. */
    fun nextChange(cues: List<Cue>, t: Double): Double {
        val lo = lastStartedAt(cues, t) + 1
        var next = if (lo < cues.size) cues[lo].start else Double.POSITIVE_INFINITY
        var i = lo - 1
        while (i >= 0 && i > lo - 1 - LOOK_BACK) {
            if (cues[i].end > t) next = minOf(next, cues[i].end)
            i--
        }
        return next - t
    }

    /** Delay of the next check: right after the next boundary, at most [CHECK_MAX_MS]. */
    fun checkDelayMs(cues: List<Cue>, t: Double): Long {
        val change = nextChange(cues, t)
        if (change.isInfinite()) return CHECK_MAX_MS
        val ms = Math.round(change * 1000) + EDGE_MS
        return ms.coerceIn(EDGE_MS, CHECK_MAX_MS)
    }
}
