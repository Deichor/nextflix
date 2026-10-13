package com.nextflix.tv.feature.subtitles

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SubsLogTest {
    private fun cue(start: Double, text: String) = Cue(start, start + 2, text)
    private fun cues(offset: Double, n: Int = 10) = List(n) { cue(60 + it * 10 + offset, "line $it") }
    private fun serverToken(s: String) = s.take(40).replace(Regex("[^A-Za-z0-9._:/+-]"), "_").ifEmpty { "-" }

    @Test
    fun tokens() {
        assertEquals("Breaking.Bad.S02E10.720p.WEB-DL", SubsLog.token("Breaking.Bad.S02E10.720p.WEB-DL"))
        assertEquals("Turkce_Altyazi_Sahane_ceviri", SubsLog.token("Türkçe Altyazı \"Şahane\"\nçeviri"))
        assertEquals("HDTV_x264", SubsLog.token("  [HDTV] x264 "))
        assertEquals("-", SubsLog.token(""))
        assertEquals("-", SubsLog.token(null))
        assertEquals("-", SubsLog.token("\"\"\""))
        val long = SubsLog.token("The.Long.Release.Name.Of.Some.Show.S02E10.1080p.NF.WEB-DL.DDP5.1.x264-GROUP")
        assertTrue(long.length <= 40)
        assertEquals(long, serverToken(long))
        assertEquals("abc", SubsLog.token("abc def", 4))
    }

    @Test
    fun syncValue() {
        assertEquals("synced", SubsLog.syncValue("synced"))
        assertEquals("poor", SubsLog.syncValue(" poor "))
        assertEquals("weird_value_x_1", SubsLog.syncValue("weird value;x=1"))
        assertEquals("-", SubsLog.syncValue(null))
        assertEquals("-", SubsLog.syncValue(""))
    }

    @Test
    fun cueSpanAndShift() {
        assertNull(SubsLog.cueSpan(emptyList()))
        assertEquals(1.2 to 2502.0, SubsLog.cueSpan(listOf(cue(1.234, "a"), cue(2501.96, "b"))))
        assertEquals(SubsLog.Shift(0.0, 0, 10), SubsLog.cueShift(cues(0.0), cues(0.0)))
        assertEquals(SubsLog.Shift(-3.2, 10, 10), SubsLog.cueShift(cues(0.0), cues(-3.24)))
        val next = cues(1.5).toMutableList()
        next[9] = cue(next[9].start + 20, next[9].text)
        assertEquals(1.5, SubsLog.cueShift(cues(0.0), next).shift!!, 1e-9)
        assertEquals(SubsLog.Shift(null, 0, 0), SubsLog.cueShift(cues(0.0), listOf(cue(1.0, "other"))))
    }

    @Test
    fun changeRules() {
        val a = SubsLog.Shown("a", cues(0.0), partial = true, sync = "pending")
        assertEquals(SubsLog.Change(true), SubsLog.change(null, a))
        assertEquals(SubsLog.Change(true), SubsLog.change(a, a.copy(id = "b")))
        // The same answer again is not logged
        val final = a.copy(partial = false, sync = "synced")
        assertFalse(SubsLog.change(final, final.copy()).log)
        assertFalse(SubsLog.change(final, final.copy(cues = cues(0.0))).log)
        // Timed by a poll: logged with the shift
        assertEquals(SubsLog.Change(true, 2.5, 10), SubsLog.change(a, SubsLog.Shown("a", cues(2.5), false, "synced")))
        assertEquals(SubsLog.Change(true, -1.0, 10), SubsLog.change(a, a.copy(cues = cues(-1.0))))
        assertEquals(SubsLog.Change(true, null, null), SubsLog.change(a, a.copy(cues = cues(0.0), partial = false, sync = "unchanged")))
    }

    @Test
    fun movedWhilePartialIsLoggedAtMostOnceAMinute() {
        val a = SubsLog.Shown("a", cues(0.0), partial = true, sync = "live")
        assertEquals(SubsLog.Change(false), SubsLog.change(a, a.copy(cues = cues(0.5)), SubsLog.MOVE_LOG_MS - 1))
        assertEquals(SubsLog.Change(true, 1.2, 10), SubsLog.change(a, a.copy(cues = cues(1.2)), SubsLog.MOVE_LOG_MS))
        assertTrue(SubsLog.change(a, a.copy(cues = cues(1.2), partial = false, sync = "synced"), 1000).log)
    }

    @Test
    fun liveTrackThatOnlyGrewIsNotLogged() {
        val empty = SubsLog.Shown("e", emptyList(), partial = true, sync = "-")
        val some = empty.copy(cues = cues(0.0, 5))
        assertTrue(SubsLog.change(empty, some).log)
        val more = some.copy(cues = cues(0.0, 10))
        assertFalse(SubsLog.change(some, more).log)
        assertTrue(SubsLog.change(more, more.copy(cues = cues(0.0, 12), partial = false)).log)
        val other = List(10) { cue(61.0 + it * 10, "satır $it") }
        assertTrue(SubsLog.change(more, more.copy(cues = other)).log)
    }

    @Test
    fun subsAndNoneFields() {
        val item = SubtitleTrack(id = "b3M6MTIzNDU2Nzg5MDpoYXNoOjE=", source = "turkcealtyazi", language = "tr", release = "Show S02E10 HDTV x264-LOL", label = "Türkçe")
        val shown = SubsLog.Shown(item.id, listOf(cue(12.34, "a"), cue(2560.04, "b")), true, "pending")
        assertEquals(
            mapOf(
                "id" to "b3M6MTIzNDU2Nzg5MDpoYXNo", "cues" to 2, "partial" to true, "lang" to "tr", "provider" to "turkcealtyazi",
                "name" to "Show_S02E10_HDTV_x264-LOL", "sync" to "pending", "why" to "open", "first" to 12.3, "last" to 2560.0,
                "subdelay" to 0.0,
            ),
            SubsLog.subsFields(item, shown, SubsLog.Why.Open, 0.0),
        )
        val f = SubsLog.subsFields(null, shown.copy(cues = emptyList()), SubsLog.Why.Poll, 1.5, SubsLog.Change(true, -2.0, 30))
        assertFalse("provider" in f)
        assertFalse("first" in f)
        assertEquals(-2.0, f["shift"])
        assertEquals(1.5, f["subdelay"])
        assertTrue(f.size <= 16)

        val items = listOf(SubtitleTrack("1", "subdl", "en"), SubtitleTrack("2", "embedded", "eng"), SubtitleTrack("3", "x", "en"))
        assertEquals(
            mapOf("why" to "missing", "lang" to "tr", "n" to 3, "have" to "en+eng", "audio" to "en"),
            SubsLog.noneFields("missing", "tr", items, mapOf("audio" to "en")),
        )
        assertEquals(mapOf("why" to "off", "lang" to "tr", "n" to 0), SubsLog.noneFields("off", "tr", null))
    }

    @Test
    fun delayLog() {
        val d = SubsLog.DelayLog()
        d.change(0.5, 1000)
        d.change(1.0, 1500)
        d.change(1.5, 2000)
        assertNull(d.take(2000 + SubsLog.DELAY_SETTLE_MS - 1))
        assertEquals(SubsLog.DelayChange(1.5, 0.0), d.take(2000 + SubsLog.DELAY_SETTLE_MS))
        assertNull(d.take(99999))
        assertEquals(1.5, d.current, 0.0)
        d.change(-2.0, 10000)
        assertEquals(SubsLog.DelayChange(-2.0, 1.5), d.take(10000 + SubsLog.DELAY_SETTLE_MS))

        val back = SubsLog.DelayLog()
        back.change(0.5, 0)
        back.change(0.0, 100)
        assertNull(back.take(10000))
        back.change(2.0, 20000)
        assertEquals(SubsLog.DelayChange(2.0, 0.0), back.take(20001, force = true))

        val reset = SubsLog.DelayLog()
        reset.change(3.0, 0)
        assertEquals(SubsLog.DelayChange(3.0, 0.0), reset.reset(0.0, 500))
        assertEquals(0.0, reset.current, 0.0)
        assertNull(reset.reset(0.0, 600))
        reset.change(1.0, 1000)
        assertEquals(SubsLog.DelayChange(1.0, 0.0), reset.take(1000 + SubsLog.DELAY_SETTLE_MS))
    }
}
