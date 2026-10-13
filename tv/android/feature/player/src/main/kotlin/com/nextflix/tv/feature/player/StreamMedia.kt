package com.nextflix.tv.feature.player

import android.content.Context
import androidx.annotation.OptIn
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.DataSource
import androidx.media3.datasource.DataSpec
import androidx.media3.datasource.okhttp.OkHttpDataSource
import androidx.media3.exoplayer.DefaultLoadControl
import androidx.media3.exoplayer.DefaultRenderersFactory
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.MediaSource
import androidx.media3.exoplayer.source.ProgressiveMediaSource
import androidx.media3.exoplayer.trackselection.DefaultTrackSelector
import androidx.media3.exoplayer.upstream.DefaultLoadErrorHandlingPolicy
import androidx.media3.exoplayer.upstream.LoadErrorHandlingPolicy
import androidx.media3.extractor.Extractor
import androidx.media3.extractor.ExtractorInput
import androidx.media3.extractor.ExtractorOutput
import androidx.media3.extractor.ExtractorsFactory
import androidx.media3.extractor.PositionHolder
import androidx.media3.extractor.SeekMap
import androidx.media3.extractor.SeekPoint
import androidx.media3.extractor.mp4.FragmentedMp4Extractor
import androidx.media3.extractor.text.DefaultSubtitleParserFactory
import okhttp3.OkHttpClient
import java.io.IOException

// ExoPlayer plumbing for the server's /play stream: a live fragmented MP4 that starts at the requested time and
// cannot be re-read (a seek is a new request with a new t). Media3 must never retry or re-open it on its own:
// a retry would restart the server's stream from its beginning, so every load error goes to the engine.

/** Seconds of played video kept behind the position for seeks back without a new stream. */
internal const val BACK_BUFFER_S = 12

/** The player for one Player screen. */
@OptIn(UnstableApi::class)
internal fun createStreamPlayer(context: Context): ExoPlayer {
    val renderers = DefaultRenderersFactory(context)
        .setEnableDecoderFallback(true)
        .setExtensionRendererMode(DefaultRenderersFactory.EXTENSION_RENDERER_MODE_OFF)
    val loadControl = DefaultLoadControl.Builder()
        // Start as soon as a frame can play (the engine's start hold waits longer for weak swarms)
        .setBufferDurationsMs(50_000, 50_000, 1_000, 2_000)
        .setBackBuffer(BACK_BUFFER_S * 1000, true)
        // The back buffer counts against the byte target: at 4K remux bitrates it would leave no room ahead.
        // Media3 still stops early when the heap runs short.
        .setPrioritizeTimeOverSizeThresholds(true)
        .build()
    val trackSelector = DefaultTrackSelector(context).apply {
        setParameters(buildUponParameters().setTrackTypeDisabled(C.TRACK_TYPE_TEXT, true))
    }
    val audio = AudioAttributes.Builder().setUsage(C.USAGE_MEDIA).setContentType(C.AUDIO_CONTENT_TYPE_MOVIE).build()
    return ExoPlayer.Builder(context, renderers)
        .setLoadControl(loadControl)
        .setTrackSelector(trackSelector)
        .setAudioAttributes(audio, true)
        .setHandleAudioBecomingNoisy(true)
        .build()
}

/** Builds the media source of one /play stream. */
@OptIn(UnstableApi::class)
internal class StreamMediaFactory(client: OkHttpClient) {
    private val http = OkHttpDataSource.Factory(client)

    /**
     * [id] tags the stream (MediaItem.mediaId) so late events of an older one are ignored. [lengthS]: about how long
     * the stream runs; a known length lets Media3 seek inside what it buffered.
     */
    fun create(url: String, id: String, lengthS: Double): MediaSource {
        val durationUs = (lengthS.coerceAtLeast(1.0) * 1_000_000).toLong()
        val dataSources = DataSource.Factory { SingleOpenDataSource(http.createDataSource()) }
        val extractors = ExtractorsFactory {
            arrayOf(BufferSeekExtractor(FragmentedMp4Extractor(DefaultSubtitleParserFactory(), 0), durationUs))
        }
        return ProgressiveMediaSource.Factory(dataSources, extractors)
            .setLoadErrorHandlingPolicy(NoRetry)
            .createMediaSource(MediaItem.Builder().setUri(url).setMediaId(id).build())
    }
}

/** Thrown when Media3 tries to read the stream a second time (a seek outside the buffer). */
internal class StreamReopenException : IOException("stream re-opened")

/** The stream may be opened once: a live stream asked again would start over at its first byte. */
@OptIn(UnstableApi::class)
private class SingleOpenDataSource(private val inner: DataSource) : DataSource by inner {
    private var opened = false

    override fun open(dataSpec: DataSpec): Long {
        if (opened) throw StreamReopenException()
        opened = true
        return inner.open(dataSpec)
    }
}

/** Every load error is final: the engine restarts the stream itself. */
@OptIn(UnstableApi::class)
private object NoRetry : DefaultLoadErrorHandlingPolicy() {
    override fun getRetryDelayMsFor(loadErrorInfo: LoadErrorHandlingPolicy.LoadErrorInfo): Long = C.TIME_UNSET

    override fun getMinimumLoadableRetryCount(dataType: Int): Int = 0
}

/**
 * A fragmented MP4 without an index is unseekable to Media3, which turns every seek into a seek to 0. This wrapper
 * declares it seekable with a known length, so Media3 seeks inside its buffer; a seek beyond it would re-open the
 * stream, which [SingleOpenDataSource] refuses (the engine then restarts at the target).
 */
@OptIn(UnstableApi::class)
private class BufferSeekExtractor(private val inner: Extractor, private val durationUs: Long) : Extractor {
    override fun sniff(input: ExtractorInput): Boolean = inner.sniff(input)

    override fun init(output: ExtractorOutput) {
        inner.init(object : ExtractorOutput by output {
            override fun seekMap(seekMap: SeekMap) = output.seekMap(BufferSeekMap(durationUs))
        })
    }

    override fun read(input: ExtractorInput, seekPosition: PositionHolder): Int = inner.read(input, seekPosition)

    override fun seek(position: Long, timeUs: Long) = inner.seek(position, timeUs)

    override fun release() = inner.release()

    override fun getUnderlyingImplementation(): Extractor = inner
}

@OptIn(UnstableApi::class)
private class BufferSeekMap(private val durationUs: Long) : SeekMap {
    override fun isSeekable() = true

    override fun getDurationUs() = durationUs

    override fun getSeekPoints(timeUs: Long) = SeekMap.SeekPoints(SeekPoint.START)
}

/** The error came from a seek Media3 could not do inside its buffer. */
internal val PlaybackException.isReopen: Boolean
    get() = generateSequence(cause) { it.cause }.any { it is StreamReopenException }
