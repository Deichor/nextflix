package com.nextflix.tv.feature.player

import android.content.Context
import android.hardware.display.DisplayManager
import android.media.MediaCodecInfo
import android.media.MediaCodecInfo.CodecProfileLevel
import android.media.MediaCodecList
import android.os.Build
import android.util.Log
import android.view.Display
import androidx.annotation.OptIn
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.util.UnstableApi
import androidx.media3.exoplayer.audio.AudioCapabilities
import com.nextflix.tv.core.prefs.DevicePrefs
import com.nextflix.tv.core.prefs.HdrMode
import com.nextflix.tv.core.stream.PlaybackCaps
import com.nextflix.tv.core.stream.PlaybackCapsProvider

/**
 * Playback capabilities of this device for the server's caps query (docs/API.md "Playback capabilities"), the same
 * set on every streaming call. From the hardware decoders (MediaCodecList), the display's HDR types and the audio
 * output's passthrough formats (AudioCapabilities: a soundbar or receiver on HDMI takes AC-3 / E-AC-3 / Atmos).
 * Detected once; [invalidate] (or a new HDR setting) detects again.
 */
class AndroidPlaybackCaps(context: Context, private val devicePrefs: DevicePrefs) : PlaybackCapsProvider {
    private val appContext = context.applicationContext

    @Volatile
    private var features: DeviceFeatures? = null

    @Volatile
    private var cached: Pair<HdrMode, PlaybackCaps>? = null

    @Volatile
    private var safeAudio = false

    override fun current(): PlaybackCaps {
        val mode = devicePrefs.settings.value.hdrMode
        cached?.let { (m, caps) -> if (m == mode) return caps }
        val f = features ?: detect().also { features = it }
        return capsOf(f, mode, safeAudio).also { cached = mode to it }
    }

    override fun invalidate() {
        features = null
        cached = null
    }

    /** Only AAC / Opus from now on (the server converts the rest); false when already restricted. */
    override fun restrictToSafeAudio(): Boolean {
        if (safeAudio) return false
        safeAudio = true
        cached = null
        return true
    }

    @OptIn(UnstableApi::class)
    private fun detect(): DeviceFeatures {
        val decoders = MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.filterNot { it.isEncoder }
        fun decodersOf(mime: String) = decoders.filter { info -> info.supportedTypes.any { it.equals(mime, ignoreCase = true) } }
        fun has(mime: String) = decodersOf(mime).isNotEmpty()

        // 4K HEVC Main10 in hardware: otherwise the server must not copy 4K / 10-bit HEVC
        val hevc = decodersOf(HEVC).any { info ->
            val caps = runCatching { info.getCapabilitiesForType(HEVC) }.getOrNull() ?: return@any false
            hardware(info) &&
                caps.profileLevels.any { it.profile == CodecProfileLevel.HEVCProfileMain10 } &&
                caps.videoCapabilities?.isSizeSupported(3840, 2160) == true
        }
        // Profile 5 needs a real Dolby Vision decoder (profile 8 also plays as HDR10 on HEVC)
        val dvDecoder = decodersOf(DOLBY_VISION).any { info ->
            val caps = runCatching { info.getCapabilitiesForType(DOLBY_VISION) }.getOrNull() ?: return@any false
            caps.profileLevels.any { it.profile == CodecProfileLevel.DolbyVisionProfileDvheStn }
        }
        val display = appContext.getSystemService(DisplayManager::class.java)?.getDisplay(Display.DEFAULT_DISPLAY)
        val hdrTypes = hdrTypes(display)

        @Suppress("DEPRECATION")
        val output = runCatching { AudioCapabilities.getCapabilities(appContext, AudioAttributes.DEFAULT, null) }.getOrNull()
        fun passthrough(encoding: Int) = output?.supportsEncoding(encoding) == true

        val audioDecoders = AUDIO_MIMES.filterValues { has(it) }.keys
        val passthroughCodecs = PASSTHROUGH.filterValues { passthrough(it) }.keys
        val features = DeviceFeatures(
            hevc = hevc,
            hdrDisplay = hdrTypes.any { it == HDR10 || it == HDR10_PLUS || it == DOLBY_VISION_HDR },
            dvDecoder = dvDecoder,
            dvDisplay = DOLBY_VISION_HDR in hdrTypes,
            audioDecoders = audioDecoders,
            passthrough = passthroughCodecs,
            atmos = passthrough(C.ENCODING_E_AC3_JOC) || has(EAC3_JOC),
        )
        Log.i("NextflixPlayer", "[caps] $features")
        return features
    }

    @Suppress("DEPRECATION")
    private fun hdrTypes(display: Display?): Set<Int> {
        if (display == null) return emptySet()
        val wide = display.hdrCapabilities?.supportedHdrTypes?.toSet().orEmpty()
        if (Build.VERSION.SDK_INT < 34) return wide
        // Any mode: boxes run their UI in an SDR mode and switch for the content
        val modes = display.supportedModes.flatMap { it.supportedHdrTypes.asList() }.toSet()
        return modes.ifEmpty { wide }
    }

    private fun hardware(info: MediaCodecInfo): Boolean =
        if (Build.VERSION.SDK_INT >= 29) {
            info.isHardwareAccelerated
        } else {
            val name = info.name.lowercase()
            !name.startsWith("omx.google.") && !name.startsWith("c2.android.")
        }

    private companion object {
        const val HEVC = "video/hevc"
        const val DOLBY_VISION = "video/dolby-vision"
        const val EAC3_JOC = "audio/eac3-joc"
        const val DOLBY_VISION_HDR = Display.HdrCapabilities.HDR_TYPE_DOLBY_VISION
        const val HDR10 = Display.HdrCapabilities.HDR_TYPE_HDR10
        const val HDR10_PLUS = Display.HdrCapabilities.HDR_TYPE_HDR10_PLUS

        /** Server (ffprobe) codec names -> decoder MIME types. */
        val AUDIO_MIMES = mapOf(
            "aac" to "audio/mp4a-latm",
            "opus" to "audio/opus",
            "mp3" to "audio/mpeg",
            "flac" to "audio/flac",
            "ac3" to "audio/ac3",
            "eac3" to "audio/eac3",
            "truehd" to "audio/true-hd",
            "dts" to "audio/vnd.dts",
        )

        /** Codecs an HDMI sink can take untouched. */
        @OptIn(UnstableApi::class)
        val PASSTHROUGH = mapOf(
            "ac3" to C.ENCODING_AC3,
            "eac3" to C.ENCODING_E_AC3,
            "truehd" to C.ENCODING_DOLBY_TRUEHD,
            "dts" to C.ENCODING_DTS,
        )
    }
}

/** What detection found (pure input of [capsOf]). */
internal data class DeviceFeatures(
    /** hardware 4K HEVC Main10 decoder */
    val hevc: Boolean,
    /** the display shows HDR10 / HDR10+ / Dolby Vision */
    val hdrDisplay: Boolean,
    /** Dolby Vision profile 5 decoder */
    val dvDecoder: Boolean,
    val dvDisplay: Boolean,
    /** server codec names with a decoder */
    val audioDecoders: Set<String>,
    /** server codec names the audio output takes untouched */
    val passthrough: Set<String>,
    /** E-AC-3 JOC (Atmos) reaches the speakers */
    val atmos: Boolean,
)

private val AUDIO_ORDER = listOf("aac", "opus", "mp3", "flac", "ac3", "eac3", "truehd", "dts")
private val SAFE_AUDIO = setOf("aac", "opus")

/**
 * Caps for the server. HDR / DV only with HEVC (and HDR per the HDR setting), Atmos only with E-AC-3; AAC always.
 * [safeAudio]: the device refused audio it claimed: only AAC / Opus.
 */
internal fun capsOf(f: DeviceFeatures, hdrMode: HdrMode, safeAudio: Boolean): PlaybackCaps {
    val hdr = f.hevc && when (hdrMode) {
        HdrMode.On -> true
        HdrMode.Off -> false
        HdrMode.Auto -> f.hdrDisplay
    }
    val playable = f.audioDecoders + f.passthrough + "aac"
    val audio = AUDIO_ORDER.filter { it in playable && (!safeAudio || it in SAFE_AUDIO) }
    return PlaybackCaps(
        hevc = f.hevc,
        hdr = hdr,
        dv = hdr && f.dvDecoder && f.dvDisplay,
        atmos = f.atmos && "eac3" in audio,
        audio = audio,
        splitAudio = emptyList(),
    )
}
