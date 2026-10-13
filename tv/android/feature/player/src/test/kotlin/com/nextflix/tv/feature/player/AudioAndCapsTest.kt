package com.nextflix.tv.feature.player

import com.nextflix.tv.core.prefs.HdrMode
import com.nextflix.tv.core.stream.PlaybackCaps
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AudioAndCapsTest {
    private fun track(index: Int, language: String, title: String = "", default: Boolean = false, channels: Int = 2, codec: String = "aac", direct: Boolean = true) =
        AudioTrack(index, language, title, codec, channels, direct, default)

    @Test
    fun `the last picked language wins, unless the preference is the original`() {
        val tracks = listOf(track(0, "en"), track(1, "tr"), track(2, "de"))
        assertEquals(2, pickAudioTrack(tracks, profileLang = "en", originalLang = "en", prefLang = "de"))
        assertEquals(0, pickAudioTrack(tracks, profileLang = "tr", originalLang = "en", prefLang = "original"))
    }

    @Test
    fun `a Turkish profile gets Turkish audio, others the original`() {
        val tracks = listOf(track(0, "en"), track(1, "tr"))
        assertEquals(1, pickAudioTrack(tracks, profileLang = "tr", originalLang = "en", prefLang = null))
        assertEquals(0, pickAudioTrack(tracks, profileLang = "en", originalLang = "en", prefLang = null))
    }

    @Test
    fun `three letter tags and titles name the original`() {
        val tagged = listOf(track(0, "rus"), track(1, "eng"))
        assertEquals(1, pickAudioTrack(tagged, profileLang = "en", originalLang = "en", prefLang = null))
        val titled = listOf(track(0, "", "Rus MVO"), track(1, "", "English 5.1"))
        assertEquals(1, pickAudioTrack(titled, profileLang = "en", originalLang = "en", prefLang = null))
    }

    @Test
    fun `an untagged track beats one tagged with another language`() {
        val tracks = listOf(track(0, "rus", default = true), track(1, "und"), track(2, "", "Dub"))
        assertEquals(1, pickAudioTrack(tracks, profileLang = "en", originalLang = "ja", prefLang = null))
    }

    @Test
    fun `without hints the default track, else the first`() {
        assertEquals(1, pickAudioTrack(listOf(track(0, "de"), track(1, "fr", default = true)), "en", null, null))
        assertEquals(0, pickAudioTrack(listOf(track(0, "de"), track(1, "fr")), "en", null, null))
        assertEquals(0, pickAudioTrack(emptyList(), "en", null, null))
    }

    @Test
    fun `audio badge says what reaches the TV`() {
        assertEquals("Atmos", audioBadge(track(0, "en", channels = 8, codec = "eac3"), atmosRelease = true))
        assertEquals("7.1", audioBadge(track(0, "en", channels = 8, codec = "eac3"), atmosRelease = false))
        assertEquals("5.1", audioBadge(track(0, "en", channels = 8, codec = "truehd", direct = false), atmosRelease = true))
        assertEquals("5.1", audioBadge(track(0, "en", channels = 6), atmosRelease = false))
        assertEquals("", audioBadge(track(0, "en", channels = 2), atmosRelease = false))
        assertEquals("", audioBadge(null, atmosRelease = true))
    }

    private val tv4k = DeviceFeatures(
        hevc = true, hdrDisplay = true, dvDecoder = true, dvDisplay = true,
        audioDecoders = setOf("aac", "opus", "mp3", "flac", "ac3"), passthrough = setOf("eac3"), atmos = true,
    )

    @Test
    fun `caps of a 4K HDR Dolby Vision TV with an Atmos receiver`() {
        val caps = capsOf(tv4k, HdrMode.Auto, safeAudio = false)
        assertEquals(PlaybackCaps(hevc = true, hdr = true, dv = true, atmos = true, audio = listOf("aac", "opus", "mp3", "flac", "ac3", "eac3")), caps)
        assertEquals(
            mapOf("hevc" to "1", "hdr" to "1", "dv" to "1", "atmos" to "1", "acodecs" to "aac,opus,mp3,flac,ac3,eac3"),
            caps.query(stream = true),
        )
    }

    @Test
    fun `HDR follows the setting, DV and Atmos need their base`() {
        assertFalse(capsOf(tv4k, HdrMode.Off, safeAudio = false).hdr)
        assertFalse(capsOf(tv4k, HdrMode.Off, safeAudio = false).dv)
        assertTrue(capsOf(tv4k.copy(hdrDisplay = false), HdrMode.On, safeAudio = false).hdr)
        val noHevc = capsOf(tv4k.copy(hevc = false), HdrMode.On, safeAudio = false)
        assertFalse(noHevc.hdr)
        assertFalse(noHevc.dv)
        assertFalse(capsOf(tv4k.copy(passthrough = emptySet()), HdrMode.Auto, safeAudio = false).atmos)
    }

    @Test
    fun `safe audio keeps only AAC and Opus, AAC always`() {
        val safe = capsOf(tv4k, HdrMode.Auto, safeAudio = true)
        assertEquals(listOf("aac", "opus"), safe.audio)
        assertFalse(safe.atmos)
        val bare = capsOf(tv4k.copy(audioDecoders = emptySet(), passthrough = emptySet()), HdrMode.Auto, safeAudio = false)
        assertEquals(listOf("aac"), bare.audio)
    }
}
