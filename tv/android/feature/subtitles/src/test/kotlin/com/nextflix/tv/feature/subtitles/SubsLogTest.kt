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

    // 100 cues every 10 s from 60 s; cues first..last (indexes) moved by [shift]
    private fun retimed(first: Int, last: Int, shift: Double) =
        cues(0.0, 100).mapIndexed { i, c -> if (i in first..last) cue(c.start + shift, c.text) else c }

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
    fun idHashIsFnv1aAndTellsIdsApartThatStartAlike() {
        assertEquals("811c9dc5", SubsLog.idHash(""))
        assertEquals("e40c292c", SubsLog.idHash("a"))
        assertEquals("bf9cf968", SubsLog.idHash("foobar"))
        // Two subtitles timed for one file: the first 24 chars are the same (values as in the webOS subsLog.test.mjs)
        val tr = "c3luY3xhMjJmMzdiNDJiMWQzYmE0NDdmYThjZDAxOGU3NDY2ZWFhMGJhMTFmfDF8c3ViZjJtfHR1cmtpc2h8MzU3ODQ0NQ"
        val en = "c3luY3xhMjJmMzdiNDJiMWQzYmE0NDdmYThjZDAxOGU3NDY2ZWFhMGJhMTFmfDF8c3ViZjJtfGVuZ2xpc2h8MzU3NjAzMQ"
        assertEquals(SubsLog.token(tr, 24), SubsLog.token(en, 24))
        assertEquals("71abc72f", SubsLog.idHash(tr))
        assertEquals("e6dc49ac", SubsLog.idHash(en))
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
        assertEquals(SubsLog.Moves(0.0, 0, 10), SubsLog.cueShift(cues(0.0), cues(0.0)))
        val s = SubsLog.cueShift(cues(0.0), cues(-3.24))
        assertEquals(-3.2, s.shift!!, 1e-9)
        assertEquals(10, s.moved)
        assertEquals(10, s.matched)
        val next = cues(1.5).toMutableList()
        next[9] = cue(next[9].start + 20, next[9].text)
        assertEquals(1.5, SubsLog.cueShift(cues(0.0), next).shift!!, 1e-9)
        assertEquals(SubsLog.Moves(null, 0, 0), SubsLog.cueShift(cues(0.0), listOf(cue(1.0, "other"))))
    }

    @Test
    fun movedCuesWhereAndHowFar() {
        // A re-fit moved 23 cues of 100 by -1.5 s, one of them by -2.25 s: the median of all stays 0
        val next = retimed(40, 62, -1.5).toMutableList()
        next[50] = cue(next[50].start - 0.75, next[50].text)
        val s = SubsLog.cueShift(cues(0.0, 100), next)
        assertEquals(0.0, s.shift!!, 1e-9)
        assertEquals(23, s.moved)
        assertEquals(458.5, s.from!!, 1e-9)
        assertEquals(678.5, s.to!!, 1e-9)
        assertEquals(-1.5, s.median!!, 1e-9)
        assertEquals(-2.25, s.max!!, 1e-9)
        // Without the position nothing about the viewer
        assertNull(s.near)
        assertNull(s.here)
    }

    @Test
    fun movedCuesNearTheViewerAndTheLineOnScreen() {
        val next = retimed(40, 62, -1.5)
        // The viewer at 500.5 sees line 44 (500-502): it moved by -1.5 s; lines 44-56 start from 495.5 to 620.5
        val at = SubsLog.cueShift(cues(0.0, 100), next, 500.5)
        assertEquals(-1.5, at.here!!, 1e-9)
        assertEquals(13, at.near)
        val gap = SubsLog.cueShift(cues(0.0, 100), next, 105.0)
        assertNull(gap.here)
        assertEquals(0, gap.near)
        // A moved cue whose old start was near counts too (it left the viewer)
        val away = cues(0.0, 100).mapIndexed { i, c -> if (i == 10) cue(c.start + 300, c.text) else c }
        assertEquals(1, SubsLog.cueShift(cues(0.0, 100), away, 155.0).near)
        assertEquals(0, SubsLog.cueShift(cues(0.0, 100), away, 160 + SubsLog.NEAR_AFTER_S + 1).near)
    }

    @Test
    fun lineOnScreenIsMatchedToItsNearestStart() {
        val prev = listOf(cue(100.0, "Yes."), cue(400.0, "No."), cue(900.0, "Yes."))
        val next = listOf(cue(100.0, "Yes."), cue(400.4, "No."), cue(900.5, "Yes."))
        assertEquals(0.5, SubsLog.cueShift(prev, next, 901.0).here!!, 1e-9)
        assertEquals(0.0, SubsLog.cueShift(prev, next, 101.0).here!!, 1e-9)
        assertEquals(0.4, SubsLog.cueShift(prev, next, 401.5).here!!, 1e-9)
        // Its line is gone from the new answer
        assertNull(SubsLog.cueShift(listOf(cue(10.0, "a"), cue(20.0, "b")), listOf(cue(10.5, "a")), 21.0).here)
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
        // Timed by a poll: logged with the moves
        val timed = SubsLog.change(a, SubsLog.Shown("a", cues(2.5), false, "synced"))
        assertTrue(timed.log)
        assertEquals(2.5, timed.moves!!.shift!!, 1e-9)
        assertEquals(10, timed.moves!!.moved)
        val step = SubsLog.change(a, a.copy(cues = cues(-1.0)))
        assertTrue(step.log)
        assertEquals(-1.0, step.moves!!.shift!!, 1e-9)
        assertEquals(SubsLog.Change(true), SubsLog.change(a, a.copy(cues = cues(0.0), partial = false, sync = "unchanged")))
    }

    @Test
    fun movesAwayFromTheViewerAreLoggedAtMostOnceAMinuteNearOnesAtOnce() {
        val a = SubsLog.Shown("a", cues(0.0, 100), partial = true, sync = "live")
        // The viewer at 100 s, the moves from 860 s on
        assertEquals(SubsLog.Change(false), SubsLog.change(a, a.copy(cues = retimed(80, 99, 0.5)), SubsLog.MOVE_LOG_MS - 1, 100.0))
        val later = SubsLog.change(a, a.copy(cues = retimed(80, 99, 1.2)), SubsLog.MOVE_LOG_MS, 100.0)
        assertTrue(later.log)
        assertEquals(1.2, later.moves!!.median!!, 1e-9)
        assertEquals(0, later.moves!!.near)
        // Moves at the viewer (161 s, line 10 on screen): logged at once
        val near = SubsLog.change(a, a.copy(cues = retimed(10, 30, -1.4)), 1000, 161.0)
        assertTrue(near.log)
        assertEquals(21, near.moves!!.moved)
        assertEquals(13, near.moves!!.near)
        assertEquals(-1.4, near.moves!!.here!!, 1e-9)
        // Without a position moves wait for the minute
        assertEquals(SubsLog.Change(false), SubsLog.change(a, a.copy(cues = retimed(10, 30, -1.4)), 1000))
        assertTrue(SubsLog.change(a, a.copy(cues = retimed(80, 99, 1.2), partial = false, sync = "synced"), 1000, 100.0).log)
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
                "id" to SubsLog.idHash(item.id), "cues" to 2, "partial" to true, "lang" to "tr", "provider" to "turkcealtyazi",
                "name" to "Show_S02E10_HDTV_x264-LOL", "sync" to "pending", "why" to "open", "first" to 12.3, "last" to 2560.0,
                "subdelay" to 0.0,
            ),
            SubsLog.subsFields(item, shown, SubsLog.Why.Open, 0.0),
        )
        val f = SubsLog.subsFields(null, shown.copy(cues = emptyList()), SubsLog.Why.Poll, 1.5, SubsLog.Moves(-2.0, 30, 600))
        assertFalse("provider" in f)
        assertFalse("first" in f)
        assertEquals(-2.0, f["shift"])
        assertEquals(30, f["moved"])
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
    fun pollThatMovedCuesNearTheViewerStaysWithinSixteenFields() {
        val item = SubtitleTrack(id = "x".repeat(90), source = "subf2m", language = "tr", release = "The.Mentalist.S03.WEBRip.Amazon.tr")
        val prev = SubsLog.Shown(item.id, cues(0.0, 100), partial = true, sync = "live")
        val next = prev.copy(cues = retimed(40, 62, -1.5))
        val change = SubsLog.change(prev, next, 1000, 500.5)
        val f = SubsLog.subsFields(item, next, SubsLog.Why.Poll, 0.0, change.moves)
        assertEquals(16, f.size)
        // The list item's fields come with the first event of a subtitle, not with its polls
        assertFalse("name" in f)
        assertEquals(listOf(458.5, 678.5, -1.5, -1.5, 13, -1.5), listOf(f["mfrom"], f["mto"], f["mshift"], f["mmax"], f["near"], f["here"]))
        assertTrue(f.keys.all { Regex("[a-z][a-z0-9_]{0,15}").matches(it) })
        assertEquals("The.Mentalist.S03.WEBRip.Amazon.tr", SubsLog.subsFields(item, next, SubsLog.Why.Open, 0.0)["name"])
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
