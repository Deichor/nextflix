package com.nextflix.tv.feature.player

import com.nextflix.tv.core.stream.QualityCap
import kotlin.math.abs
import kotlin.math.floor
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow
import kotlin.math.roundToInt

// Pure decisions of the player (webOS player/playRules.ts, switchLength.ts, startBuffer.ts and engine helpers).

/** A start this close to the end counts as the end (seconds). */
internal const val END_ZONE_S = 10.0

/**
 * Where an opened source starts; null: the title is over. A first resume at the very end watches the title again
 * from 0; a switch or recovery that lands there means the title is over, never a replay from 0.
 */
internal fun openStart(startAt: Double, duration: Double, firstResume: Boolean): Double? {
    if (startAt <= 0) return 0.0
    if (duration <= 0 || startAt < duration - END_ZONE_S) return startAt
    return if (firstResume) 0.0 else null
}

/** Seeks act only on an opened source: not while another one is looked up, nor after a fatal error. */
internal fun canSeek(phase: Phase, fatal: Boolean, duration: Double, hash: String): Boolean =
    hash.isNotEmpty() && phase == Phase.Ready && !fatal && duration > 0

/** Position to save, or null: nothing of the item played yet (a failed start never overwrites progress) or under 10 s. */
internal fun progressToSave(
    finished: Boolean,
    itemPlayed: Boolean,
    duration: Double,
    exact: Boolean,
    position: Double,
    offset: Double,
): Double? {
    if (duration <= 0 || (!finished && !itemPlayed)) return null
    val pos = when {
        finished -> duration
        exact -> min(duration, position)
        else -> offset
    }
    return if (pos < 10) null else pos
}

/** The next episode is warmed once 10 minutes are left (85 % of episodes under 25 minutes). */
internal fun nextWarmDue(position: Double, duration: Double): Boolean {
    if (duration <= 0 || position <= 0) return false
    if (duration < 25 * 60) return position >= duration * 0.85
    return duration - position <= 10 * 60
}

// Releases of one title differ a little in length: up to max(30 s, 1 %) apart they are the same title; under
// 10 s apart the same time is the same moment, beyond that a position is mapped by the length ratio.
private const val SAME_TITLE_S = 30.0
private const val SAME_TITLE_SHARE = 0.01
private const val KEEP_TIME_S = 10.0

/** The new file can replace the playing one (unknown lengths always fit). */
internal fun lengthsMatch(playing: Double, next: Double): Boolean {
    if (playing <= 0 || next <= 0) return true
    return abs(next - playing) <= max(SAME_TITLE_S, SAME_TITLE_SHARE * max(playing, next))
}

/** Factor from a position in the playing file to the same moment in the new one. */
internal fun timeScale(playing: Double, next: Double): Double {
    if (playing <= 0 || next <= 0 || abs(next - playing) <= KEEP_TIME_S) return 1.0
    return next / playing
}

internal fun releaseHeight(quality: String?): Int = when (quality) {
    "2160p" -> 2160
    "1080p" -> 1080
    "720p" -> 720
    else -> 0
}

/** Resolution class of a probed video size. */
internal fun videoQuality(w: Int, h: Int): Int = when {
    w >= 3200 || h >= 1800 -> 2160
    w >= 1800 || h >= 1000 -> 1080
    w >= 1200 || h >= 700 -> 720
    w > 0 || h > 0 -> 480
    else -> 0
}

internal fun lowerCap(quality: Int): QualityCap? = when {
    quality >= 2160 -> QualityCap.P1080
    quality >= 1080 -> QualityCap.P720
    else -> null
}

internal fun resolutionLabel(w: Int, h: Int): String = when {
    w >= 3200 || h >= 1800 -> "4K"
    w >= 1800 || h >= 1000 -> "1080p"
    w >= 1200 || h >= 700 -> "720p"
    w > 0 || h > 0 -> "SD"
    else -> ""
}

/** What is really shown: the original when copied, at most 1080p SDR when the server converts it. */
internal fun qualityOf(video: VideoInfo?, source: StreamSource?, hevc: Boolean): QualityBadge? {
    if (video != null && video.width > 0) {
        val w = if (video.direct) video.width else min(1920, video.width)
        val h = (video.height * (w.toDouble() / video.width)).roundToInt()
        return QualityBadge(resolutionLabel(w, h), hdr = video.direct && video.hdr, dv = video.direct && video.dolbyVision > 0)
    }
    val q = source?.quality?.takeIf { it.isNotEmpty() } ?: return null
    return QualityBadge(if (q == "2160p") "4K" else q, hdr = source.hdr && hevc, dv = false)
}

/** Seek step of a Left / Right press: 10 s, held 30 s, then 60 s. [repeats]: presses in a row (0 = the first). */
internal fun scrubStep(repeats: Int): Int = when {
    repeats < 5 -> 10
    repeats < 15 -> 30
    else -> 60
}

internal const val MAX_SUB_DELAY_S = 60.0

/** Subtitle delay limited to ±60 s, on a 0.1 s grid. */
internal fun clampSubDelay(seconds: Double): Double =
    (seconds.coerceIn(-MAX_SUB_DELAY_S, MAX_SUB_DELAY_S) * 10).roundToInt() / 10.0

/** "+1.5" / "−0.5" / "0.0"; Turkish uses a decimal comma. */
internal fun delayText(seconds: Double, turkish: Boolean): String {
    val n = "%.1f".format(java.util.Locale.US, abs(seconds))
    val sign = if (seconds > 0) "+" else if (seconds < 0) "−" else ""
    return sign + if (turkish) n.replace('.', ',') else n
}

/** Player clock that floors (a position never shows a second it has not reached): 75.9 -> "1:15". */
internal fun formatTime(seconds: Double): String {
    val s = if (seconds.isFinite() && seconds > 0) floor(seconds).toInt() else 0
    val h = s / 3600
    val m = (s % 3600) / 60
    val sec = s % 60
    return if (h > 0) "%d:%02d:%02d".format(h, m, sec) else "%d:%02d".format(m, sec)
}

// ---- Adaptive start buffer (startBuffer.ts) ----

internal enum class HoldKind { Start, Stall }

/** rate / need at which a source starts at once */
internal const val HEALTHY_RATIO = 1.3
private const val START_AHEAD_S = 8.0
private const val START_MAX_WAIT_MS = 4000L
private const val RESUME_AHEAD_S = 4.0
private const val RESUME_MAX_WAIT_MS = 6000L
/** the player stopped downloading on its own with this much buffered: it will not fetch more */
private const val IDLE_AHEAD_S = 2.0

/** rate / need of a resolve answer's health; null when not measured. */
internal fun healthRatio(health: SourceHealth?): Double? {
    if (health == null) return null
    if (health.dead) return 0.0
    return if (health.need > 0) health.rate / health.need else null
}

/** A source whose start waits for a larger buffer: measured and borderline (unknown starts at once). */
internal fun needsStartHold(ratio: Double?): Boolean = ratio != null && ratio < HEALTHY_RATIO

/** The hold ends now: enough buffered, the file is in, the download stopped by itself, or the wait cap passed. */
internal fun holdDone(kind: HoldKind, ahead: Double, waitedMs: Long, fetching: Boolean, atEnd: Boolean): Boolean {
    val target = if (kind == HoldKind.Start) START_AHEAD_S else RESUME_AHEAD_S
    val cap = if (kind == HoldKind.Start) START_MAX_WAIT_MS else RESUME_MAX_WAIT_MS
    return ahead >= target || atEnd || waitedMs >= cap || (!fetching && ahead >= IDLE_AHEAD_S)
}

/** Wait before the n-th retry (n >= 1) of something that keeps failing: [baseMs] doubling up to [maxMs]. */
internal fun backoffMs(n: Int, baseMs: Long, maxMs: Long): Long =
    min(maxMs.toDouble(), baseMs * 2.0.pow(max(0, n - 1))).toLong()
