package com.nextflix.tv.feature.account.intro

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.abs
import kotlin.math.log10
import kotlin.random.Random

class IntroTest {
    @Test
    fun trackHoldsEndsAndInterpolatesLinearly() {
        val stage = IntroTracks.stage
        assertEquals(1f, stage.at(-1f, 0), 0f)
        assertEquals(1f, stage.at(2.25f, 0), 1e-6f)
        assertEquals(0.5f, stage.at(2.5f, 0), 1e-4f)
        assertEquals(0f, stage.at(2.8f, 0), 0f)
        assertEquals(0f, stage.at(10f, 0), 0f)
    }

    @Test
    fun nLandsOnTheFirstHitAndZoomsOut() {
        val n = IntroTracks.n(lite = false)
        assertEquals(0f, n.at(0.05f, 1), 0f)
        assertEquals(1f, n.at(Intro.HIT1, 0), 1e-4f)
        assertEquals(1f, n.at(Intro.HIT1, 1), 1e-4f)
        assertEquals(9f, n.at(2.6f, 0), 1e-4f)
        assertEquals(4f, IntroTracks.n(lite = true).at(2.6f, 0), 1e-4f)
    }

    @Test
    fun ribbonsAreSeededAndFanOutToBothSides() {
        val a = IntroTracks.ribbons(14)
        val b = IntroTracks.ribbons(14)
        assertEquals(14, a.size)
        for (i in a.indices) {
            assertEquals(a[i].width, b[i].width, 0f)
            assertEquals(a[i].track.at(1.5f, 0), b[i].track.at(1.5f, 0), 0f)
        }
        // Even ribbons go left, odd ones right; all are invisible before the second hit and at the end
        assertTrue(a[0].track.at(2f, 0) < 0f)
        assertTrue(a[1].track.at(2f, 0) > 0f)
        assertTrue(a.all { it.track.at(0.5f, 5) == 0f && it.track.at(Intro.DURATION, 5) == 0f })
        assertTrue(a.any { it.track.at(1.3f, 5) > 0.3f })
    }

    @Test
    fun fftConvolutionMatchesDirect() {
        val n = 64
        val x = FloatArray(n) { if (it < 20) Random(it).nextFloat() - 0.5f else 0f }
        val h = FloatArray(n) { if (it < 30) Random(100 + it).nextFloat() - 0.5f else 0f }
        val xr = x.copyOf()
        val xi = FloatArray(n)
        val hr = h.copyOf()
        val hi = FloatArray(n)
        Fft.transform(xr, xi, inverse = false)
        Fft.transform(hr, hi, inverse = false)
        for (k in 0 until n) {
            val re = xr[k] * hr[k] - xi[k] * hi[k]
            val im = xr[k] * hi[k] + xi[k] * hr[k]
            xr[k] = re
            xi[k] = im
        }
        Fft.transform(xr, xi, inverse = true)
        for (i in 0 until n) {
            var direct = 0f
            for (j in 0..i) direct += x[j] * h[i - j]
            assertEquals(direct, xr[i], 1e-4f)
            assertEquals(0f, xi[i], 1e-4f)
        }
    }

    @Test
    fun stingPeaksNearMinus12DbfsAndEndsSilent() {
        val rate = 22_050
        val out = StingSynth.render(rate, Random(1))
        val frames = out.size / 2
        assertTrue(frames >= (Intro.SOUND_END * rate).toInt())
        val peak = out.maxOf { abs(it) }
        val dbfs = 20 * log10(peak)
        assertTrue("peak $dbfs dBFS", dbfs in -18f..-6f)
        // Silent before the first hit and after the fade
        val hit1 = (Intro.HIT1 * rate).toInt()
        assertTrue((0 until hit1 * 2 - 2).all { abs(out[it]) < 1e-5f })
        val end = ((Intro.SOUND_END + 0.001f) * rate).toInt()
        assertTrue((end * 2 until out.size).all { out[it] == 0f })
    }
}
