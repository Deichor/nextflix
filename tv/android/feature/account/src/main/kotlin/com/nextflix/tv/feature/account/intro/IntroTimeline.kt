package com.nextflix.tv.feature.account.intro

import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.Easing
import androidx.compose.animation.core.LinearEasing

// Timeline of the opening intro (webOS components/Intro.tsx keyframes and lib/intro.ts INTRO). Times in seconds,
// lengths in webOS rem (8 dp). The animation and the sting share it.

internal object Intro {
    const val DURATION = 2.8f
    const val HIT1 = 0.3f
    const val HIT2 = 1.05f
    const val SOUND_END = 2.55f
    const val SKIP_MS = 130L
}

private val EaseIn = CubicBezierEasing(0.42f, 0f, 1f, 1f)
private val EaseOut = CubicBezierEasing(0f, 0f, 0.58f, 1f)
private val EaseInOut = CubicBezierEasing(0.42f, 0f, 0.58f, 1f)

/** One keyframe: its time, the values of every channel and the easing of the segment that starts here (CSS). */
internal class Frame(t: Float, vararg val values: Float, val ease: Easing = LinearEasing) {
    val t: Float = t.coerceIn(0f, Intro.DURATION)
}

/** Keyframed channels, interpolated like a CSS animation with linear default timing. */
internal class Track(private val frames: List<Frame>) {
    val channels = frames.first().values.size

    fun at(time: Float, channel: Int): Float {
        if (time <= frames.first().t) return frames.first().values[channel]
        for (i in 0 until frames.lastIndex) {
            val a = frames[i]
            val b = frames[i + 1]
            if (time < b.t) {
                val span = b.t - a.t
                if (span <= 0f) return b.values[channel]
                val f = a.ease.transform(((time - a.t) / span).coerceIn(0f, 1f))
                return a.values[channel] + (b.values[channel] - a.values[channel]) * f
            }
        }
        return frames.last().values[channel]
    }
}

/** A light ribbon: channels x, y (rem), rotation (deg), scale x, scale y, opacity; [width] in rem; [tone] 0..2. */
internal class Ribbon(val track: Track, val width: Float, val tone: Int)

internal object IntroTracks {
    private const val H1 = Intro.HIT1
    private const val H2 = Intro.HIT2
    private const val D = Intro.DURATION

    /** The N: scale, opacity. */
    fun n(lite: Boolean): Track {
        val zoom = if (lite) 4f else 9f
        return Track(
            listOf(
                Frame(0f, 1.5f, 0f),
                Frame(0.08f, 1.5f, 0f, ease = CubicBezierEasing(0.6f, 0f, 0.9f, 0.45f)),
                Frame(H1, 1f, 1f, ease = EaseOut),
                Frame(H1 + 0.08f, 0.975f, 1f, ease = EaseInOut),
                Frame(H1 + 0.22f, 1f, 1f),
                Frame(H2, 1.05f, 1f, ease = EaseOut),
                Frame(H2 + 0.08f, 1.11f, 1f, ease = EaseInOut),
                Frame(1.95f, 1.16f, 1f, ease = CubicBezierEasing(0.55f, 0f, 0.85f, 0.35f)),
                Frame(2.5f, zoom, 0f),
                Frame(D, zoom, 0f),
            ),
        )
    }

    /** The N's hot glow: opacity. */
    val hot = Track(
        listOf(Frame(0f, 0f), Frame(H2 - 0.04f, 0f), Frame(H2 + 0.05f, 0.85f), Frame(1.75f, 0.25f), Frame(2.2f, 0f), Frame(D, 0f)),
    )

    /** Red glow behind the N: scale, opacity. */
    val glow = Track(
        listOf(
            Frame(0f, 0.6f, 0f),
            Frame(H1 - 0.04f, 0.6f, 0f),
            Frame(H1 + 0.03f, 0.9f, 0.6f),
            Frame(H2 - 0.05f, 1f, 0.3f),
            Frame(H2 + 0.07f, 1.25f, 0.95f),
            Frame(1.85f, 1.45f, 0.5f),
            Frame(2.55f, 2.4f, 0f),
            Frame(D, 2.4f, 0f),
        ),
    )

    /** White-red flash on the second hit: scale, opacity. */
    val flash = Track(
        listOf(
            Frame(0f, 0.3f, 0f),
            Frame(H2 - 0.02f, 0.3f, 0f),
            Frame(H2 + 0.05f, 1f, 0.85f),
            Frame(H2 + 0.55f, 1.8f, 0f),
            Frame(D, 1.8f, 0f),
        ),
    )

    /** Light sweep: x (rem), opacity. */
    val sweep = Track(
        listOf(Frame(0f, -70f, 0f), Frame(H2 - 0.1f, -70f, 0f), Frame(H2, -45f, 1f), Frame(H2 + 0.65f, 70f, 0f), Frame(D, 70f, 0f)),
    )

    /** The black stage: opacity (it fades out to show the app). */
    val stage = Track(listOf(Frame(0f, 1f), Frame(2.25f, 1f), Frame(2.75f, 0f), Frame(D, 0f)))

    /** The ribbons fanning out of the N on the second hit; seeded, so every launch looks the same. */
    fun ribbons(count: Int): List<Ribbon> {
        var seed = 7L
        fun rnd(): Float {
            seed = (seed * 16807L) % 2147483647L
            return (seed.toDouble() / 2147483647.0).toFloat()
        }
        val pairs = count / 2f
        return List(count) { i ->
            val side = if (i % 2 == 1) 1f else -1f
            val rank = i / 2
            val reach = 7f + (rank / maxOf(1f, pairs - 1f)) * 52f + rnd() * 4f
            val angle = side * (2f + rank * (12f / pairs) + rnd() * 2f)
            val y = (rnd() - 0.5f) * 8f
            val peak = 0.7f + rnd() * 0.3f
            val s = H2 - 0.03f + rank * 0.03f + rnd() * 0.04f
            val track = Track(
                listOf(
                    Frame(0f, 0f, y, 0f, 0.15f, 0.08f, 0f),
                    Frame(s, 0f, y, 0f, 0.15f, 0.08f, 0f, ease = CubicBezierEasing(0.2f, 0.7f, 0.3f, 1f)),
                    Frame(s + 0.14f, side * reach * 0.12f, y * 0.6f, angle * 0.25f, 1f, 0.85f, peak),
                    Frame(s + 0.6f, side * reach * 0.62f, y * 0.2f, angle * 0.8f, 1.3f, 1.05f, peak * 0.55f, ease = EaseIn),
                    Frame(s + 1.15f, side * reach, -y * 0.3f, angle, 1.7f, 1.15f, 0f),
                    Frame(D, side * reach, -y * 0.3f, angle, 1.7f, 1.15f, 0f),
                ),
            )
            Ribbon(track, width = 1.2f + rnd() * 3f, tone = i % 3)
        }
    }
}
