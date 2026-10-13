package com.nextflix.tv.feature.player

// Buffer health of the playing stream (webOS player/bufferHealth.ts): how far the buffer reaches past the
// position and whether the download keeps pace with playback. Pure logic, fed once per second by the engine.

internal enum class BufferState { Healthy, Draining, Critical }

internal data class BufferSample(
    /** wall clock (ms) */
    val at: Long,
    /** playback position in the file (s) */
    val pos: Double,
    /** end of the buffer that holds pos (s, file time); pos when nothing is buffered there */
    val end: Double,
    /** the player is downloading; false: it stopped on its own (buffer full) */
    val fetching: Boolean,
    /** the buffer reaches the end of the file */
    val atEnd: Boolean = false,
)

internal data class BufferVerdict(
    val state: BufferState,
    /** seconds buffered ahead of the position */
    val ahead: Double,
    /** buffered end growth per second of playback over the window; null until it spans enough playback */
    val growth: Double?,
    /** how long the stream has been healthy (ms); 0 while not */
    val healthyMs: Long,
)

internal class BufferMonitor {
    private val win = ArrayDeque<BufferSample>()
    private var healthySince = 0L
    private var idleAt = 0L

    /** Forget everything (new stream, seek, source switch). */
    fun reset() {
        win.clear()
        healthySince = 0
        idleAt = 0
    }

    fun push(s: BufferSample): BufferVerdict {
        val ahead = maxOf(0.0, s.end - s.pos)
        val last = win.lastOrNull()
        // A jump in position (seek) or a gap in sampling breaks the comparison
        if (last != null && (s.at - last.at > GAP_MS || s.pos < last.pos - 0.5 || s.pos - last.pos > (s.at - last.at) / 1000.0 * 2 + 2)) {
            reset()
        }
        // The player paused the download itself: a shrinking buffer says nothing about the network
        if (!s.fetching) {
            win.clear()
            idleAt = s.at
        }
        win.addLast(s)
        while (win.size > 2 && s.at - win[1].at >= WINDOW_MS) win.removeFirst()

        val first = win.first()
        val span = s.pos - first.pos
        val growth = if (span >= MIN_SPAN_S) (s.end - first.end) / span else null

        var state = BufferState.Healthy
        var known = growth != null
        if (s.atEnd || !s.fetching) {
            known = true
        } else if (idleAt != 0L && s.at - idleAt < IDLE_GRACE_MS) {
            known = true
            if (ahead < IDLE_MIN_AHEAD_S) state = BufferState.Critical
        } else if (growth != null) {
            if (growth < 1 && ahead < CRITICAL_AHEAD_S) state = BufferState.Critical
            else if (growth < DRAIN_GROWTH || (growth < 1 && ahead < DRAIN_AHEAD_S)) state = BufferState.Draining
        }

        // A window that is still filling neither starts nor ends a healthy stretch
        if (state != BufferState.Healthy) healthySince = 0
        else if (known && healthySince == 0L) healthySince = s.at
        return BufferVerdict(state, ahead, growth, if (healthySince != 0L) s.at - healthySince else 0)
    }

    companion object {
        /** sliding window the growth is measured over */
        const val WINDOW_MS = 10_000L
        /** playback the window needs before its growth counts */
        const val MIN_SPAN_S = 6.0
        /** below this much buffer, any shrinking is draining */
        const val DRAIN_AHEAD_S = 25.0
        /** growth below this is draining whatever the buffer */
        const val DRAIN_GROWTH = 0.9
        /** below this much buffer while not keeping pace: critical, the stall is seconds away */
        const val CRITICAL_AHEAD_S = 6.0
        /** a pause between samples longer than this restarts the window */
        const val GAP_MS = 3000L
        /** after the download idled on its own it comes in bursts: judged again once it fetched this long */
        const val IDLE_GRACE_MS = 15_000L
        /** fetching with less than this buffered (also within the grace) is critical */
        const val IDLE_MIN_AHEAD_S = 1.0
    }
}
