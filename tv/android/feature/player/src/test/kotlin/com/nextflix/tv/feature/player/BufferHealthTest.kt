package com.nextflix.tv.feature.player

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class BufferHealthTest {
    /** One sample per second of playback from [pos0]; the buffer end grows by [growth] per second. */
    private fun run(m: BufferMonitor, seconds: Int, ahead0: Double, growth: Double, fetching: Boolean = true, at0: Long = 0, pos0: Double = 100.0): BufferVerdict {
        var v: BufferVerdict? = null
        for (i in 0..seconds) {
            val pos = pos0 + i
            v = m.push(BufferSample(at = at0 + i * 1000L, pos = pos, end = pos0 + ahead0 + i * growth, fetching = fetching))
        }
        return v!!
    }

    @Test
    fun `a buffer that keeps pace is healthy and counts healthy time`() {
        val v = run(BufferMonitor(), 12, ahead0 = 30.0, growth = 1.2)
        assertEquals(BufferState.Healthy, v.state)
        assertTrue(v.healthyMs > 0)
    }

    @Test
    fun `growth is unknown until 6 s of playback`() {
        val v = run(BufferMonitor(), 4, ahead0 = 30.0, growth = 0.0)
        assertNull(v.growth)
        assertEquals(BufferState.Healthy, v.state)
        assertEquals(0L, v.healthyMs)
    }

    @Test
    fun `a slow download drains, close to empty it is critical`() {
        assertEquals(BufferState.Draining, run(BufferMonitor(), 8, ahead0 = 40.0, growth = 0.5).state)
        assertEquals(BufferState.Critical, run(BufferMonitor(), 8, ahead0 = 9.0, growth = 0.5).state)
    }

    @Test
    fun `a shrinking small buffer drains even when nearly keeping up`() {
        assertEquals(BufferState.Draining, run(BufferMonitor(), 8, ahead0 = 20.0, growth = 0.95).state)
        assertEquals(BufferState.Healthy, run(BufferMonitor(), 8, ahead0 = 40.0, growth = 0.95).state)
    }

    @Test
    fun `a stopped download (full buffer) is healthy`() {
        val v = run(BufferMonitor(), 8, ahead0 = 50.0, growth = 0.0, fetching = false)
        assertEquals(BufferState.Healthy, v.state)
        assertTrue(v.healthyMs > 0)
    }

    @Test
    fun `after the download idled, an almost empty buffer is critical within the grace`() {
        val m = BufferMonitor()
        m.push(BufferSample(at = 5000, pos = 100.0, end = 101.0, fetching = false))
        val v = m.push(BufferSample(at = 6000, pos = 101.0, end = 101.5, fetching = true))
        assertEquals(BufferState.Critical, v.state)
    }

    @Test
    fun `a seek or a gap starts the window over`() {
        val m = BufferMonitor()
        run(m, 8, ahead0 = 9.0, growth = 0.5)
        // jump far ahead: the old window does not count
        val v = m.push(BufferSample(at = 9000, pos = 2000.0, end = 2030.0, fetching = true))
        assertNull(v.growth)
        assertEquals(BufferState.Healthy, v.state)
    }
}
