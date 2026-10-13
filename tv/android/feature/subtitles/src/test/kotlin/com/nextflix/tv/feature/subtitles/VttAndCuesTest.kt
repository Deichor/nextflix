package com.nextflix.tv.feature.subtitles

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class VttAndCuesTest {
    private fun near(expected: Double, actual: Double) = assertEquals(expected, actual, 1e-9)

    @Test
    fun parsesServerVtt() {
        val vtt = "﻿WEBVTT\r\n\r\nNOTE nextflix-partial\r\n\r\n" +
            "00:00:01.500 --> 00:00:03.000\r\n<i>Hello</i> &amp; welcome\r\nsecond line\r\n\r\n" +
            "00:01:02.250 --> 00:01:04.000 align:start position:10%\r\nLater\r\n\r\n" +
            "1\r\n00:00:00.200 --> 00:00:00.900\r\nCue with an id\r\n"
        val cues = Vtt.parse(vtt)
        assertEquals(3, cues.size)
        near(0.2, cues[0].start)
        assertEquals("Cue with an id", cues[0].text)
        near(1.5, cues[1].start)
        near(3.0, cues[1].end)
        assertEquals("<i>Hello</i> &amp; welcome\nsecond line", cues[1].text)
        near(62.25, cues[2].start)
        assertTrue(Vtt.PARTIAL_NOTE in vtt)
    }

    @Test
    fun parsesShortAndCommaTimes() {
        near(61.5, Vtt.parseTime("01:01.5")!!)
        near(3723.04, Vtt.parseTime("1:02:03,04")!!)
        near(36000.0, Vtt.parseTime("10:00:00.000")!!)
        assertEquals(null, Vtt.parseTime("nope"))
    }

    @Test
    fun dropsBrokenCues() {
        val vtt = "WEBVTT\n\n00:00:05.000 --> 00:00:04.000\nbackwards\n\n00:00:06.000 --> 00:00:07.000\n\n" +
            "00:00:08.000 --> xx\nno end\n\nSTYLE\n::cue { color: red }\n\n00:00:09.000 --> 00:00:10.000\nok"
        val cues = Vtt.parse(vtt)
        assertEquals(listOf("ok"), cues.map { it.text })
    }

    @Test
    fun emptyAnswerHasNoCues() {
        assertEquals(emptyList<Cue>(), Vtt.parse(""))
        assertEquals(emptyList<Cue>(), Vtt.parse("WEBVTT\n\nNOTE nextflix-ai-translating\n\n"))
    }

    private val timed = listOf(
        Cue(10.0, 12.0, "a"),
        Cue(11.0, 20.0, "b"), // overlaps the first, ends after the next starts
        Cue(15.0, 16.0, "c"),
        Cue(30.0, 31.0, "d"),
    )

    @Test
    fun activeCuesIncludeOverlaps() {
        assertEquals(emptyList<Cue>(), CueLookup.active(timed, 9.99))
        assertEquals(listOf("a"), CueLookup.active(timed, 10.0).map { it.text })
        assertEquals(listOf("a", "b"), CueLookup.active(timed, 11.5).map { it.text })
        assertEquals(listOf("b"), CueLookup.active(timed, 12.0).map { it.text })
        assertEquals(listOf("b", "c"), CueLookup.active(timed, 15.5).map { it.text })
        assertEquals(emptyList<Cue>(), CueLookup.active(timed, 25.0))
        assertEquals(listOf("d"), CueLookup.active(timed, 30.5).map { it.text })
        assertEquals(emptyList<Cue>(), CueLookup.active(timed, 31.0))
        assertEquals(emptyList<Cue>(), CueLookup.active(emptyList(), 5.0))
    }

    @Test
    fun nextChangeIsTheNextStartOrTheEndOfAShownCue() {
        near(10.0, CueLookup.nextChange(timed, 0.0))
        near(1.0, CueLookup.nextChange(timed, 10.0))
        near(0.5, CueLookup.nextChange(timed, 11.5))
        near(3.0, CueLookup.nextChange(timed, 12.0))
        near(0.5, CueLookup.nextChange(timed, 15.5))
        near(4.0, CueLookup.nextChange(timed, 16.0))
        near(5.0, CueLookup.nextChange(timed, 25.0))
        assertEquals(Double.POSITIVE_INFINITY, CueLookup.nextChange(timed, 31.0), 0.0)
        assertEquals(Double.POSITIVE_INFINITY, CueLookup.nextChange(emptyList(), 5.0), 0.0)
    }

    @Test
    fun checkDelayRightAfterTheBoundary() {
        assertEquals(54L, CueLookup.checkDelayMs(timed, 9.95))
        assertEquals(CueLookup.CHECK_MAX_MS, CueLookup.checkDelayMs(timed, 5.0))
        assertEquals(CueLookup.CHECK_MAX_MS, CueLookup.checkDelayMs(timed, 31.0))
        assertTrue(CueLookup.checkDelayMs(timed, 10.0) >= 4)
    }

    @Test
    fun manyOverlappingCuesWithinTheLookBack() {
        val cues = (0 until 30).map { Cue(it.toDouble(), 100.0, "c$it") }
        // Only the last 12 started cues are looked at (like webOS)
        assertEquals(12, CueLookup.active(cues, 50.0).size)
        assertEquals("c29", CueLookup.active(cues, 50.0).last().text)
    }

    @Test
    fun markupKeepsOnlyItalicBoldUnderline() {
        val runs = CueMarkup.runs("<i>Hi</i> <font color=\"red\">there</font>\n<b.loud>BIG</b> <u>u</u> <c.yellow>x</c> &lt;3 &#252;")
        assertEquals(
            listOf(
                CueMarkup.Run("Hi", italic = true),
                CueMarkup.Run(" "),
                CueMarkup.Run("there"),
                CueMarkup.Run("\n"),
                CueMarkup.Run("BIG", bold = true),
                CueMarkup.Run(" "),
                CueMarkup.Run("u", underline = true),
                CueMarkup.Run(" "),
                CueMarkup.Run("x"),
                CueMarkup.Run(" <3 ü"),
            ),
            runs,
        )
    }

    @Test
    fun markupUnbalancedTagsNeverGoNegative() {
        val runs = CueMarkup.runs("</i>a<I>b</i></i>c")
        assertEquals(listOf(CueMarkup.Run("a"), CueMarkup.Run("b", italic = true), CueMarkup.Run("c")), runs)
    }

    @Test
    fun markupLeavesTextThatOnlyLooksLikeATag() {
        assertEquals(listOf(CueMarkup.Run("<-- look")), CueMarkup.runs("<-- look"))
        assertEquals("a b", CueMarkup.decode("a&nbsp;b"))
        assertEquals("&unknown;", CueMarkup.decode("&unknown;"))
        assertEquals("‎ab", CueMarkup.decode("&#x200E;a&lrm;b"))
    }
}
