package com.nextflix.tv.feature.player

import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import android.os.SystemClock
import com.nextflix.tv.core.Core
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.coroutines.launch
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt
import kotlin.math.roundToLong
import kotlin.random.Random

// Playback diagnostics (webOS player/playbackLog.ts): notable events of the player (waits, freezes, position jumps,
// dropped frame bursts, seeks and why, late timers, engine events) sent in small batches to POST /api/playback-log,
// which only writes them to the server log. Driven by the engine's 250 ms tick; no per-frame work.

/** What the log reads from the player at each tick. */
internal interface LogProbe {
    /** stream time (s) */
    val mediaTime: Double
    /** file position (s) */
    val position: Double
    /** seconds buffered ahead */
    val ahead: Double
    /** playing at normal speed (not paused, ended or seeking) */
    val running: Boolean
    /** Media3 playback state (1 idle, 2 buffering, 3 ready, 4 ended) */
    val playbackState: Int
    val loading: Boolean
    /** dropped and rendered video frames so far (-1 when unknown) */
    val droppedFrames: Int
    val renderedFrames: Int
    val videoWidth: Int
    val videoHeight: Int
}

/** Title and source of the batch (`ctx`). */
internal data class LogContext(
    val type: String,
    val tmdb: Int,
    val season: Int?,
    val episode: Int?,
    val title: String,
    val src: String,
    val debrid: Boolean,
    val quality: String,
    val duration: Int,
)

internal class PlaybackLog(
    private val core: Core,
    private val probe: LogProbe,
    private val context: () -> LogContext,
    /** fields added to every summary (the subtitle delay) */
    private val extra: () -> Map<String, Any?> = { emptyMap() },
) {
    private class Event(val t: Long, val k: String, val p: Double, val x: Map<String, Any>?)

    private val sid = Random.nextLong().toULong().toString(36).take(8)
    private val app: String by lazy { appVersion(core.appContext) }
    private var seq = 0
    private var events = ArrayList<Event>()
    private var dropped = 0
    private var ticks = 0
    private var lastTick = SystemClock.elapsedRealtime()
    private var lastFlush = SystemClock.elapsedRealtime()
    private var lastSummary = SystemClock.elapsedRealtime()

    // Media time against the wall clock
    private var gen = 0
    private var baseGen = -1
    private var lastCt = 0.0
    private var lag = 0.0
    private var lagTicks = 0
    private var lagWaited = false
    private var wait: Wait? = null
    private var seek: Seek? = null
    private var pendingMark: Pair<Long, String>? = null
    private var played = false
    private var frames = -1 to -1
    private var sum = Summary()

    private class Wait(val at: Long, val ct: Double, val p: Double, val x: Map<String, Any>)
    private class Seek(val at: Long, val from: Double, val why: String)
    private class Summary {
        var aheadMin = Double.MAX_VALUE
        var aheadSum = 0.0
        var aheadN = 0
        var dropped = 0
        var decoded = 0
        var waits = 0
        var waitMs = 0L
        var freezes = 0
        var freezeMs = 0.0
        var jumps = 0
        var late = 0
        var lateMax = 0L
    }

    // ---- engine API ----

    /** An engine event (restart, open, hold, health ...). */
    fun note(k: String, x: Map<String, Any?> = emptyMap()) {
        if (k == "restart" || k == "open") reset()
        push(k, x)
    }

    /** The next seek is the app's own, for [why] (user, skip-intro ...). */
    fun mark(why: String) {
        pendingMark = SystemClock.elapsedRealtime() to why
    }

    fun onWaiting(seeking: Boolean) {
        if (wait != null) return
        wait = Wait(now(), probe.mediaTime, probe.position, media() + mapOf("seeking" to seeking, "start" to !played))
    }

    fun onPlaying() {
        played = true
        closeWait("playing")
    }

    fun onSeeking() {
        val m = pendingMark?.takeIf { now() - it.first < MARK_MS }
        pendingMark = null
        seek = Seek(now(), lastCt, m?.second ?: "unmarked")
        reset()
    }

    fun onSeeked() {
        val s = seek ?: return
        seek = null
        reset()
        push("seek", mapOf("why" to s.why, "from" to s.from, "to" to probe.mediaTime, "d" to now() - s.at) + media())
    }

    fun onPause() {
        closeWait("pause")
        reset()
        if (now() - lastFlush > 2000) flush()
    }

    fun onEnded() {
        closeWait("ended")
        push("ended")
        summary()
        flush()
    }

    /** The stream was replaced or removed. */
    fun onEmptied() {
        closeWait("emptied")
        played = false
        frames = -1 to -1
        reset()
    }

    /** Sends what is queued. */
    fun flush() {
        if (events.isEmpty() && dropped == 0) return
        val all = events
        val lost = dropped
        events = ArrayList()
        dropped = 0
        lastFlush = now()
        var keep: List<Event> = all
        var body = body(keep, lost)
        // Over the cap: the oldest events go
        while (body.toString().length > MAX_BODY && keep.size > 1) {
            keep = keep.drop((keep.size + 3) / 4)
            body = body(keep, lost + all.size - keep.size)
        }
        core.scope.launch { quiet { core.api.raw("POST", "/api/playback-log", body = body).close() } }
    }

    fun destroy() {
        closeWait("exit")
        summary()
        flush()
    }

    /** Called every [TICK_MS] by the engine. */
    fun tick() {
        val t = now()
        val dWall = (t - lastTick) / 1000.0
        lastTick = t
        ticks++
        val ct = probe.mediaTime
        val running = probe.running && played
        // A tick late while playing: a long task on the main thread
        val late = (dWall * 1000 - TICK_MS).roundToLong()
        if (late > LATE_MS && running) {
            sum.late++
            sum.lateMax = max(sum.lateMax, late)
            push("late", mapOf("ms" to late))
        }
        wait?.let { if (running && ct > it.ct + 0.05) closeWait("progress") }
        if (baseGen != gen || !running) {
            baseGen = gen
            endLag()
        } else {
            val dCt = ct - lastCt
            if (dCt - dWall > JUMP_S || dCt < -0.05) {
                endLag()
                sum.jumps++
                push("jump", mapOf("by" to dCt - dWall, "from" to lastCt, "to" to ct) + media())
            } else if (dWall - dCt > 0.05) {
                lag += dWall - dCt
                lagTicks++
                if (wait != null) lagWaited = true
            } else {
                endLag()
            }
        }
        lastCt = ct

        if (ticks % AHEAD_EVERY == 0 && running) {
            val a = probe.ahead
            sum.aheadMin = min(sum.aheadMin, a)
            sum.aheadSum += a
            sum.aheadN++
        }
        if (ticks % FRAMES_EVERY == 0) sampleFrames()
        if (t - lastSummary >= SUMMARY_MS) summary()
        if (t - lastFlush >= FLUSH_MS) flush()
    }

    // ---- internals ----

    private fun now() = SystemClock.elapsedRealtime()

    private fun push(k: String, x: Map<String, Any?> = emptyMap()) {
        if (events.size >= MAX_EVENTS) {
            dropped++
            return
        }
        val fields = x.mapNotNull { (key, v) ->
            when (v) {
                null -> null
                is Double -> key to r2(v)
                is Float -> key to r2(v.toDouble())
                is Number, is Boolean, is String -> key to v
                else -> key to v.toString()
            }
        }.toMap()
        events.add(Event(System.currentTimeMillis(), k, r2(probe.position), fields.ifEmpty { null }))
    }

    private fun media(): Map<String, Any> = mapOf("ahead" to r2(probe.ahead), "rs" to probe.playbackState, "loading" to probe.loading)

    private fun reset() {
        gen++
        endLag()
    }

    private fun closeWait(end: String) {
        val w = wait ?: return
        wait = null
        val d = now() - w.at
        // Waits of a seek or a new stream are expected; still counted, without an event when short
        if (w.x["start"] == true || w.x["seeking"] == true) {
            if (d < 1000) return
        } else {
            sum.waits++
            sum.waitMs += d
        }
        push("wait", mapOf("d" to d, "end" to end, "from" to w.p) + w.x)
    }

    /** A run of slow progress ended: reported when the picture lost more than FREEZE_S without a wait. */
    private fun endLag() {
        val l = lag
        val n = lagTicks
        val waited = lagWaited
        lag = 0.0
        lagTicks = 0
        lagWaited = false
        if (l < FREEZE_S || waited) return
        sum.freezes++
        sum.freezeMs += l * 1000
        push("freeze", mapOf("ms" to (l * 1000).roundToInt(), "ticks" to n) + media())
    }

    private fun sampleFrames() {
        val dropped = probe.droppedFrames
        val total = probe.renderedFrames
        if (dropped < 0 || total < 0) return
        val (lastDropped, lastTotal) = frames
        if (lastTotal >= 0 && total >= lastTotal && dropped >= lastDropped) {
            val dd = dropped - lastDropped
            val dt = total - lastTotal
            sum.dropped += dd
            sum.decoded += dt
            if (dd > DROP_BURST) push("drops", mapOf("n" to dd, "of" to dt, "total" to dropped))
        }
        frames = dropped to total
    }

    private fun summary() {
        val s = sum
        lastSummary = now()
        sum = Summary()
        if (s.aheadN == 0 && s.waits == 0 && s.freezes == 0 && s.jumps == 0 && s.dropped == 0) return
        push(
            "summary",
            mapOf(
                "ahead_min" to s.aheadMin.takeIf { s.aheadN > 0 },
                "ahead_avg" to (s.aheadSum / s.aheadN).takeIf { s.aheadN > 0 },
                "dropped" to s.dropped, "decoded" to s.decoded,
                "waits" to s.waits, "wait_ms" to s.waitMs, "freezes" to s.freezes, "freeze_ms" to s.freezeMs.roundToInt(),
                "jumps" to s.jumps, "late" to s.late, "late_max" to s.lateMax,
                "w" to probe.videoWidth, "h" to probe.videoHeight,
            ) + extra(),
        )
    }

    private fun body(events: List<Event>, lost: Int): JsonObject {
        val c = context()
        return buildJsonObject {
            put("sid", sid)
            put("seq", ++seq)
            put("client", CLIENT)
            put("app", app)
            put(
                "ctx",
                buildJsonObject {
                    put("type", c.type)
                    put("tmdb", c.tmdb)
                    c.season?.let { put("s", it) }
                    c.episode?.let { put("e", it) }
                    put("title", c.title)
                    put("src", c.src)
                    put("mode", "muxed")
                    put("debrid", c.debrid)
                    put("q", c.quality)
                    put("dur", c.duration)
                },
            )
            put("dropped", lost)
            put("events", JsonArray(events.map { it.json() }))
        }
    }

    private fun Event.json(): JsonElement = buildJsonObject {
        put("t", t)
        put("k", k)
        put("p", p)
        x?.let { fields ->
            put("x", JsonObject(fields.mapValues { (_, v) -> primitive(v) }))
        }
    }

    private fun primitive(v: Any): JsonPrimitive = when (v) {
        is Boolean -> JsonPrimitive(v)
        is Number -> JsonPrimitive(v)
        else -> JsonPrimitive(v.toString())
    }

    companion object {
        /** The log's client tag ("tv" is the webOS app) */
        const val CLIENT = "atv"
        const val TICK_MS = 250L
        private const val FRAMES_EVERY = 8
        private const val AHEAD_EVERY = 4
        private const val FLUSH_MS = 30_000L
        private const val SUMMARY_MS = 60_000L
        private const val MAX_BODY = 32 * 1024
        private const val MAX_EVENTS = 150
        private const val DROP_BURST = 5
        private const val LATE_MS = 250L
        private const val FREEZE_S = 0.25
        private const val JUMP_S = 0.3
        private const val MARK_MS = 1500L

        private fun r2(n: Double) = (n * 100).roundToLong() / 100.0
    }
}

/** versionName of the installed app ("" when unknown), as in the settings' info block */
private fun appVersion(context: Context): String = try {
    val info = if (Build.VERSION.SDK_INT >= 33) {
        context.packageManager.getPackageInfo(context.packageName, PackageManager.PackageInfoFlags.of(0))
    } else {
        @Suppress("DEPRECATION")
        context.packageManager.getPackageInfo(context.packageName, 0)
    }
    info.versionName.orEmpty()
} catch (_: PackageManager.NameNotFoundException) {
    ""
}
