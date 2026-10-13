package com.nextflix.tv.feature.player

import com.nextflix.tv.core.stream.QualityCap
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class PlayRulesTest {
    @Test
    fun `openStart keeps a resume or switch away from the end`() {
        assertEquals(0.0, openStart(0.0, 3600.0, true))
        assertEquals(1200.0, openStart(1200.0, 3600.0, true))
        assertEquals(1200.0, openStart(1200.0, 3600.0, false))
        assertEquals(3589.0, openStart(3589.0, 3600.0, false))
        // Unknown length: the position is kept
        assertEquals(1200.0, openStart(1200.0, 0.0, false))
    }

    @Test
    fun `openStart watches again from 0 on the first resume at the very end`() {
        assertEquals(0.0, openStart(3595.0, 3600.0, true))
        assertEquals(0.0, openStart(3600.0, 3600.0, true))
    }

    @Test
    fun `openStart ends the title for a switch in the last 10 s`() {
        assertNull(openStart(3590.0, 3600.0, false))
        assertNull(openStart(3598.0, 3600.0, false))
        assertNull(openStart(4000.0, 3600.0, false))
    }

    @Test
    fun `canSeek only on an opened source`() {
        assertTrue(canSeek(Phase.Ready, fatal = false, duration = 3600.0, hash = "abc"))
        assertFalse(canSeek(Phase.Ready, fatal = false, duration = 3600.0, hash = ""))
        assertFalse(canSeek(Phase.Ready, fatal = false, duration = 0.0, hash = "abc"))
        assertFalse(canSeek(Phase.Ready, fatal = true, duration = 3600.0, hash = "abc"))
        // Finding another source: the failed source's hash and duration are still set
        assertFalse(canSeek(Phase.Resolving, fatal = false, duration = 3600.0, hash = "abc"))
        assertFalse(canSeek(Phase.Error, fatal = false, duration = 3600.0, hash = "abc"))
    }

    @Test
    fun `progressToSave keeps the saved progress until something played`() {
        assertNull(progressToSave(finished = false, itemPlayed = false, duration = 3600.0, exact = true, position = 1200.0, offset = 1190.0))
        assertEquals(3600.0, progressToSave(finished = true, itemPlayed = false, duration = 3600.0, exact = true, position = 1200.0, offset = 1190.0))
    }

    @Test
    fun `progressToSave uses the stream start until the seek point is known`() {
        assertEquals(1490.0, progressToSave(false, true, 3600.0, exact = false, position = 1500.0, offset = 1490.0))
        assertEquals(1500.0, progressToSave(false, true, 3600.0, exact = true, position = 1500.0, offset = 1490.0))
        assertEquals(3600.0, progressToSave(false, true, 3600.0, exact = true, position = 3700.0, offset = 1490.0))
    }

    @Test
    fun `progressToSave saves nothing without a length or under 10 s`() {
        assertNull(progressToSave(false, true, 0.0, true, 1500.0, 0.0))
        assertNull(progressToSave(false, true, 3600.0, true, 8.0, 0.0))
    }

    @Test
    fun `next episode warms 10 minutes before the end, short episodes at 85 percent`() {
        assertFalse(nextWarmDue(0.0, 3000.0))
        assertFalse(nextWarmDue(2300.0, 3000.0))
        assertTrue(nextWarmDue(2400.0, 3000.0))
        assertFalse(nextWarmDue(1200.0, 1420.0))
        assertTrue(nextWarmDue(850.0, 1000.0))
        assertFalse(nextWarmDue(849.0, 1000.0))
        assertFalse(nextWarmDue(100.0, 0.0))
    }

    @Test
    fun `lengths of two releases match within 30 s or 1 percent`() {
        assertTrue(lengthsMatch(3695.1, 3702.1))
        assertTrue(lengthsMatch(3600.0, 3630.0))
        assertFalse(lengthsMatch(3600.0, 3640.0))
        assertTrue(lengthsMatch(7200.0, 7270.0))
        assertTrue(lengthsMatch(0.0, 5000.0))
    }

    @Test
    fun `positions map by the length ratio only beyond 10 s apart`() {
        assertEquals(1.0, timeScale(3695.1, 3702.1), 0.0)
        assertEquals(3630.0 / 3600.0, timeScale(3600.0, 3630.0), 1e-9)
        assertEquals(1.0, timeScale(0.0, 3630.0), 0.0)
    }

    @Test
    fun `quality classes and step down caps`() {
        assertEquals(2160, videoQuality(3840, 1600))
        assertEquals(1080, videoQuality(1920, 800))
        assertEquals(720, videoQuality(1280, 534))
        assertEquals(480, videoQuality(720, 400))
        assertEquals(0, videoQuality(0, 0))
        assertEquals(QualityCap.P1080, lowerCap(2160))
        assertEquals(QualityCap.P720, lowerCap(1080))
        assertNull(lowerCap(720))
        assertEquals(2160, releaseHeight("2160p"))
        assertEquals(0, releaseHeight(null))
    }

    @Test
    fun `quality badge shows what reaches the screen`() {
        val copied = VideoInfo("hevc", 3840, 2160, hdr = true, dolbyVision = 5, direct = true)
        assertEquals(QualityBadge("4K", hdr = true, dv = true), qualityOf(copied, null, hevc = true))
        // Converted: at most 1080p SDR
        val converted = copied.copy(direct = false)
        assertEquals(QualityBadge("1080p", hdr = false, dv = false), qualityOf(converted, null, hevc = false))
        // Before the probe: the release name
        val src = StreamSource(hash = "h", index = 0, quality = "2160p", hdr = true)
        assertEquals(QualityBadge("4K", hdr = true, dv = false), qualityOf(null, src, hevc = true))
        assertEquals(QualityBadge("4K", hdr = false, dv = false), qualityOf(null, src, hevc = false))
        assertNull(qualityOf(null, StreamSource(hash = "h", index = 0), hevc = true))
    }

    @Test
    fun `scrub steps grow while a key is held`() {
        assertEquals(10, scrubStep(0))
        assertEquals(10, scrubStep(4))
        assertEquals(30, scrubStep(5))
        assertEquals(30, scrubStep(14))
        assertEquals(60, scrubStep(15))
    }

    @Test
    fun `subtitle delay is limited and shown with a sign`() {
        assertEquals(1.5, clampSubDelay(1.5), 0.0)
        assertEquals(60.0, clampSubDelay(75.0), 0.0)
        assertEquals(-60.0, clampSubDelay(-61.0), 0.0)
        assertEquals(0.3, clampSubDelay(0.29999), 0.0)
        assertEquals("+1.5", delayText(1.5, turkish = false))
        assertEquals("−0,5", delayText(-0.5, turkish = true))
        assertEquals("0.0", delayText(0.0, turkish = false))
    }

    @Test
    fun `player clock floors`() {
        assertEquals("0:00", formatTime(-3.0))
        assertEquals("1:15", formatTime(75.9))
        assertEquals("1:02:05", formatTime(3725.0))
        assertEquals("0:00", formatTime(Double.NaN))
    }

    @Test
    fun `start buffer holds only a measured borderline source`() {
        assertFalse(needsStartHold(null))
        assertTrue(needsStartHold(1.1))
        assertFalse(needsStartHold(1.3))
        assertEquals(0.0, healthRatio(SourceHealth(rate = 10.0, need = 5.0, keepsUp = false, dead = true)))
        assertEquals(2.0, healthRatio(SourceHealth(rate = 10.0, need = 5.0, keepsUp = true, dead = false)))
        assertNull(healthRatio(SourceHealth(rate = 10.0, need = 0.0, keepsUp = true, dead = false)))
    }

    @Test
    fun `holds end with enough buffer, at the end, after the cap or when the download stopped`() {
        assertFalse(holdDone(HoldKind.Start, ahead = 5.0, waitedMs = 1000, fetching = true, atEnd = false))
        assertTrue(holdDone(HoldKind.Start, ahead = 8.0, waitedMs = 0, fetching = true, atEnd = false))
        assertTrue(holdDone(HoldKind.Start, ahead = 1.0, waitedMs = 4000, fetching = true, atEnd = false))
        assertTrue(holdDone(HoldKind.Stall, ahead = 4.0, waitedMs = 0, fetching = true, atEnd = false))
        assertFalse(holdDone(HoldKind.Stall, ahead = 3.0, waitedMs = 5000, fetching = true, atEnd = false))
        assertTrue(holdDone(HoldKind.Stall, ahead = 0.5, waitedMs = 6000, fetching = true, atEnd = false))
        assertTrue(holdDone(HoldKind.Stall, ahead = 2.0, waitedMs = 0, fetching = false, atEnd = false))
        assertTrue(holdDone(HoldKind.Stall, ahead = 0.0, waitedMs = 0, fetching = true, atEnd = true))
    }

    @Test
    fun `skip buttons follow the position`() {
        val segs = SkipSegments(SkipSegment(60.0, 120.0), SkipSegment(3300.0, 3600.0))
        assertNull(skipPhase(segs, 30.0, 3600.0))
        assertEquals(SkipPhase.Intro, skipPhase(segs, 60.0, 3600.0))
        assertNull(skipPhase(segs, 119.5, 3600.0))
        assertEquals(SkipPhase.Credits, skipPhase(segs, 3400.0, 3600.0))
        assertNull(skipPhase(segs, 3599.5, 3600.0))
        assertNull(skipPhase(segs, 3400.0, 0.0))
        assertNull(skipPhase(null, 3400.0, 3600.0))
    }
}
