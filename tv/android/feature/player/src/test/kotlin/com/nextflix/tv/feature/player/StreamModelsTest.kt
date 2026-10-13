package com.nextflix.tv.feature.player

import com.nextflix.tv.core.net.NextflixJson
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class StreamModelsTest {
    private fun json(text: String) = NextflixJson.parseToJsonElement(text)

    @Test
    fun `probe answer of the live server`() {
        val probe = ProbeInfo.parse(
            json(
                """{"audio_tracks":[{"channels":2,"codec":"aac","default":true,"direct":true,"index":0,"language":"und","split_direct":true,"title":""}],
                "direct":true,"dolby_vision":0,"duration":5796.46,"duration_mismatch":false,"expected_runtime":96,"hdr":false,
                "height":1072,"split":true,"video_codec":"h264","width":1472}""",
            ),
        )
        assertEquals(5796.46, probe.duration, 0.0)
        assertFalse(probe.durationMismatch)
        assertEquals(VideoInfo("h264", 1472, 1072, hdr = false, dolbyVision = 0, direct = true), probe.video)
        assertEquals(listOf(AudioTrack(0, "und", "", "aac", 2, direct = true, default = true)), probe.audioTracks)
    }

    @Test
    fun `probe without video`() {
        val probe = ProbeInfo.parse(json("""{"duration":0}"""))
        assertNull(probe.video)
        assertTrue(probe.audioTracks.isEmpty())
    }

    @Test
    fun `resolve answer becomes the playing source`() {
        val r = ResolvedSource.parse(
            json(
                """{"magnet":"magnet:?xt=urn:btih:abc","hash":"ABCDEF","index":3,"file_name":"a.mkv","torrent_title":"A 2160p",
                "quality":"2160p","hdr":true,"health":{"rate":3000000,"need":2000000,"active_peers":12,"seeders":40,"keeps_up":true},
                "cached":true,"format":{"atmos":true},"source":"torbox"}""",
            ),
        )!!
        assertTrue(r.cached)
        val src = r.toSource()
        assertEquals("ABCDEF", src.hash)
        assertEquals(3, src.index)
        assertEquals(1.5, src.ratio!!, 1e-9)
        assertFalse(src.starving)
        assertTrue(src.debrid)
        assertTrue(src.atmos)
        assertFalse(src.cinema)
    }

    @Test
    fun `a starving or unmeasured source`() {
        val starving = ResolvedSource.parse(json("""{"hash":"aa","index":0,"health":{"rate":1,"need":2,"keeps_up":false}}"""))!!.toSource()
        assertTrue(starving.starving)
        assertEquals(0.5, starving.ratio!!, 1e-9)
        val unmeasured = ResolvedSource.parse(json("""{"hash":"aa","index":0,"cinema":true}"""))!!.toSource()
        assertFalse(unmeasured.starving)
        assertNull(unmeasured.ratio)
        assertTrue(unmeasured.cinema)
        assertNull(ResolvedSource.parse(json("""{"index":0}""")))
    }

    @Test
    fun `skip segments drop broken ones`() {
        val segs = SkipSegments.parse(json("""{"intro":{"start":30,"end":95,"source":"chapters"},"credits":{"start":10,"end":5},"duration":2600}"""))!!
        assertEquals(SkipSegment(30.0, 95.0), segs.intro)
        assertNull(segs.credits)
        assertNull(SkipSegments.parse(json("null")))
    }
}
